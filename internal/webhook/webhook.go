package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"janus/internal/config"
)

// Event types.
const (
	EventCircuitBreakerOpen   = "circuit_breaker.open"
	EventCircuitBreakerClosed = "circuit_breaker.closed"
	EventBudgetThreshold      = "budget.threshold"
)

// Event represents a webhook notification payload.
type Event struct {
	Type      string         `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data"`
}

// Notifier sends webhook notifications.
type Notifier struct {
	webhooks []config.WebhookConfig
	ch       chan Event
	done     chan struct{}
	client   *http.Client
}

// NewNotifier creates a webhook notifier from config.
func NewNotifier(webhooks []config.WebhookConfig) *Notifier {
	n := &Notifier{
		webhooks: webhooks,
		ch:       make(chan Event, 100),
		done:     make(chan struct{}),
		client:   &http.Client{Timeout: 10 * time.Second},
	}
	go n.run()
	return n
}

// Notify queues an event for delivery (non-blocking).
func (n *Notifier) Notify(evt Event) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	select {
	case n.ch <- evt:
	default:
		log.Printf("WARNING: webhook channel full, dropping event %s", evt.Type)
	}
}

// Close drains pending events and shuts down.
func (n *Notifier) Close() {
	close(n.ch)
	<-n.done
}

func (n *Notifier) run() {
	defer close(n.done)
	for evt := range n.ch {
		for _, wh := range n.webhooks {
			if !n.shouldSend(wh, evt.Type) {
				continue
			}
			n.send(wh, evt)
		}
	}
}

func (n *Notifier) shouldSend(wh config.WebhookConfig, eventType string) bool {
	if len(wh.Events) == 0 {
		return true // no filter = send all
	}
	for _, e := range wh.Events {
		if e == eventType {
			return true
		}
	}
	return false
}

func (n *Notifier) send(wh config.WebhookConfig, evt Event) {
	body, err := json.Marshal(evt)
	if err != nil {
		log.Printf("ERROR: webhook marshal: %v", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		log.Printf("ERROR: webhook request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	if wh.Secret != "" {
		mac := hmac.New(sha256.New, []byte(wh.Secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Webhook-Signature", "sha256="+sig)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		log.Printf("WARNING: webhook delivery to %s failed: %v", wh.URL, err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("WARNING: webhook %s returned %d", wh.URL, resp.StatusCode)
	}
}
