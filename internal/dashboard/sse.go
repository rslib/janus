package dashboard

import (
	"fmt"
	"net/http"
	"sync"
)

// SSEBroker manages Server-Sent Events connections.
type SSEBroker struct {
	mu      sync.RWMutex
	clients map[chan struct{}]struct{}
}

// NewSSEBroker creates a new SSE broker.
func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		clients: make(map[chan struct{}]struct{}),
	}
}

// Notify sends a refresh signal to all connected SSE clients.
func (b *SSEBroker) Notify() {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.clients {
		select {
		case ch <- struct{}{}:
		default:
			// Client not ready, skip
		}
	}
}

// ServeHTTP handles SSE connections.
func (b *SSEBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.clients, ch)
		b.mu.Unlock()
	}()

	// Send initial connection event
	fmt.Fprintf(w, "event: connected\ndata: ok\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			fmt.Fprintf(w, "event: refresh\ndata: updated\n\n")
			flusher.Flush()
		}
	}
}
