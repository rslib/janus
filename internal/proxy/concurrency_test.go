package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"janus/internal/auth"
)

func TestConcurrencyLimiter_AllowsWithinLimit(t *testing.T) {
	tests := []struct {
		name          string
		maxConcurrent int
		numRequests   int
		wantAllowed   int
	}{
		{
			name:          "single request within limit",
			maxConcurrent: 5,
			numRequests:   1,
			wantAllowed:   1,
		},
		{
			name:          "all requests within limit",
			maxConcurrent: 5,
			numRequests:   5,
			wantAllowed:   5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := NewConcurrencyLimiter()

			token := &auth.APIToken{
				Name:          "conc-token",
				MaxConcurrent: tt.maxConcurrent,
			}

			var allowed atomic.Int64
			var wg sync.WaitGroup
			// Use a channel to hold all goroutines inside the handler simultaneously.
			gate := make(chan struct{})

			handler := cl.Check(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				allowed.Add(1)
				<-gate // Block until released.
				w.WriteHeader(http.StatusOK)
			}))

			for i := 0; i < tt.numRequests; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					rec := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					ctx := withAPIToken(req.Context(), token)
					req = req.WithContext(ctx)
					handler.ServeHTTP(rec, req)
				}()
			}

			// Wait for goroutines to enter the handler.
			time.Sleep(50 * time.Millisecond)
			close(gate)
			wg.Wait()

			if int(allowed.Load()) != tt.wantAllowed {
				t.Errorf("allowed = %d, want %d", allowed.Load(), tt.wantAllowed)
			}
		})
	}
}

func TestConcurrencyLimiter_DeniesOverLimit(t *testing.T) {
	tests := []struct {
		name          string
		maxConcurrent int
		numRequests   int
		wantDenied    int
	}{
		{
			name:          "one over limit",
			maxConcurrent: 2,
			numRequests:   3,
			wantDenied:    1,
		},
		{
			name:          "many over limit",
			maxConcurrent: 1,
			numRequests:   5,
			wantDenied:    4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := NewConcurrencyLimiter()

			token := &auth.APIToken{
				Name:          "conc-token",
				MaxConcurrent: tt.maxConcurrent,
			}

			var denied atomic.Int64
			var wg sync.WaitGroup
			gate := make(chan struct{})

			handler := cl.Check(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				<-gate
				w.WriteHeader(http.StatusOK)
			}))

			results := make([]int, tt.numRequests)
			for i := 0; i < tt.numRequests; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					rec := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					ctx := withAPIToken(req.Context(), token)
					req = req.WithContext(ctx)
					handler.ServeHTTP(rec, req)
					results[idx] = rec.Code
					if rec.Code == http.StatusTooManyRequests {
						denied.Add(1)
					}
				}(i)
			}

			// Wait for goroutines to reach the handler or be rejected.
			time.Sleep(50 * time.Millisecond)
			close(gate)
			wg.Wait()

			if int(denied.Load()) != tt.wantDenied {
				t.Errorf("denied = %d, want %d", denied.Load(), tt.wantDenied)
			}
		})
	}
}

func TestConcurrencyLimiter_CounterDecrementsAfterCompletion(t *testing.T) {
	cl := NewConcurrencyLimiter()

	token := &auth.APIToken{
		Name:          "decrement-token",
		MaxConcurrent: 1,
	}

	handler := cl.Check(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request should succeed and complete, decrementing the counter.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := withAPIToken(req.Context(), token)
	req = req.WithContext(ctx)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("first request: status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Counter should have decremented. A second request should also succeed.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx2 := withAPIToken(req2.Context(), token)
	req2 = req2.WithContext(ctx2)
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("second request after first completed: status = %d, want %d", rec2.Code, http.StatusOK)
	}

	// Verify the internal counter is back to 0.
	counter := cl.getCounter(token.Name)
	val := counter.Load()
	if val != 0 {
		t.Errorf("counter = %d, want 0 after all requests completed", val)
	}
}

func TestConcurrencyLimiter_ZeroMaxConcurrentMeansUnlimited(t *testing.T) {
	tests := []struct {
		name          string
		maxConcurrent int
		numRequests   int
	}{
		{
			name:          "zero allows unlimited concurrent requests",
			maxConcurrent: 0,
			numRequests:   50,
		},
		{
			name:          "negative allows unlimited concurrent requests",
			maxConcurrent: -1,
			numRequests:   50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := NewConcurrencyLimiter()

			token := &auth.APIToken{
				Name:          "unlimited-token",
				MaxConcurrent: tt.maxConcurrent,
			}

			var allowed atomic.Int64
			var wg sync.WaitGroup
			gate := make(chan struct{})

			handler := cl.Check(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				allowed.Add(1)
				<-gate
				w.WriteHeader(http.StatusOK)
			}))

			for i := 0; i < tt.numRequests; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					rec := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					ctx := withAPIToken(req.Context(), token)
					req = req.WithContext(ctx)
					handler.ServeHTTP(rec, req)
				}()
			}

			// Give goroutines time to enter the handler.
			time.Sleep(100 * time.Millisecond)
			close(gate)
			wg.Wait()

			if int(allowed.Load()) != tt.numRequests {
				t.Errorf("allowed = %d, want %d (zero/negative maxConcurrent should be unlimited)", allowed.Load(), tt.numRequests)
			}
		})
	}
}

func TestConcurrencyLimiter_NoToken(t *testing.T) {
	cl := NewConcurrencyLimiter()

	handler := cl.Check(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No API token in context.
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (should pass through without token)", rec.Code, http.StatusOK)
	}
}

func TestConcurrencyLimiter_PerTokenIsolation(t *testing.T) {
	cl := NewConcurrencyLimiter()

	tokenA := &auth.APIToken{
		Name:          "token-a",
		MaxConcurrent: 1,
	}
	tokenB := &auth.APIToken{
		Name:          "token-b",
		MaxConcurrent: 1,
	}

	gateA := make(chan struct{})
	var wg sync.WaitGroup

	handler := cl.Check(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := getAPIToken(r.Context())
		if tok.Name == "token-a" {
			<-gateA // Block token A's request.
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Start a request for token A that blocks.
	wg.Add(1)
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := withAPIToken(req.Context(), tokenA)
		req = req.WithContext(ctx)
		handler.ServeHTTP(rec, req)
	}()

	// Give token A's goroutine time to enter handler.
	time.Sleep(50 * time.Millisecond)

	// Token B should not be affected by token A's in-flight request.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := withAPIToken(req.Context(), tokenB)
	req = req.WithContext(ctx)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("token-b status = %d, want %d (should not be affected by token-a)", rec.Code, http.StatusOK)
	}

	close(gateA)
	wg.Wait()
}
