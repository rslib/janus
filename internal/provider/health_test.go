package provider

import (
	"errors"
	"testing"
	"time"

	"janus/internal/config"
)

func newTestTracker(window time.Duration, cb config.CircuitBreakerConfig) *HealthTracker {
	return NewHealthTracker(window, cb)
}

func defaultCBConfig() config.CircuitBreakerConfig {
	return config.CircuitBreakerConfig{
		Enabled:          true,
		ErrorThreshold:   0.5,
		MinRequests:      3,
		CooldownDuration: 100 * time.Millisecond,
	}
}

func TestHealthTracker_Record(t *testing.T) {
	ht := newTestTracker(5*time.Minute, config.CircuitBreakerConfig{})

	ht.Record("provA", 100, nil)
	ht.Record("provA", 200, errors.New("fail"))
	ht.Record("provB", 50, nil)

	ht.mu.RLock()
	defer ht.mu.RUnlock()

	if len(ht.windows["provA"]) != 2 {
		t.Fatalf("expected 2 events for provA, got %d", len(ht.windows["provA"]))
	}
	if len(ht.windows["provB"]) != 1 {
		t.Fatalf("expected 1 event for provB, got %d", len(ht.windows["provB"]))
	}

	// Verify event fields
	ev := ht.windows["provA"][1]
	if !ev.IsError || ev.ErrorMsg != "fail" || ev.LatencyMS != 200 {
		t.Fatalf("unexpected event fields: %+v", ev)
	}
}

func TestHealthTracker_Status(t *testing.T) {
	tests := []struct {
		name          string
		successCount  int
		errorCount    int
		wantStatus    string
		wantErrorRate float64
		wantTotal     int
		wantErrors    int
	}{
		{
			name:          "healthy - no errors",
			successCount:  10,
			errorCount:    0,
			wantStatus:    "healthy",
			wantErrorRate: 0.0,
			wantTotal:     10,
			wantErrors:    0,
		},
		{
			name:          "healthy - below 10% errors",
			successCount:  19,
			errorCount:    1,
			wantStatus:    "healthy",
			wantErrorRate: 0.05,
			wantTotal:     20,
			wantErrors:    1,
		},
		{
			name:          "degraded - 20% errors",
			successCount:  8,
			errorCount:    2,
			wantStatus:    "degraded",
			wantErrorRate: 0.2,
			wantTotal:     10,
			wantErrors:    2,
		},
		{
			name:          "degraded - exactly 10% errors",
			successCount:  9,
			errorCount:    1,
			wantStatus:    "degraded",
			wantErrorRate: 0.1,
			wantTotal:     10,
			wantErrors:    1,
		},
		{
			name:          "down - 50% errors",
			successCount:  5,
			errorCount:    5,
			wantStatus:    "down",
			wantErrorRate: 0.5,
			wantTotal:     10,
			wantErrors:    5,
		},
		{
			name:          "down - all errors",
			successCount:  0,
			errorCount:    5,
			wantStatus:    "down",
			wantErrorRate: 1.0,
			wantTotal:     5,
			wantErrors:    5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ht := newTestTracker(5*time.Minute, config.CircuitBreakerConfig{})

			for i := 0; i < tt.successCount; i++ {
				ht.Record("prov", 100, nil)
			}
			for i := 0; i < tt.errorCount; i++ {
				ht.Record("prov", 100, errors.New("err"))
			}

			statuses := ht.Status()
			if len(statuses) != 1 {
				t.Fatalf("expected 1 status, got %d", len(statuses))
			}

			s := statuses[0]
			if s.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", s.Status, tt.wantStatus)
			}
			if s.Total != tt.wantTotal {
				t.Errorf("total = %d, want %d", s.Total, tt.wantTotal)
			}
			if s.Errors != tt.wantErrors {
				t.Errorf("errors = %d, want %d", s.Errors, tt.wantErrors)
			}
			// Allow small float tolerance
			if diff := s.ErrorRate - tt.wantErrorRate; diff > 0.001 || diff < -0.001 {
				t.Errorf("error_rate = %f, want %f", s.ErrorRate, tt.wantErrorRate)
			}
		})
	}
}

func TestHealthTracker_CircuitBreaker_ClosedToOpen(t *testing.T) {
	cb := defaultCBConfig()
	cb.MinRequests = 3
	cb.ErrorThreshold = 0.5

	ht := newTestTracker(5*time.Minute, cb)

	// Record errors to exceed threshold (3 errors out of 3 = 100% > 50%)
	ht.Record("prov", 100, errors.New("err"))
	ht.Record("prov", 100, errors.New("err"))
	ht.Record("prov", 100, errors.New("err"))

	ht.mu.RLock()
	state := ht.circuits["prov"].State
	ht.mu.RUnlock()

	if state != CircuitOpen {
		t.Fatalf("expected CircuitOpen, got %s", state)
	}

	if ht.IsAvailable("prov") {
		t.Fatal("expected IsAvailable=false when circuit is open")
	}
}

