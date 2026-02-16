package proxy

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"janus/internal/auth"
	"janus/internal/storage"
)

// newTestDB creates an in-memory SQLite database wrapped in storage.DB.
// It creates the request_logs table needed by TodaySpend.
func newTestDB(t *testing.T) *storage.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	// Create the minimal table needed for TodaySpend queries.
	_, err = sqlDB.Exec(`CREATE TABLE IF NOT EXISTS request_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token_name TEXT,
		cost_usd REAL,
		timestamp INTEGER
	)`)
	if err != nil {
		t.Fatalf("creating request_logs table: %v", err)
	}
	return &storage.DB{DB: sqlDB}
}

// okHandler is a simple handler that writes 200 OK.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestRateLimiter_Allow(t *testing.T) {
	tests := []struct {
		name         string
		rateLimitRPM int
		numRequests  int
		wantAllowed  int
		wantDenied   int
	}{
		{
			name:         "allows requests within limit",
			rateLimitRPM: 10,
			numRequests:  5,
			wantAllowed:  5,
			wantDenied:   0,
		},
		{
			name:         "denies requests over limit",
			rateLimitRPM: 3,
			numRequests:  6,
			wantAllowed:  3,
			wantDenied:   3,
		},
		{
			name:         "allows exactly up to limit",
			rateLimitRPM: 5,
			numRequests:  5,
			wantAllowed:  5,
			wantDenied:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newTestDB(t)
			rl := NewRateLimiter(db)

			allowed := 0
			denied := 0
			for i := 0; i < tt.numRequests; i++ {
				ok, _, _ := rl.allow("test-token", tt.rateLimitRPM)
				if ok {
					allowed++
				} else {
					denied++
				}
			}

			if allowed != tt.wantAllowed {
				t.Errorf("allowed = %d, want %d", allowed, tt.wantAllowed)
			}
			if denied != tt.wantDenied {
				t.Errorf("denied = %d, want %d", denied, tt.wantDenied)
			}
		})
	}
}

func TestRateLimiter_TokenRefillsOverTime(t *testing.T) {
	db := newTestDB(t)
	rl := NewRateLimiter(db)

	rpm := 60 // 1 token per second refill rate

	// Exhaust all tokens.
	for i := 0; i < rpm; i++ {
		ok, _, _ := rl.allow("refill-token", rpm)
		if !ok {
			t.Fatalf("request %d should have been allowed", i)
		}
	}

	// Next request should be denied.
	ok, _, _ := rl.allow("refill-token", rpm)
	if ok {
		t.Fatal("request should have been denied after exhausting tokens")
	}

	// Manually advance the bucket's lastRefill to simulate time passing.
	rl.mu.Lock()
	bucket := rl.buckets["refill-token"]
	bucket.lastRefill = bucket.lastRefill.Add(-2 * time.Second)
	rl.mu.Unlock()

	// After 2 seconds at 1 token/sec, we should have ~2 tokens refilled.
	ok, remaining, _ := rl.allow("refill-token", rpm)
	if !ok {
		t.Fatal("request should have been allowed after token refill")
	}
	// We consumed 1 of the ~2 refilled tokens, so remaining should be >= 0.
	if remaining < 0 {
		t.Errorf("remaining = %d, want >= 0", remaining)
	}
}

func TestRateLimiter_AllowReturnValues(t *testing.T) {
	tests := []struct {
		name              string
		rateLimitRPM      int
		numRequests       int
		wantLastAllowed   bool
		wantLastRemaining int
	}{
		{
			name:              "remaining decrements correctly",
			rateLimitRPM:      5,
			numRequests:       1,
			wantLastAllowed:   true,
			wantLastRemaining: 4,
		},
		{
			name:              "remaining is zero at limit",
			rateLimitRPM:      3,
			numRequests:       3,
			wantLastAllowed:   true,
			wantLastRemaining: 0,
		},
		{
			name:              "denied returns zero remaining",
			rateLimitRPM:      2,
			numRequests:       3,
			wantLastAllowed:   false,
			wantLastRemaining: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newTestDB(t)
			rl := NewRateLimiter(db)

			var allowed bool
			var remaining int
			for i := 0; i < tt.numRequests; i++ {
				allowed, remaining, _ = rl.allow("test-token", tt.rateLimitRPM)
			}

			if allowed != tt.wantLastAllowed {
				t.Errorf("allowed = %v, want %v", allowed, tt.wantLastAllowed)
			}
			if remaining != tt.wantLastRemaining {
				t.Errorf("remaining = %d, want %d", remaining, tt.wantLastRemaining)
			}
		})
	}
}

