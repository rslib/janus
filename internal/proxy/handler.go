package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"janus/internal/auth"
	"janus/internal/cache"
	"janus/internal/config"
	"janus/internal/metrics"
	"janus/internal/provider"
	"janus/internal/storage"
)

type contextKey string

const tokenNameKey contextKey = "token_name"
const apiTokenKey contextKey = "api_token"

func withTokenName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, tokenNameKey, name)
}

func getTokenName(ctx context.Context) string {
	name, _ := ctx.Value(tokenNameKey).(string)
	return name
}

func withAPIToken(ctx context.Context, token *auth.APIToken) context.Context {
	return context.WithValue(ctx, apiTokenKey, token)
}

func getAPIToken(ctx context.Context) *auth.APIToken {
	t, _ := ctx.Value(apiTokenKey).(*auth.APIToken)
	return t
}

type Handler struct {
	registry      *provider.Registry
	logger        *storage.AsyncLogger
	cache         *cache.Cache
	metrics       *metrics.Metrics
	cfg           *config.Config
	healthTracker *provider.HealthTracker
	debugLogger   *storage.DebugLogger
	dedup         *Deduplicator
}

func NewHandler(registry *provider.Registry, logger *storage.AsyncLogger, c *cache.Cache, m *metrics.Metrics, cfg *config.Config, ht *provider.HealthTracker) *Handler {
	return &Handler{
		registry:      registry,
		logger:        logger,
		cache:         c,
		metrics:       m,
		cfg:           cfg,
		healthTracker: ht,
	}
}

func (h *Handler) SetDebugLogger(dl *storage.DebugLogger) {
	h.debugLogger = dl
}

func (h *Handler) SetDeduplicator(d *Deduplicator) {
	h.dedup = d
}

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(h.cfg.Server.MaxRequestBodyMB)<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req provider.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}

	routes, ok := h.registry.Lookup(req.Model)
	if !ok {
		writeError(w, http.StatusNotFound, "model not found: "+req.Model)
		return
	}

	// Filter healthy routes (circuit breaker)
	routes = h.filterHealthyRoutes(routes)

	tokenName := getTokenName(r.Context())
	requestID := middleware.GetReqID(r.Context())

	// Check cache for non-streaming requests
	if !req.Stream && h.cache != nil {
		if cached, err := h.cache.Get(r.Context(), req.Model, body); err == nil && cached != nil {
			h.logRequest(requestID, tokenName, req.Model, "cache", "", 0, 0, 0, 0, "cached", "", false, true)
			if h.metrics != nil {
				h.metrics.RecordCacheHit()
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			w.Header().Set("X-Request-ID", requestID)
			w.Write(cached)
			return
		}
		if h.metrics != nil {
			h.metrics.RecordCacheMiss()
		}
	}

	// Apply per-model timeout for non-streaming requests
	modelTimeouts := h.registry.ModelTimeoutsFor(req.Model)

	if req.Stream {
		h.handleStream(w, r, &req, routes, tokenName, requestID, modelTimeouts)
		return
	}

	// Request deduplication for non-streaming requests
	if h.dedup != nil {
		dedupKey := DedupKey(req.Model, body)
		flight, isLeader := h.dedup.TryJoin(dedupKey)
		if !isLeader {
			// Wait for the leader to complete
			select {
			case <-flight.done:
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", requestID)
				w.Header().Set("X-Dedup", "HIT")
				w.WriteHeader(flight.statusCode)
				w.Write(flight.result)
				return
			case <-r.Context().Done():
				writeError(w, http.StatusGatewayTimeout, "request cancelled while waiting for dedup")
				return
			}
		}
		// Leader: proceed normally, but capture response for followers
		defer func() {
			// If we haven't completed yet (e.g., panic), clean up
		}()
		h.handleNonStreamDedup(w, r, &req, routes, tokenName, body, requestID, modelTimeouts, dedupKey)
		return
	}

	if modelTimeouts != nil && modelTimeouts.RequestTimeout > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), modelTimeouts.RequestTimeout)
		defer cancel()
		r = r.WithContext(ctx)
	}

	h.handleNonStream(w, r, &req, routes, tokenName, body, requestID)
}