func TestHealthTracker_CircuitBreaker_OpenToHalfOpenOnCooldown(t *testing.T) {
	cb := defaultCBConfig()
	cb.CooldownDuration = 50 * time.Millisecond

	ht := newTestTracker(5*time.Minute, cb)

	// Trip the circuit
	for i := 0; i < 5; i++ {
		ht.Record("prov", 100, errors.New("err"))
	}

	if ht.IsAvailable("prov") {
		t.Fatal("expected circuit open, IsAvailable should be false")
	}

	// Wait for cooldown
	time.Sleep(60 * time.Millisecond)

	// After cooldown, IsAvailable should return true (will transition to half-open)
	if !ht.IsAvailable("prov") {
		t.Fatal("expected IsAvailable=true after cooldown")
	}
}

func TestHealthTracker_CircuitBreaker_HalfOpenToClosedOnSuccess(t *testing.T) {
	cb := defaultCBConfig()
	cb.CooldownDuration = 10 * time.Millisecond

	ht := newTestTracker(5*time.Minute, cb)

	// Trip the circuit
	for i := 0; i < 5; i++ {
		ht.Record("prov", 100, errors.New("err"))
	}

	// Wait for cooldown so next Record transitions through Open->HalfOpen
	time.Sleep(20 * time.Millisecond)

	// A successful record should transition: Open -> HalfOpen -> Closed
	ht.Record("prov", 100, nil)

	ht.mu.RLock()
	state := ht.circuits["prov"].State
	ht.mu.RUnlock()

	if state != CircuitClosed {
		t.Fatalf("expected CircuitClosed after success in half-open, got %s", state)
	}

	if !ht.IsAvailable("prov") {
		t.Fatal("expected IsAvailable=true after circuit closed")
	}
}

func TestHealthTracker_CircuitBreaker_HalfOpenToOpenOnFailure(t *testing.T) {
	cb := defaultCBConfig()
	cb.CooldownDuration = 10 * time.Millisecond

	ht := newTestTracker(5*time.Minute, cb)

	// Trip the circuit
	for i := 0; i < 5; i++ {
		ht.Record("prov", 100, errors.New("err"))
	}

	// Wait for cooldown
	time.Sleep(20 * time.Millisecond)

	// A failed record should transition: Open -> HalfOpen -> Open
	ht.Record("prov", 100, errors.New("still failing"))

	ht.mu.RLock()
	state := ht.circuits["prov"].State
	ht.mu.RUnlock()

	if state != CircuitOpen {
		t.Fatalf("expected CircuitOpen after failure in half-open, got %s", state)
	}
}

func TestHealthTracker_IsAvailable_NoCircuitBreaker(t *testing.T) {
	ht := newTestTracker(5*time.Minute, config.CircuitBreakerConfig{Enabled: false})

	// Even with errors, IsAvailable should return true when CB is disabled
	for i := 0; i < 10; i++ {
		ht.Record("prov", 100, errors.New("err"))
	}

	if !ht.IsAvailable("prov") {
		t.Fatal("expected IsAvailable=true when circuit breaker disabled")
	}
}

func TestHealthTracker_IsAvailable_UnknownProvider(t *testing.T) {
	ht := newTestTracker(5*time.Minute, defaultCBConfig())

	if !ht.IsAvailable("unknown") {
		t.Fatal("expected IsAvailable=true for unknown provider (no circuit)")
	}
}

func TestHealthTracker_WindowPruning(t *testing.T) {
	// Use a tiny window so events expire quickly
	ht := newTestTracker(50*time.Millisecond, config.CircuitBreakerConfig{})

	ht.Record("prov", 100, nil)
	ht.Record("prov", 200, nil)

	// Wait for events to expire
	time.Sleep(60 * time.Millisecond)

	// Record a new event to trigger pruning
	ht.Record("prov", 300, nil)

	ht.mu.RLock()
	count := len(ht.windows["prov"])
	ht.mu.RUnlock()

	if count != 1 {
		t.Fatalf("expected 1 event after pruning, got %d", count)
	}
}

func TestHealthTracker_Status_EmptyAfterPruning(t *testing.T) {
	ht := newTestTracker(50*time.Millisecond, config.CircuitBreakerConfig{})

	ht.Record("prov", 100, nil)

	// Wait for events to expire
	time.Sleep(60 * time.Millisecond)

	statuses := ht.Status()
	if len(statuses) != 0 {
		t.Fatalf("expected 0 statuses after window expiry, got %d", len(statuses))
	}
}

func TestHealthTracker_Status_AvgLatency(t *testing.T) {
	ht := newTestTracker(5*time.Minute, config.CircuitBreakerConfig{})

	ht.Record("prov", 100, nil)
	ht.Record("prov", 200, nil)
	ht.Record("prov", 300, nil)

	statuses := ht.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	want := 200.0
	if diff := statuses[0].AvgLatency - want; diff > 0.001 || diff < -0.001 {
		t.Errorf("avg_latency = %f, want %f", statuses[0].AvgLatency, want)
	}
}

func TestHealthTracker_Status_CircuitStateReported(t *testing.T) {
	cb := defaultCBConfig()
	ht := newTestTracker(5*time.Minute, cb)

	// Trip the circuit
	for i := 0; i < 5; i++ {
		ht.Record("prov", 100, errors.New("err"))
	}

	statuses := ht.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}

	if statuses[0].CircuitState != "open" {
		t.Errorf("circuit_state = %q, want %q", statuses[0].CircuitState, "open")
	}
}
