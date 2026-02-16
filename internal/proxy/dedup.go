package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// inflight represents an in-progress deduplicated request.
type inflight struct {
	done       chan struct{}
	result     []byte
	statusCode int
	createdAt  time.Time
}

// Deduplicator coalesces identical concurrent non-streaming requests.
type Deduplicator struct {
	mu      sync.Mutex
	flights map[string]*inflight
	window  time.Duration
	done    chan struct{}
}

// NewDeduplicator creates a new request deduplicator.
func NewDeduplicator(window time.Duration) *Deduplicator {
	if window == 0 {
		window = 30 * time.Second
	}
	d := &Deduplicator{
		flights: make(map[string]*inflight),
		window:  window,
		done:    make(chan struct{}),
	}
	go d.cleanup()
	return d
}

// DedupKey computes a dedup key from model name and request body.
func DedupKey(model string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// TryJoin attempts to join an in-flight request. Returns the inflight entry and
// whether this caller is the leader (true) or a follower (false).
func (d *Deduplicator) TryJoin(key string) (*inflight, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if f, ok := d.flights[key]; ok {
		return f, false // follower
	}

	f := &inflight{
		done:      make(chan struct{}),
		createdAt: time.Now(),
	}
	d.flights[key] = f
	return f, true // leader
}

// Complete signals completion of a deduplicated request.
func (d *Deduplicator) Complete(key string, result []byte, statusCode int) {
	d.mu.Lock()
	f, ok := d.flights[key]
	delete(d.flights, key)
	d.mu.Unlock()

	if ok {
		f.result = result
		f.statusCode = statusCode
		close(f.done)
	}
}

// Close stops the background cleanup goroutine.
func (d *Deduplicator) Close() {
	close(d.done)
}

// cleanup periodically removes stale in-flight entries.
func (d *Deduplicator) cleanup() {
	ticker := time.NewTicker(d.window)
	defer ticker.Stop()

	for {
		select {
		case <-d.done:
			return
		case <-ticker.C:
			d.mu.Lock()
			now := time.Now()
			for key, f := range d.flights {
				if now.Sub(f.createdAt) > d.window*2 {
					delete(d.flights, key)
					close(f.done) // unblock any waiting followers
				}
			}
			d.mu.Unlock()
		}
	}
}