func (h *Handler) handleNonStream(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, routes []provider.Route, tokenName string, rawBody []byte, requestID string) {
	var lastErr error

	for i, route := range routes {
		// Retry backoff between attempts (not before first attempt)
		if i > 0 {
			backoff := backoffDuration(i, h.cfg.Retry)
			select {
			case <-time.After(backoff):
			case <-r.Context().Done():
				writeError(w, http.StatusGatewayTimeout, "request cancelled")
				return
			}
		}

		start := time.Now()
		resp, err := route.Provider.ChatCompletion(r.Context(), route.ProviderModel, req)
		latency := time.Since(start).Milliseconds()

		if err != nil {
			var pe *provider.ProviderError
			if errors.As(err, &pe) && !pe.IsRetryable() {
				h.metrics.RecordRequest(req.Model, route.Provider.Name(), tokenName, "error", latency, 0, 0, 0)
				h.logRequest(requestID, tokenName, req.Model, route.Provider.Name(), route.ProviderModel, 0, 0, 0, latency, "error", err.Error(), false, false)
				if h.healthTracker != nil {
					h.healthTracker.Record(route.Provider.Name(), latency, err)
				}
				w.Header().Set("X-Request-ID", requestID)
				writeErrorRaw(w, pe.StatusCode, pe.Body)
				return
			}
			lastErr = err
			log.Printf("Provider %s failed for %s: %v", route.Provider.Name(), req.Model, err)
			h.metrics.RecordRequest(req.Model, route.Provider.Name(), tokenName, "error", latency, 0, 0, 0)
			h.logRequest(requestID, tokenName, req.Model, route.Provider.Name(), route.ProviderModel, 0, 0, 0, latency, "error", err.Error(), false, false)
			if h.healthTracker != nil {
				h.healthTracker.Record(route.Provider.Name(), latency, err)
			}
			continue
		}

		if h.healthTracker != nil {
			h.healthTracker.Record(route.Provider.Name(), latency, nil)
		}

		inputTokens, outputTokens := 0, 0
		if resp.Usage != nil {
			inputTokens = resp.Usage.PromptTokens
			outputTokens = resp.Usage.CompletionTokens
		}
		cost := computeCost(inputTokens, outputTokens, route.InputPrice, route.OutputPrice)

		h.metrics.RecordRequest(req.Model, route.Provider.Name(), tokenName, "success", latency, inputTokens, outputTokens, cost)
		h.logRequest(requestID, tokenName, req.Model, route.Provider.Name(), route.ProviderModel, inputTokens, outputTokens, cost, latency, "success", "", false, false)

		resp.Model = req.Model

		respBytes, err := json.Marshal(resp)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to marshal response")
			return
		}

		if h.cache != nil {
			h.cache.Set(r.Context(), req.Model, rawBody, respBytes)
		}

		// Debug logging
		if h.debugLogger != nil && h.debugLogger.IsEnabled() {
			reqBody := string(rawBody)
			respBody := string(respBytes)
			if h.cfg.Debug.MaxBodyBytes > 0 {
				if len(reqBody) > h.cfg.Debug.MaxBodyBytes {
					reqBody = reqBody[:h.cfg.Debug.MaxBodyBytes]
				}
				if len(respBody) > h.cfg.Debug.MaxBodyBytes {
					respBody = respBody[:h.cfg.Debug.MaxBodyBytes]
				}
			}
			h.debugLogger.Log(storage.DebugLogEntry{
				RequestID:      requestID,
				TokenName:      tokenName,
				Model:          req.Model,
				Provider:       route.Provider.Name(),
				RequestBody:    reqBody,
				ResponseBody:   respBody,
				RequestHeaders: formatHeaders(r.Header),
				ResponseStatus: http.StatusOK,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "MISS")
		w.Header().Set("X-Request-ID", requestID)
		w.Write(respBytes)
		return
	}

	if lastErr != nil {
		w.Header().Set("X-Request-ID", requestID)
		writeError(w, http.StatusBadGateway, "all providers failed: "+lastErr.Error())
	} else {
		w.Header().Set("X-Request-ID", requestID)
		writeError(w, http.StatusBadGateway, "all providers failed")
	}
}

// handleNonStreamDedup wraps handleNonStream to capture the response for dedup followers.
func (h *Handler) Embeddings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(h.cfg.Server.MaxRequestBodyMB)<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req provider.EmbeddingRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}

	routes, ok := h.registry.Lookup(req.Model)
	if !ok {
		writeError(w, http.StatusNotFound, "model not found: "+req.Model)
		return
	}

	routes = h.filterHealthyRoutes(routes)
	tokenName := getTokenName(r.Context())
	requestID := middleware.GetReqID(r.Context())

	var lastErr error
	for i, route := range routes {
		if i > 0 {
			backoff := backoffDuration(i, h.cfg.Retry)
			select {
			case <-time.After(backoff):
			case <-r.Context().Done():
				writeError(w, http.StatusGatewayTimeout, "request cancelled")
				return
			}
		}

		start := time.Now()
		resp, err := route.Provider.Embedding(r.Context(), route.ProviderModel, &req)
		latency := time.Since(start).Milliseconds()

		if err != nil {
			var pe *provider.ProviderError
			if errors.As(err, &pe) && !pe.IsRetryable() {
				h.metrics.RecordRequest(req.Model, route.Provider.Name(), tokenName, "error", latency, 0, 0, 0)
				h.logEmbeddingRequest(requestID, tokenName, req.Model, route.Provider.Name(), route.ProviderModel, 0, 0, latency, "error", err.Error())
				if h.healthTracker != nil {
					h.healthTracker.Record(route.Provider.Name(), latency, err)
				}
				w.Header().Set("X-Request-ID", requestID)
				writeErrorRaw(w, pe.StatusCode, pe.Body)
				return
			}
			lastErr = err
			log.Printf("Provider %s embedding failed for %s: %v", route.Provider.Name(), req.Model, err)
			h.logEmbeddingRequest(requestID, tokenName, req.Model, route.Provider.Name(), route.ProviderModel, 0, 0, latency, "error", err.Error())
			if h.healthTracker != nil {
				h.healthTracker.Record(route.Provider.Name(), latency, err)
			}
			continue
		}

		if h.healthTracker != nil {
			h.healthTracker.Record(route.Provider.Name(), latency, nil)
		}

		promptTokens := 0
		if resp.Usage != nil {
			promptTokens = resp.Usage.PromptTokens
		}
		cost := float64(promptTokens) / 1_000_000.0 * route.InputPrice

		h.metrics.RecordRequest(req.Model, route.Provider.Name(), tokenName, "success", latency, promptTokens, 0, cost)
		h.logEmbeddingRequest(requestID, tokenName, req.Model, route.Provider.Name(), route.ProviderModel, promptTokens, cost, latency, "success", "")

		resp.Model = req.Model

		respBytes, err := json.Marshal(resp)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to marshal response")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", requestID)
		w.Write(respBytes)
		return
	}

	w.Header().Set("X-Request-ID", requestID)
	if lastErr != nil {
		writeError(w, http.StatusBadGateway, "all providers failed: "+lastErr.Error())
	} else {
		writeError(w, http.StatusBadGateway, "all providers failed")
	}
}

