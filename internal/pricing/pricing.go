package pricing

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

const defaultPricesURL = "https://raw.githubusercontent.com/pydantic/genai-prices/main/prices/data_slim.json"

// Provider represents a provider entry in genai_prices.json.
type Provider struct {
	ID     string  `json:"id"`
	Models []Model `json:"models"`
}

// Model represents a model entry with pricing.
type Model struct {
	ID     string          `json:"id"`
	Prices json.RawMessage `json:"prices"`
}

// Lookup provides pricing data fetched from genai-prices.
type Lookup struct {
	mu     sync.RWMutex
	prices map[string][2]float64
	url    string
	stopCh chan struct{}
}

// NewLookup creates a Lookup that fetches pricing data immediately and refreshes every interval.
// If url is empty, uses the default genai-prices URL.
// Returns a usable Lookup even if the initial fetch fails (prices will be empty until next refresh).
func NewLookup(url string, interval time.Duration) *Lookup {
	if url == "" {
		url = defaultPricesURL
	}
	l := &Lookup{
		prices: make(map[string][2]float64),
		url:    url,
		stopCh: make(chan struct{}),
	}

	// Initial fetch
	l.refresh()

	// Background refresh
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				l.refresh()
			case <-l.stopCh:
				return
			}
		}
	}()

	return l
}

// Close stops the background refresh goroutine.
func (l *Lookup) Close() {
	close(l.stopCh)
}

// Get returns (inputPer1M, outputPer1M) for a provider:model pair.
// Returns (0, 0) if not found.
func (l *Lookup) Get(provider, model string) (float64, float64) {
	if l == nil {
		return 0, 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	key := fmt.Sprintf("%s:%s", provider, model)
	if p, ok := l.prices[key]; ok {
		return p[0], p[1]
	}
	return 0, 0
}

// FillMissing fills in zero-value pricing from the lookup data.
// Returns the number of prices filled.
func (l *Lookup) FillMissing(provider, model string, input, output *float64) bool {
	if l == nil || (*input > 0 && *output > 0) {
		return false
	}
	i, o := l.Get(provider, model)
	if i == 0 && o == 0 {
		return false
	}
	if *input == 0 {
		*input = i
	}
	if *output == 0 {
		*output = o
	}
	return true
}

func (l *Lookup) refresh() {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(l.url)
	if err != nil {
		log.Printf("WARNING: failed to fetch pricing data: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("WARNING: pricing data fetch returned %d", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("WARNING: failed to read pricing data: %v", err)
		return
	}

	var providers []Provider
	if err := json.Unmarshal(body, &providers); err != nil {
		log.Printf("WARNING: failed to parse pricing data: %v", err)
		return
	}

	prices := make(map[string][2]float64)
	for _, p := range providers {
		for _, m := range p.Models {
			input, output := parsePrices(m.Prices)
			if input > 0 || output > 0 {
				key := fmt.Sprintf("%s:%s", p.ID, m.ID)
				prices[key] = [2]float64{input, output}
			}
		}
	}

	l.mu.Lock()
	l.prices = prices
	l.mu.Unlock()

	log.Printf("Loaded pricing data: %d model prices from genai-prices", len(prices))
}

// parsePrices handles the different shapes of the "prices" field:
//   - object: {"input_mtok": 0.5, "output_mtok": 1.0}
//   - array:  [{"prices": {"input_mtok": 0.5, ...}}, ...] (time-of-day; use first entry)
func parsePrices(raw json.RawMessage) (input, output float64) {
	if len(raw) == 0 {
		return 0, 0
	}

	// Try as object first (most common)
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		return extractPrice(obj, "input_mtok"), extractPrice(obj, "output_mtok")
	}

	// Try as array (time-of-day pricing) — use first entry
	var arr []struct {
		Prices map[string]any `json:"prices"`
	}
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		return extractPrice(arr[0].Prices, "input_mtok"), extractPrice(arr[0].Prices, "output_mtok")
	}

	return 0, 0
}

// extractPrice handles both simple float and tiered pricing (uses base price).
func extractPrice(prices map[string]any, key string) float64 {
	v, ok := prices[key]
	if !ok {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case map[string]any:
		if base, ok := val["base"].(float64); ok {
			return base
		}
	}
	return 0
}
