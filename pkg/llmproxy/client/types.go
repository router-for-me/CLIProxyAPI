// Package client provides a Go SDK for the cliproxyapi++ HTTP proxy API.
//
// It covers the core LLM proxy surface: model listing, chat completions, the
// Responses API, and the proxy process lifecycle (start/stop/health).
//
// # Migration note
//
// This package is the canonical Go replacement for the Python adapter code
// that previously lived in thegent/src/thegent/cliproxy_adapter.py and
// related helpers.  Any new consumer should import this package rather than
// re-implementing raw HTTP calls.
package client

import "time"

// ---------------------------------------------------------------------------
// Model types
// ---------------------------------------------------------------------------

// Model is a single entry from GET /v1/models.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	Created int64  `json:"created,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
}

// ModelsResponse is the envelope returned by GET /v1/models.
// cliproxyapi++ normalises the upstream shape into {"models": [...]}.
type ModelsResponse struct {
	Models []Model `json:"models"`
	// ETag is populated from the x-models-etag response header when present.
	ETag string `json:"-"`
}

// ---------------------------------------------------------------------------
// Chat completions types
// ---------------------------------------------------------------------------

// ChatMessage is a single message in a chat conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionRequest is the body for POST /v1/chat/completions.
type ChatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream,omitempty"`
	// MaxTokens limits the number of tokens generated.
	MaxTokens *int `json:"max_tokens,omitempty"`
	// Temperature controls randomness (0–2).
	Temperature *float64 `json:"temperature,omitempty"`
}

// ChatChoice is a single completion choice.
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// Usage holds token counts reported by the backend.
type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float64 `json:"cost,omitempty"`
}

// ChatCompletionResponse is the non-streaming response from POST /v1/chat/completions.
type ChatCompletionResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   Usage        `json:"usage"`
}

// ---------------------------------------------------------------------------
// Responses API types  (POST /v1/responses)
// ---------------------------------------------------------------------------

// ResponsesRequest is the body for POST /v1/responses (OpenAI Responses API).
type ResponsesRequest struct {
	Model  string `json:"model"`
	Input  any    `json:"input"`
	Stream bool   `json:"stream,omitempty"`
}

// ---------------------------------------------------------------------------
// Error type
// ---------------------------------------------------------------------------

// APIError is returned when the server responds with a non-2xx status code.
type APIError struct {
	StatusCode int
	Message    string
	Code       any
}

func (e *APIError) Error() string {
	return e.Message
}

// ---------------------------------------------------------------------------
// Client options
// ---------------------------------------------------------------------------

// Option configures a [Client].
type Option func(*clientConfig)

type clientConfig struct {
	baseURL     string
	apiKey      string
	secretKey   string
	httpTimeout time.Duration
}

func defaultConfig() clientConfig {
	return clientConfig{
		baseURL:     "http://127.0.0.1:8318",
		httpTimeout: 120 * time.Second,
	}
}

// WithBaseURL overrides the proxy base URL (default: http://127.0.0.1:8318).
func WithBaseURL(u string) Option {
	return func(c *clientConfig) { c.baseURL = u }
}

// WithAPIKey sets the Authorization: Bearer <key> header for LLM API calls.
func WithAPIKey(key string) Option {
	return func(c *clientConfig) { c.apiKey = key }
}

// WithSecretKey sets the management API bearer token (used for /v0/management/* routes).
func WithSecretKey(key string) Option {
	return func(c *clientConfig) { c.secretKey = key }
}

// WithTimeout sets the HTTP client timeout (default: 120s).
func WithTimeout(d time.Duration) Option {
	return func(c *clientConfig) { c.httpTimeout = d }
}
