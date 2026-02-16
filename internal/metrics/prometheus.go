package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	tokensTotal     *prometheus.CounterVec
	costTotal       *prometheus.CounterVec
	cacheHits       prometheus.Counter
	cacheMisses     prometheus.Counter
}

func New() *Metrics {
	return &Metrics{
		requestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "janus_requests_total",
			Help: "Total number of LLM requests",
		}, []string{"model", "provider", "token_name", "status"}),

		requestDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "janus_request_duration_ms",
			Help:    "Request duration in milliseconds",
			Buckets: []float64{100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000},
		}, []string{"model", "provider"}),

		tokensTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "janus_tokens_total",
			Help: "Total tokens processed",
		}, []string{"model", "provider", "type"}),

		costTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "janus_cost_usd_total",
			Help: "Total cost in USD",
		}, []string{"model", "provider", "token_name"}),

		cacheHits: promauto.NewCounter(prometheus.CounterOpts{
			Name: "janus_cache_hits_total",
			Help: "Total number of cache hits",
		}),

		cacheMisses: promauto.NewCounter(prometheus.CounterOpts{
			Name: "janus_cache_misses_total",
			Help: "Total number of cache misses",
		}),
	}
}

func (m *Metrics) RecordRequest(model, providerName, tokenName, status string, latencyMS int64, inputTokens, outputTokens int, cost float64) {
	m.requestsTotal.WithLabelValues(model, providerName, tokenName, status).Inc()
	m.requestDuration.WithLabelValues(model, providerName).Observe(float64(latencyMS))

	if inputTokens > 0 {
		m.tokensTotal.WithLabelValues(model, providerName, "input").Add(float64(inputTokens))
	}
	if outputTokens > 0 {
		m.tokensTotal.WithLabelValues(model, providerName, "output").Add(float64(outputTokens))
	}
	if cost > 0 {
		m.costTotal.WithLabelValues(model, providerName, tokenName).Add(cost)
	}
}

func (m *Metrics) RecordCacheHit() {
	m.cacheHits.Inc()
}

func (m *Metrics) RecordCacheMiss() {
	m.cacheMisses.Inc()
}