func (h *Handler) logEmbeddingRequest(requestID, tokenName, model, providerName, providerModel string, inputTokens int, cost float64, latencyMS int64, status, errMsg string) {
	h.logger.Log(storage.RequestLog{
		RequestID:     requestID,
		Timestamp:     time.Now().Unix(),
		TokenName:     tokenName,
		Model:         model,
		Provider:      providerName,
		ProviderModel: providerModel,
		InputTokens:   inputTokens,
		CostUSD:       cost,
		LatencyMS:     latencyMS,
		Status:        status,
		ErrorMessage:  errMsg,
		RequestType:   "embedding",
	})
}

func (h *Handler) handleNonStreamDedup(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, routes []provider.Route, tokenName string, rawBody []byte, requestID string, modelTimeouts *provider.ModelTimeouts, dedupKey string) {
	if modelTimeouts != nil && modelTimeouts.RequestTimeout > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), modelTimeouts.RequestTimeout)
		defer cancel()
		r = r.WithContext(ctx)
	}

	rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
	h.handleNonStream(rec, r, req, routes, tokenName, rawBody, requestID)
	h.dedup.Complete(dedupKey, rec.body, rec.statusCode)
}

// responseRecorder captures the response for dedup.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       []byte
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body = append(r.body, b...)
	return r.ResponseWriter.Write(b)
}

// filterHealthyRoutes removes providers with open circuit breakers.
// If all are filtered out, returns original routes as fallback.
func (h *Handler) filterHealthyRoutes(routes []provider.Route) []provider.Route {
	if h.healthTracker == nil {
		return routes
	}
	var healthy []provider.Route
	for _, r := range routes {
		if h.healthTracker.IsAvailable(r.Provider.Name()) {
			healthy = append(healthy, r)
		}
	}
	if len(healthy) == 0 {
		return routes // all-down fallback
	}
	return healthy
}

// backoffDuration computes exponential backoff for the given attempt.
func backoffDuration(attempt int, cfg config.RetryConfig) time.Duration {
	d := cfg.InitialBackoff
	for i := 1; i < attempt; i++ {
		d = time.Duration(float64(d) * cfg.Multiplier)
		if d > cfg.MaxBackoff {
			d = cfg.MaxBackoff
			break
		}
	}
	return d
}

func (h *Handler) logRequest(requestID, tokenName, model, providerName, providerModel string, inputTokens, outputTokens int, cost float64, latencyMS int64, status, errMsg string, streaming, cached bool) {
	h.logger.Log(storage.RequestLog{
		RequestID:     requestID,
		Timestamp:     time.Now().Unix(),
		TokenName:     tokenName,
		Model:         model,
		Provider:      providerName,
		ProviderModel: providerModel,
		InputTokens:   inputTokens,
		OutputTokens:  outputTokens,
		CostUSD:       cost,
		LatencyMS:     latencyMS,
		Status:        status,
		ErrorMessage:  errMsg,
		Streaming:     streaming,
		Cached:        cached,
	})
}

func computeCost(inputTokens, outputTokens int, inputPrice, outputPrice float64) float64 {
	return (float64(inputTokens) / 1_000_000.0 * inputPrice) + (float64(outputTokens) / 1_000_000.0 * outputPrice)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "error",
			"code":    code,
		},
	})
}

func writeErrorRaw(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write([]byte(body))
}

// formatHeaders serializes HTTP headers to a readable string, sorted by key.
// Sensitive headers (Authorization) are redacted.
func formatHeaders(h http.Header) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		val := strings.Join(h[k], ", ")
		if strings.EqualFold(k, "Authorization") {
			val = "[REDACTED]"
		}
		fmt.Fprintf(&b, "%s: %s\n", k, val)
	}
	return b.String()
}
