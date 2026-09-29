package dto

// PluginResponsesResponse is the host-owned Responses facade used for stream
// snapshots and sanitized terminal failures. Non-stream success objects may
// retain additional validated plugin fields, but identifiers, lifecycle state,
// and retrieval metadata are always populated by the host.
type PluginResponsesResponse struct {
	ID                string                           `json:"id"`
	Object            string                           `json:"object"`
	CreatedAt         int64                            `json:"created_at"`
	Status            string                           `json:"status"`
	Error             *PluginResponsesError            `json:"error"`
	IncompleteDetails *PluginResponsesIncompleteDetail `json:"incomplete_details"`
	Instructions      any                              `json:"instructions"`
	Model             string                           `json:"model"`
	Output            []PluginResponsesOutput          `json:"output"`
	ParallelToolCalls bool                             `json:"parallel_tool_calls"`
	Temperature       float64                          `json:"temperature"`
	ToolChoice        any                              `json:"tool_choice"`
	Tools             []any                            `json:"tools"`
	TopP              float64                          `json:"top_p"`
	Metadata          map[string]string                `json:"metadata"`
	Usage             *PluginResponsesUsage            `json:"usage"`
}

type PluginResponsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PluginResponsesIncompleteDetail struct {
	Reason string `json:"reason"`
}

type PluginResponsesUsage struct {
	InputTokens         int                               `json:"input_tokens"`
	InputTokensDetails  PluginResponsesInputTokenDetails  `json:"input_tokens_details"`
	OutputTokens        int                               `json:"output_tokens"`
	OutputTokensDetails PluginResponsesOutputTokenDetails `json:"output_tokens_details"`
	TotalTokens         int                               `json:"total_tokens"`
}

type PluginResponsesInputTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type PluginResponsesOutputTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type PluginResponsesOutput struct {
	ID      string                   `json:"id"`
	Type    string                   `json:"type"`
	Status  string                   `json:"status"`
	Role    string                   `json:"role"`
	Content []PluginResponsesContent `json:"content"`
}

type PluginResponsesContent struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
	Logprobs    []any  `json:"logprobs"`
}

// PluginResponsesStreamEvent contains the exact event-specific fields used by
// the host Responses state machine. Pointer fields preserve required empty
// strings and arrays on delta/done events while omitting unrelated fields.
type PluginResponsesStreamEvent struct {
	Type           string                   `json:"type"`
	SequenceNumber int                      `json:"sequence_number"`
	Response       *PluginResponsesResponse `json:"response,omitempty"`
	OutputIndex    *int                     `json:"output_index,omitempty"`
	ContentIndex   *int                     `json:"content_index,omitempty"`
	ItemID         string                   `json:"item_id,omitempty"`
	Item           *PluginResponsesOutput   `json:"item,omitempty"`
	Part           *PluginResponsesContent  `json:"part,omitempty"`
	Delta          *string                  `json:"delta,omitempty"`
	Text           *string                  `json:"text,omitempty"`
	Logprobs       *[]any                   `json:"logprobs,omitempty"`
}
