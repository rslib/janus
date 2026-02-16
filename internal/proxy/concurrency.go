package proxy

import (
	"net/http"
	"sync"
	"sync/atomic"
)

// ConcurrencyLimiter enforces per-token concurrent request limits.
type ConcurrencyLimiter struct {
	mu       sync.Mutex
	counters map[string]*atomic.Int64
}

func NewConcurrencyLimiter() *ConcurrencyLimiter {
	return &ConcurrencyLimiter{
		counters: make(map[string]*atomic.Int64),
	}
}

func (cl *ConcurrencyLimiter) getCounter(tokenName string) *atomic.Int64 {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	c, ok := cl.counters[tokenName]
	if !ok {
		c = &atomic.Int64{}
		cl.counters[tokenName] = c
	}
	return c
}

func (cl *ConcurrencyLimiter) Check(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiToken := getAPIToken(r.Context())
		if apiToken == nil || apiToken.MaxConcurrent <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		counter := cl.getCounter(apiToken.Name)
		current := counter.Add(1)
		defer counter.Add(-1)

		if current > int64(apiToken.MaxConcurrent) {
			writeError(w, http.StatusTooManyRequests, "concurrent request limit exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}
