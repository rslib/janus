package provider

import (
	"sync"
	"time"

	"janus/internal/config"
)

// CircuitState represents the state of a circuit breaker.
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // normal operation
	CircuitOpen                         // blocking requests
	CircuitHalfOpen                     // testing with probe request
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// ProviderCircuit tracks circuit breaker state for a single provider.
type ProviderCircuit struct {
	State     CircuitState
	OpenedAt  time.Time
	LastProbe time.Time
}

// HealthEvent represents a single request outcome for a provider.
type HealthEvent struct {
	Timestamp time.Time
	LatencyMS int64
	IsError   bool
	ErrorMsg  string
}

// ProviderHealth is the computed health status for a provider.
type ProviderHealth struct {
	Provider     string  `json:"provider"`
	Status       string  `json:"status"` // healthy, degraded, down
	ErrorRate    float64 `json:"error_rate"`
	AvgLatency   float64 `json:"avg_latency_ms"`
	Total        int     `json:"total"`
	Errors       int     `json:"errors"`
	CircuitState string  `json:"circuit_state"`
}

// HealthTracker tracks per-provider health using a sliding window.
type HealthTracker struct {
	mu            sync.RWMutex
	windows       map[string][]HealthEvent
	windowDu      time.Duration
	circuits      map[string]*ProviderCircuit
	cbConfig      config.CircuitBreakerConfig
	OnStateChange func(provider string, from, to CircuitState)
}

// NewHealthTracker creates a health tracker with the given window duration.
func NewHealthTracker(window time.Duration, cbCfg config.CircuitBreakerConfig) *HealthTracker {
	if window == 0 {
		window = 5 * time.Minute
	}
	return &HealthTracker{
		windows:  make(map[string][]HealthEvent),
		circuits: make(map[string]*ProviderCircuit),
		windowDu: window,
		cbConfig: cbCfg,
	}
}

// IsAvailable returns true if the provider's circuit breaker allows requests.
func (h *HealthTracker) IsAvailable(provider string) bool {
	if !h.cbConfig.Enabled {
		return true
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	circuit, ok := h.circuits[provider]
	if !ok {
		return true // no circuit = closed = available
	}

	switch circuit.State {
	case CircuitOpen:
		// Check if cooldown has elapsed -> transition to half-open
		if time.Since(circuit.OpenedAt) >= h.cbConfig.CooldownDuration {
			return true // will transition to half-open on next record
		}
		return false
	case CircuitHalfOpen:
		return true // allow probe
	default:
		return true
	}
}

// Record adds a health event for a provider and evaluates circuit transitions.
func (h *HealthTracker) Record(provider string, latencyMS int64, err error) {
	event := HealthEvent{
		Timestamp: time.Now(),
		LatencyMS: latencyMS,
		IsError:   err != nil,
	}
	if err != nil {
		event.ErrorMsg = err.Error()
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.windows[provider] = append(h.windows[provider], event)
	h.prune(provider)

	if h.cbConfig.Enabled {
		h.evaluateCircuit(provider, err)
	}
}

// evaluateCircuit transitions circuit breaker state. Must be called with lock held.
func (h *HealthTracker) evaluateCircuit(providerName string, lastErr error) {
	circuit, ok := h.circuits[providerName]
	if !ok {
		circuit = &ProviderCircuit{State: CircuitClosed}
		h.circuits[providerName] = circuit
	}

	prevState := circuit.State

	switch circuit.State {
	case CircuitClosed:
		// Check if error threshold exceeded
		errorRate, total := h.errorRateUnlocked(providerName)
		if total >= h.cbConfig.MinRequests && errorRate >= h.cbConfig.ErrorThreshold {
			circuit.State = CircuitOpen
			circuit.OpenedAt = time.Now()
		}
	case CircuitOpen:
		// Check if cooldown elapsed -> half-open
		if time.Since(circuit.OpenedAt) >= h.cbConfig.CooldownDuration {
			circuit.State = CircuitHalfOpen
			circuit.LastProbe = time.Now()
			// Evaluate the probe result immediately
			if lastErr == nil {
				circuit.State = CircuitClosed
			} else {
				circuit.State = CircuitOpen
				circuit.OpenedAt = time.Now()
			}
		}
	case CircuitHalfOpen:
		if lastErr == nil {
			circuit.State = CircuitClosed
		} else {
			circuit.State = CircuitOpen
			circuit.OpenedAt = time.Now()
		}
	}

	if circuit.State != prevState && h.OnStateChange != nil {
		cb := h.OnStateChange
		from, to := prevState, circuit.State
		// Call outside lock to avoid deadlocks
		go cb(providerName, from, to)
	}
}

// errorRateUnlocked computes error rate within window. Must be called with lock held.
func (h *HealthTracker) errorRateUnlocked(provider string) (float64, int) {
	cutoff := time.Now().Add(-h.windowDu)
	events := h.windows[provider]
	var total, errors int
	for _, e := range events {
		if e.Timestamp.Before(cutoff) {
			continue
		}
		total++
		if e.IsError {
			errors++
		}
	}
	if total == 0 {
		return 0, 0
	}
	return float64(errors) / float64(total), total
}

// Status returns computed health for all tracked providers.
func (h *HealthTracker) Status() []ProviderHealth {
	h.mu.RLock()
	defer h.mu.RUnlock()

	cutoff := time.Now().Add(-h.windowDu)
	var results []ProviderHealth

	for provider, events := range h.windows {
		var total, errors int
		var totalLatency int64

		for _, e := range events {
			if e.Timestamp.Before(cutoff) {
				continue
			}
			total++
			totalLatency += e.LatencyMS
			if e.IsError {
				errors++
			}
		}

		if total == 0 {
			continue
		}

		errorRate := float64(errors) / float64(total)
		status := "healthy"
		if errorRate >= 0.5 {
			status = "down"
		} else if errorRate >= 0.1 {
			status = "degraded"
		}

		circuitState := "closed"
		if circuit, ok := h.circuits[provider]; ok {
			circuitState = circuit.State.String()
		}

		results = append(results, ProviderHealth{
			Provider:     provider,
			Status:       status,
			ErrorRate:    errorRate,
			AvgLatency:   float64(totalLatency) / float64(total),
			Total:        total,
			Errors:       errors,
			CircuitState: circuitState,
		})
	}

	return results
}

// prune removes events outside the window. Must be called with lock held.
func (h *HealthTracker) prune(provider string) {
	cutoff := time.Now().Add(-h.windowDu)
	events := h.windows[provider]
	i := 0
	for i < len(events) && events[i].Timestamp.Before(cutoff) {
		i++
	}
	if i > 0 {
		h.windows[provider] = events[i:]
	}
}
