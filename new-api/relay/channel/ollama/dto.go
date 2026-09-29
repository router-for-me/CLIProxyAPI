package ollama

import (
	"encoding/json"
)

type OllamaChatMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	Images     []string         `json:"images,omitempty"`
	ToolCalls  []OllamaToolCall `json:"tool_calls,omitempty"`
	ToolName   string           `json:"tool_name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Thinking   json.RawMessage  `json:"thinking,omitempty"`
}

type OllamaToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type OllamaTool struct {
	Type     string             `json:"type"`
	Function OllamaToolFunction `json:"function"`
}

type OllamaToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	} `json:"function"`
}

type OllamaChatRequest struct {
	Model     string              `json:"model"`
	Messages  []OllamaChatMessage `json:"messages"`
	Tools     any                 `json:"tools,omitempty"`
	Format    any                 `json:"format,omitempty"`
	Stream    bool                `json:"stream"`
	Options   map[string]any      `json:"options,omitempty"`
	KeepAlive any                 `json:"keep_alive,omitempty"`
	Think     json.RawMessage     `json:"think,omitempty"`
}

type OllamaGenerateRequest struct {
	Model     string          `json:"model"`
	Prompt    string          `json:"prompt,omitempty"`
	Suffix    string          `json:"suffix,omitempty"`
	Images    []string        `json:"images,omitempty"`
	Format    any             `json:"format,omitempty"`
	Stream    bool            `json:"stream"`
	Options   map[string]any  `json:"options,omitempty"`
	KeepAlive any             `json:"keep_alive,omitempty"`
	Think     json.RawMessage `json:"think,omitempty"`
}

type OllamaEmbeddingRequest struct {
	Model      string         `json:"model"`
	Input      any            `json:"input"`
	Options    map[string]any `json:"options,omitempty"`
	Dimensions int            `json:"dimensions,omitempty"`
}

type OllamaEmbeddingResponse struct {
	Error           string      `json:"error,omitempty"`
	Model           string      `json:"model"`
	Embeddings      [][]float64 `json:"embeddings"`
	PromptEvalCount int         `json:"prompt_eval_count,omitempty"`
}

type OllamaTagsResponse struct {
	Models []OllamaModel `json:"models"`
}

type OllamaModel struct {
	Name       string            `json:"name"`
	Size       int64             `json:"size"`
	Digest     string            `json:"digest,omitempty"`
	ModifiedAt string            `json:"modified_at"`
	Details    OllamaModelDetail `json:"details"`
}

type OllamaModelDetail struct {
	ParentModel       string   `json:"parent_model,omitempty"`
	Format            string   `json:"format,omitempty"`
	Family            string   `json:"family,omitempty"`
	Families          []string `json:"families,omitempty"`
	ParameterSize     string   `json:"parameter_size,omitempty"`
	QuantizationLevel string   `json:"quantization_level,omitempty"`
}

type OllamaPullRequest struct {
	Name   string `json:"name"`
	Stream bool   `json:"stream,omitempty"`
}

type OllamaPullResponse struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
}

type OllamaDeleteRequest struct {
	Name string `json:"name"`
}
