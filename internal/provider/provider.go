package provider

import (
	"context"
	"io"
)

// ChatRequest is the OpenAI-compatible chat completion request.
type ChatRequest struct {
	Model            string         `json:"model"`
	Messages         []Message      `json:"messages"`
	Temperature      *float64       `json:"temperature,omitempty"`
	MaxTokens        *int           `json:"max_tokens,omitempty"`
	TopP             *float64       `json:"top_p,omitempty"`
	Stream           bool           `json:"stream"`
	Stop             any            `json:"stop,omitempty"`
	FrequencyPenalty *float64       `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64       `json:"presence_penalty,omitempty"`
	N                *int           `json:"n,omitempty"`
	Tools            []any          `json:"tools,omitempty"`
	ToolChoice       any            `json:"tool_choice,omitempty"`
	ResponseFormat   any            `json:"response_format,omitempty"`
	Extra            map[string]any `json:"-"` // pass through unknown fields
}

type Message struct {
	Role       string `json:"role"`
	Content    any    `json:"content"` // string or []ContentPart
	Name       string `json:"name,omitempty"`
	ToolCalls  []any  `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// EmbeddingRequest is the OpenAI-compatible embedding request.
type EmbeddingRequest struct {
	Model          string `json:"model"`
	Input          any    `json:"input"` // string or []string
	EncodingFormat string `json:"encoding_format,omitempty"`
}

// EmbeddingResponse is the OpenAI-compatible embedding response.
type EmbeddingResponse struct {
	Object string          `json:"object"`
	Data   []EmbeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  *EmbeddingUsage `json:"usage,omitempty"`
}

// EmbeddingData holds a single embedding vector.
type EmbeddingData struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

// EmbeddingUsage reports token usage for embeddings.
type EmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Provider sends requests to an LLM API.
type Provider interface {
	Name() string
	ChatCompletion(ctx context.Context, model string, req *ChatRequest) (*ChatResponse, error)
	ChatCompletionStream(ctx context.Context, model string, req *ChatRequest) (io.ReadCloser, error)
	Embedding(ctx context.Context, model string, req *EmbeddingRequest) (*EmbeddingResponse, error)
}