func TestRateLimiter_CheckMiddleware_Headers(t *testing.T) {
	tests := []struct {
		name            string
		rateLimitRPM    int
		numRequests     int
		wantStatusCode  int
		wantLimitHeader string
		wantRetryAfter  bool
	}{
		{
			name:            "sets rate limit headers on allowed request",
			rateLimitRPM:    10,
			numRequests:     1,
			wantStatusCode:  http.StatusOK,
			wantLimitHeader: "10",
			wantRetryAfter:  false,
		},
		{
			name:            "sets Retry-After header on 429",
			rateLimitRPM:    2,
			numRequests:     3,
			wantStatusCode:  http.StatusTooManyRequests,
			wantLimitHeader: "2",
			wantRetryAfter:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newTestDB(t)
			rl := NewRateLimiter(db)

			token := &auth.APIToken{
				Name:         "header-test-token",
				RateLimitRPM: tt.rateLimitRPM,
			}

			handler := rl.Check(okHandler)

			var rec *httptest.ResponseRecorder
			for i := 0; i < tt.numRequests; i++ {
				rec = httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				ctx := withAPIToken(req.Context(), token)
				req = req.WithContext(ctx)
				handler.ServeHTTP(rec, req)
			}

			// Check the last response.
			if rec.Code != tt.wantStatusCode {
				t.Errorf("status code = %d, want %d", rec.Code, tt.wantStatusCode)
			}

			// X-RateLimit-Limit header.
			limitHeader := rec.Header().Get("X-RateLimit-Limit")
			if limitHeader != tt.wantLimitHeader {
				t.Errorf("X-RateLimit-Limit = %q, want %q", limitHeader, tt.wantLimitHeader)
			}

			// X-RateLimit-Remaining header must be present and numeric.
			remainingHeader := rec.Header().Get("X-RateLimit-Remaining")
			if remainingHeader == "" {
				t.Error("X-RateLimit-Remaining header is missing")
			} else if _, err := strconv.Atoi(remainingHeader); err != nil {
				t.Errorf("X-RateLimit-Remaining = %q, not a valid integer", remainingHeader)
			}

			// X-RateLimit-Reset header must be present and numeric.
			resetHeader := rec.Header().Get("X-RateLimit-Reset")
			if resetHeader == "" {
				t.Error("X-RateLimit-Reset header is missing")
			} else if _, err := strconv.ParseInt(resetHeader, 10, 64); err != nil {
				t.Errorf("X-RateLimit-Reset = %q, not a valid integer", resetHeader)
			}

			// Retry-After header.
			retryAfter := rec.Header().Get("Retry-After")
			if tt.wantRetryAfter && retryAfter == "" {
				t.Error("Retry-After header is missing on 429 response")
			}
			if !tt.wantRetryAfter && retryAfter != "" {
				t.Errorf("Retry-After header should not be present, got %q", retryAfter)
			}
		})
	}
}

func TestRateLimiter_CheckMiddleware_NoToken(t *testing.T) {
	db := newTestDB(t)
	rl := NewRateLimiter(db)

	handler := rl.Check(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No API token in context.
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d (should pass through without token)", rec.Code, http.StatusOK)
	}
}

func TestRateLimiter_CheckMiddleware_ZeroRPM(t *testing.T) {
	db := newTestDB(t)
	rl := NewRateLimiter(db)

	token := &auth.APIToken{
		Name:         "unlimited-token",
		RateLimitRPM: 0, // zero means unlimited
	}

	handler := rl.Check(okHandler)

	for i := 0; i < 100; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := withAPIToken(req.Context(), token)
		req = req.WithContext(ctx)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status code = %d, want %d (zero RPM should be unlimited)", i, rec.Code, http.StatusOK)
		}
	}
}

func TestRateLimiter_PerTokenIsolation(t *testing.T) {
	db := newTestDB(t)
	rl := NewRateLimiter(db)

	rpm := 2

	// Exhaust token A.
	for i := 0; i < rpm; i++ {
		rl.allow("token-a", rpm)
	}
	ok, _, _ := rl.allow("token-a", rpm)
	if ok {
		t.Fatal("token-a should be rate limited")
	}

	// Token B should still have its own bucket.
	ok, _, _ = rl.allow("token-b", rpm)
	if !ok {
		t.Fatal("token-b should not be affected by token-a's rate limit")
	}
}

func TestRateLimiter_ResetAtIsFuture(t *testing.T) {
	db := newTestDB(t)
	rl := NewRateLimiter(db)

	// Consume one token so there's a deficit.
	_, _, resetAt := rl.allow("reset-token", 10)
	now := time.Now().Unix()

	if resetAt < now {
		t.Errorf("resetAt = %d, want >= %d (should be now or in the future)", resetAt, now)
	}
}

func TestRateLimiter_CheckMiddleware_ContextCancelled(t *testing.T) {
	db := newTestDB(t)
	rl := NewRateLimiter(db)

	token := &auth.APIToken{
		Name:         "ctx-token",
		RateLimitRPM: 10,
	}

	handler := rl.Check(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, cancel := context.WithCancel(req.Context())
	ctx = withAPIToken(ctx, token)
	cancel() // Cancel immediately.
	req = req.WithContext(ctx)

	// Should still process (rate limiter does not check context cancellation).
	handler.ServeHTTP(rec, req)
	// The handler itself may or may not respect cancelled context;
	// the key point is no panic occurs.
}
