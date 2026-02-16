package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAIProvider is a generic OpenAI-compatible HTTP client.
type OpenAIProvider struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewOpenAIProvider(name, baseURL, apiKey string, timeout time.Duration) *OpenAIProvider {
	return &OpenAIProvider{
		name:    name,
		baseURL: baseURL,
		apiKey:  apiKey,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (p *OpenAIProvider) Name() string { return p.name }

func (p *OpenAIProvider) ChatCompletion(ctx context.Context, model string, req *ChatRequest) (*ChatResponse, error) {
	reqCopy := *req
	reqCopy.Model = model
	reqCopy.Stream = false

	body, err := json.Marshal(reqCopy)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
			Provider:   p.name,
		}
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	return &chatResp, nil
}

func (p *OpenAIProvider) ChatCompletionStream(ctx context.Context, model string, req *ChatRequest) (io.ReadCloser, error) {
	reqCopy := *req
	reqCopy.Model = model
	reqCopy.Stream = true

	body, err := json.Marshal(reqCopy)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &ProviderError{
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
			Provider:   p.name,
		}
	}

	return resp.Body, nil
}

func (p *OpenAIProvider) Embedding(ctx context.Context, model string, req *EmbeddingRequest) (*EmbeddingResponse, error) {
	reqCopy := *req
	reqCopy.Model = model

	body, err := json.Marshal(reqCopy)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &ProviderError{
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
			Provider:   p.name,
		}
	}

	var embResp EmbeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	return &embResp, nil
}

func (p *OpenAIProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	// Forward request ID if present in context
	if reqID := req.Context().Value("requestID"); reqID != nil {
		if id, ok := reqID.(string); ok && id != "" {
			req.Header.Set("X-Request-ID", id)
		}
	}
}

// ProviderError represents a non-200 response from a provider.
type ProviderError struct {
	StatusCode int
	Body       string
	Provider   string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider %s returned %d: %s", e.Provider, e.StatusCode, e.Body)
}

// IsRetryable returns true if the error is a server-side error worth retrying with another provider.
func (e *ProviderError) IsRetryable() bool {
	return e.StatusCode >= 500 || e.StatusCode == 429
}
