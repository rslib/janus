package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"janus/internal/provider"
	"janus/internal/storage"
)

func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, routes []provider.Route, tokenName, requestID string, modelTimeouts *provider.ModelTimeouts) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	var lastErr error

	for i, route := range routes {
		// Retry backoff between attempts
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
		body, err := route.Provider.ChatCompletionStream(r.Context(), route.ProviderModel, req)

		if err != nil {
			var pe *provider.ProviderError
			if errors.As(err, &pe) && !pe.IsRetryable() {
				latency := time.Since(start).Milliseconds()
				h.metrics.RecordRequest(req.Model, route.Provider.Name(), tokenName, "error", latency, 0, 0, 0)
				h.logRequest(requestID, tokenName, req.Model, route.Provider.Name(), route.ProviderModel, 0, 0, 0, latency, "error", err.Error(), true, false)
				if h.healthTracker != nil {
					h.healthTracker.Record(route.Provider.Name(), latency, err)
				}
				w.Header().Set("X-Request-ID", requestID)
				writeErrorRaw(w, pe.StatusCode, pe.Body)
				return
			}
			lastErr = err
			latency := time.Since(start).Milliseconds()
			log.Printf("Provider %s stream failed for %s: %v", route.Provider.Name(), req.Model, err)
			h.logRequest(requestID, tokenName, req.Model, route.Provider.Name(), route.ProviderModel, 0, 0, 0, latency, "error", err.Error(), true, false)
			if h.healthTracker != nil {
				h.healthTracker.Record(route.Provider.Name(), latency, err)
			}
			continue
		}

		// Apply streaming timeout (per-model override takes precedence)
		streamingTimeout := h.cfg.Server.StreamingTimeout
		if modelTimeouts != nil && modelTimeouts.StreamingTimeout > 0 {
			streamingTimeout = modelTimeouts.StreamingTimeout
		}
		var streamCtx context.Context
		var streamCancel context.CancelFunc
		if streamingTimeout > 0 {
			streamCtx, streamCancel = context.WithTimeout(r.Context(), streamingTimeout)
		} else {
			streamCtx, streamCancel = context.WithCancel(r.Context())
		}

		// Stream the response
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("X-Request-ID", requestID)
		w.WriteHeader(http.StatusOK)

		inputTokens, outputTokens := 0, 0
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 64*1024), 256*1024)

		// Capture streamed lines for debug logging
		debugEnabled := h.debugLogger != nil && h.debugLogger.IsEnabled()
		var debugLines []string

		scanDone := make(chan struct{})
		go func() {
			defer close(scanDone)
			for scanner.Scan() {
				select {
				case <-streamCtx.Done():
					return
				default:
				}

				line := scanner.Text()

				if strings.HasPrefix(line, "data: ") {
					data := strings.TrimPrefix(line, "data: ")
					if data != "[DONE]" {
						var chunk streamChunk
						if json.Unmarshal([]byte(data), &chunk) == nil {
							if chunk.Usage != nil {
								inputTokens = chunk.Usage.PromptTokens
								outputTokens = chunk.Usage.CompletionTokens
							}
							if chunk.Model != "" {
								chunk.Model = req.Model
								if rewritten, err := json.Marshal(chunk); err == nil {
									line = "data: " + string(rewritten)
								}
							}
						}
					}
				}

				if debugEnabled {
					debugLines = append(debugLines, line)
				}

				w.Write([]byte(line + "\n"))
				flusher.Flush()
			}
		}()

		select {
		case <-scanDone:
			// Normal completion
		case <-streamCtx.Done():
			log.Printf("Stream timeout for %s via %s", req.Model, route.Provider.Name())
		}

		body.Close()
		streamCancel()

		latency := time.Since(start).Milliseconds()
		cost := computeCost(inputTokens, outputTokens, route.InputPrice, route.OutputPrice)
		h.metrics.RecordRequest(req.Model, route.Provider.Name(), tokenName, "success", latency, inputTokens, outputTokens, cost)
		h.logRequest(requestID, tokenName, req.Model, route.Provider.Name(), route.ProviderModel, inputTokens, outputTokens, cost, latency, "success", "", true, false)
		if h.healthTracker != nil {
			h.healthTracker.Record(route.Provider.Name(), latency, nil)
		}

		// Debug logging for streaming requests
		if debugEnabled && len(debugLines) > 0 {
			respBody := strings.Join(debugLines, "\n")
			reqBody, _ := json.Marshal(req)
			reqBodyStr := string(reqBody)
			if h.cfg.Debug.MaxBodyBytes > 0 {
				if len(reqBodyStr) > h.cfg.Debug.MaxBodyBytes {
					reqBodyStr = reqBodyStr[:h.cfg.Debug.MaxBodyBytes]
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
				RequestBody:    reqBodyStr,
				ResponseBody:   respBody,
				RequestHeaders: formatHeaders(r.Header),
				ResponseStatus: http.StatusOK,
			})
		}

		return
	}

	// All providers failed
	w.Header().Set("X-Request-ID", requestID)
	if lastErr != nil {
		writeError(w, http.StatusBadGateway, "all providers failed: "+lastErr.Error())
	} else {
		writeError(w, http.StatusBadGateway, "all providers failed")
	}
}

type streamChunk struct {
	ID      string          `json:"id,omitempty"`
	Object  string          `json:"object,omitempty"`
	Created int64           `json:"created,omitempty"`
	Model   string          `json:"model,omitempty"`
	Choices []any           `json:"choices,omitempty"`
	Usage   *provider.Usage `json:"usage,omitempty"`
}
