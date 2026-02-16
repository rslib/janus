package proxy

import (
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"janus/internal/storage"
	"janus/internal/webhook"
)

type RateLimiter struct {
	db             *storage.DB
	mu             sync.Mutex
	buckets        map[string]*tokenBucket
	notifier       *webhook.Notifier
	budgetNotified sync.Map // tracks which token+budget combos have been notified
}

type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

func NewRateLimiter(db *storage.DB) *RateLimiter {
	return &RateLimiter{
		db:      db,
		buckets: make(map[string]*tokenBucket),
	}
}

// SetNotifier sets the webhook notifier for budget threshold alerts.
func (rl *RateLimiter) SetNotifier(n *webhook.Notifier) {
	rl.notifier = n
}

func (rl *RateLimiter) Check(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiToken := getAPIToken(r.Context())
		if apiToken == nil {
			next.ServeHTTP(w, r)
			return
		}

		tokenName := apiToken.Name

		// Check rate limit
		if apiToken.RateLimitRPM > 0 {
			allowed, remaining, resetAt := rl.allow(tokenName, apiToken.RateLimitRPM)

			// Set rate limit headers on all responses
			w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", apiToken.RateLimitRPM))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", resetAt))

			if !allowed {
				retryAfter := resetAt - time.Now().Unix()
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}

		// Check daily budget
		if apiToken.DailyBudgetUSD > 0 {
			spent, err := rl.db.TodaySpend(tokenName)
			if err == nil {
				if spent >= apiToken.DailyBudgetUSD {
					writeError(w, http.StatusTooManyRequests, "daily budget exceeded")
					return
				}
				rl.checkBudgetThreshold(tokenName, "daily", spent, apiToken.DailyBudgetUSD)
			}
		}

		// Check monthly budget
		if apiToken.MonthlyBudgetUSD > 0 {
			spent, err := rl.db.MonthSpend(tokenName)
			if err == nil {
				if spent >= apiToken.MonthlyBudgetUSD {
					writeError(w, http.StatusTooManyRequests, "monthly budget exceeded")
					return
				}
				rl.checkBudgetThreshold(tokenName, "monthly", spent, apiToken.MonthlyBudgetUSD)
			}
		}

		next.ServeHTTP(w, r)
	})
}

// checkBudgetThreshold fires a webhook notification when spend reaches 80% of budget.
func (rl *RateLimiter) checkBudgetThreshold(tokenName, budgetType string, spent, budget float64) {
	if rl.notifier == nil || budget <= 0 {
		return
	}
	if spent/budget < 0.8 {
		return
	}
	key := tokenName + ":" + budgetType
	if _, loaded := rl.budgetNotified.LoadOrStore(key, true); loaded {
		return // already notified
	}
	rl.notifier.Notify(webhook.Event{
		Type: webhook.EventBudgetThreshold,
		Data: map[string]any{
			"token":       tokenName,
			"budget_type": budgetType,
			"spent":       spent,
			"budget":      budget,
			"percent":     spent / budget * 100,
		},
	})
}

func (rl *RateLimiter) allow(tokenName string, rateLimitRPM int) (bool, int, int64) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, ok := rl.buckets[tokenName]
	if !ok {
		bucket = &tokenBucket{
			tokens:     float64(rateLimitRPM),
			maxTokens:  float64(rateLimitRPM),
			refillRate: float64(rateLimitRPM) / 60.0,
			lastRefill: time.Now(),
		}
		rl.buckets[tokenName] = bucket
	}

	now := time.Now()
	elapsed := now.Sub(bucket.lastRefill).Seconds()
	bucket.tokens += elapsed * bucket.refillRate
	if bucket.tokens > bucket.maxTokens {
		bucket.tokens = bucket.maxTokens
	}
	bucket.lastRefill = now

	remaining := int(math.Floor(bucket.tokens))
	if remaining < 0 {
		remaining = 0
	}

	// Compute reset time: when bucket would be full again
	deficit := bucket.maxTokens - bucket.tokens
	var resetAt int64
	if deficit > 0 && bucket.refillRate > 0 {
		resetAt = now.Add(time.Duration(deficit/bucket.refillRate) * time.Second).Unix()
	} else {
		resetAt = now.Unix()
	}

	if bucket.tokens < 1 {
		return false, 0, resetAt
	}
	bucket.tokens--
	remaining = int(math.Floor(bucket.tokens))
	if remaining < 0 {
		remaining = 0
	}
	return true, remaining, resetAt
}
