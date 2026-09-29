package dto

import (
	"encoding/json"
	"fmt"
	"strings"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

const (
	ResponsesOutputTypeImageGenerationCall = "image_generation_call"
)

type SimpleResponse struct {
	Usage `json:"usage"`
	Error any `json:"error"`
}

// GetOpenAIError 从动态错误类型中提取OpenAIError结构
func (s *SimpleResponse) GetOpenAIError() *types.OpenAIError {
	return GetOpenAIError(s.Error)
}

type TextResponse struct {
	Id      string                     `json:"id"`
	Object  string                     `json:"object"`
	Created int64                      `json:"created"`
	Model   string                     `json:"model"`
	Choices []OpenAITextResponseChoice `json:"choices"`
	Usage   `json:"usage"`
}

type OpenAITextResponseChoice struct {
	Index        int `json:"index"`
	Message      `json:"message"`
	FinishReason string `json:"finish_reason"`
}

type OpenAITextResponse struct {
	Id      string                     `json:"id"`
	Model   string                     `json:"model"`
	Object  string                     `json:"object"`
	Created any                        `json:"created"`
	Choices []OpenAITextResponseChoice `json:"choices"`
	Error   any                        `json:"error,omitempty"`
	Usage   `json:"usage"`
}

// GetOpenAIError 从动态错误类型中提取OpenAIError结构
func (o *OpenAITextResponse) GetOpenAIError() *types.OpenAIError {
	return GetOpenAIError(o.Error)
}

type OpenAIEmbeddingResponseItem struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type OpenAIEmbeddingResponse struct {
	Object string                        `json:"object"`
	Data   []OpenAIEmbeddingResponseItem `json:"data"`
	Model  string                        `json:"model"`
	Usage  `json:"usage"`
}

type FlexibleEmbeddingResponseItem struct {
	Object    string `json:"object"`
	Index     int    `json:"index"`
	Embedding any    `json:"embedding"`
}

type FlexibleEmbeddingResponse struct {
	Object string                          `json:"object"`
	Data   []FlexibleEmbeddingResponseItem `json:"data"`
	Model  string                          `json:"model"`
	Usage  `json:"usage"`
}

type ChatCompletionsStreamResponseChoice struct {
	Delta        ChatCompletionsStreamResponseChoiceDelta `json:"delta"`
	Logprobs     *any                                     `json:"logprobs"`
	FinishReason *string                                  `json:"finish_reason"`
	Index        int                                      `json:"index"`
}

type ChatCompletionsStreamResponseChoiceDelta struct {
	Content          *string            `json:"content,omitempty"`
	ReasoningContent *string            `json:"reasoning_content,omitempty"`
	Reasoning        *string            `json:"reasoning,omitempty"`
	Role             string             `json:"role,omitempty"`
	ToolCalls        []ToolCallResponse `json:"tool_calls,omitempty"`
	// Annotations is an OpenAI-compatible streaming extension supported by
	// providers such as OpenRouter. Relaykit uses it to preserve streaming URL
	// citations, including Claude round-trip metadata.
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

func (c *ChatCompletionsStreamResponseChoiceDelta) SetContentString(s string) {
	c.Content = &s
}

func (c *ChatCompletionsStreamResponseChoiceDelta) GetContentString() string {
	if c.Content == nil {
		return ""
	}
	return *c.Content
}

func (c *ChatCompletionsStreamResponseChoiceDelta) GetReasoningContent() string {
	if c.ReasoningContent == nil && c.Reasoning == nil {
		return ""
	}
	if c.ReasoningContent != nil {
		return *c.ReasoningContent
	}
	return *c.Reasoning
}

func (c *ChatCompletionsStreamResponseChoiceDelta) SetReasoningContent(s string) {
	c.ReasoningContent = &s
	//c.Reasoning = &s
}

type ToolCallResponse struct {
	// Index is not nil only in chat completion chunk object
	Index    *int             `json:"index,omitempty"`
	ID       string           `json:"id,omitempty"`
	Type     any              `json:"type"`
	Function FunctionResponse `json:"function"`
}

func (c *ToolCallResponse) SetIndex(i int) {
	c.Index = &i
}

type FunctionResponse struct {
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`
	// call function with arguments in JSON format
	Parameters any    `json:"parameters,omitempty"` // request
	Arguments  string `json:"arguments"`            // response
}

type ChatCompletionsStreamResponse struct {
	Id                string                                `json:"id"`
	Object            string                                `json:"object"`
	Created           int64                                 `json:"created"`
	Model             string                                `json:"model"`
	SystemFingerprint *string                               `json:"system_fingerprint"`
	Choices           []ChatCompletionsStreamResponseChoice `json:"choices"`
	Usage             *Usage                                `json:"usage"`
}

func (c *ChatCompletionsStreamResponse) IsFinished() bool {
	if len(c.Choices) == 0 {
		return false
	}
	return c.Choices[0].FinishReason != nil && *c.Choices[0].FinishReason != ""
}

func (c *ChatCompletionsStreamResponse) IsToolCall() bool {
	if len(c.Choices) == 0 {
		return false
	}
	return len(c.Choices[0].Delta.ToolCalls) > 0
}

func (c *ChatCompletionsStreamResponse) GetFirstToolCall() *ToolCallResponse {
	if c.IsToolCall() {
		return &c.Choices[0].Delta.ToolCalls[0]
	}
	return nil
}

func (c *ChatCompletionsStreamResponse) ClearToolCalls() {
	if !c.IsToolCall() {
		return
	}
	for choiceIdx := range c.Choices {
		for callIdx := range c.Choices[choiceIdx].Delta.ToolCalls {
			c.Choices[choiceIdx].Delta.ToolCalls[callIdx].ID = ""
			c.Choices[choiceIdx].Delta.ToolCalls[callIdx].Type = nil
			c.Choices[choiceIdx].Delta.ToolCalls[callIdx].Function.Name = ""
		}
	}
}

func (c *ChatCompletionsStreamResponse) Copy() *ChatCompletionsStreamResponse {
	choices := make([]ChatCompletionsStreamResponseChoice, len(c.Choices))
	copy(choices, c.Choices)
	return &ChatCompletionsStreamResponse{
		Id:                c.Id,
		Object:            c.Object,
		Created:           c.Created,
		Model:             c.Model,
		SystemFingerprint: c.SystemFingerprint,
		Choices:           choices,
		Usage:             c.Usage,
	}
}

func (c *ChatCompletionsStreamResponse) GetSystemFingerprint() string {
	if c.SystemFingerprint == nil {
		return ""
	}
	return *c.SystemFingerprint
}

func (c *ChatCompletionsStreamResponse) SetSystemFingerprint(s string) {
	c.SystemFingerprint = &s
}

type ChatCompletionsStreamResponseSimple struct {
	Choices []ChatCompletionsStreamResponseChoice `json:"choices"`
	Usage   *Usage                                `json:"usage"`
}

type CompletionsStreamResponse struct {
	Choices []struct {
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type Usage struct {
	PromptTokens         int           `json:"prompt_tokens"`
	CompletionTokens     int           `json:"completion_tokens"`
	TotalTokens          int           `json:"total_tokens"`
	PromptCacheHitTokens int           `json:"prompt_cache_hit_tokens,omitempty"`
	UsageSemantic        string        `json:"usage_semantic,omitempty"`
	UsageSource          string        `json:"usage_source,omitempty"`
	BillingUsage         *BillingUsage `json:"billing_usage,omitempty"`

	PromptTokensDetails    InputTokenDetails   `json:"prompt_tokens_details"`
	CompletionTokenDetails OutputTokenDetails  `json:"completion_tokens_details"`
	InputTokens            int                 `json:"input_tokens"`
	OutputTokens           int                 `json:"output_tokens"`
	InputTokensDetails     *InputTokenDetails  `json:"input_tokens_details"`
	OutputTokensDetails    *OutputTokenDetails `json:"output_tokens_details,omitempty"`

	// claude cache 1h
	ClaudeCacheCreation5mTokens int `json:"claude_cache_creation_5_m_tokens"`
	ClaudeCacheCreation1hTokens int `json:"claude_cache_creation_1_h_tokens"`

	// OpenRouter Params
	Cost any `json:"cost,omitempty"`
}

type OpenAIVideoResponse struct {
	Id        string `json:"id" example:"file-abc123"`
	Object    string `json:"object" example:"file"`
	Bytes     int64  `json:"bytes" example:"120000"`
	CreatedAt int64  `json:"created_at" example:"1677610602"`
	ExpiresAt int64  `json:"expires_at" example:"1677614202"`
	Filename  string `json:"filename" example:"mydata.jsonl"`
	Purpose   string `json:"purpose" example:"fine-tune"`
}

type InputTokenDetails struct {
	CachedTokens         int                 `json:"cached_tokens"`
	CachedTokensDetails  *CachedTokenDetails `json:"cached_tokens_details,omitempty"`
	CachedCreationTokens int                 `json:"cached_creation_tokens,omitempty"`
	// CacheWriteTokens is OpenAI's native cache-write count, reported as
	// prompt_tokens_details.cache_write_tokens (Chat Completions) or
	// input_tokens_details.cache_write_tokens (Responses). It is billed at the
	// cache-creation price.
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	TextTokens       int `json:"text_tokens"`
	AudioTokens      int `json:"audio_tokens"`
	ImageTokens      int `json:"image_tokens"`
}

// CachedTokenDetails describes subsets of cached_tokens. Pointers distinguish
// an unreported modality from an explicitly reported zero.
type CachedTokenDetails struct {
	TextTokens  *int `json:"text_tokens,omitempty"`
	ImageTokens *int `json:"image_tokens,omitempty"`
	AudioTokens *int `json:"audio_tokens,omitempty"`
}

func (d *CachedTokenDetails) HasTokens() bool {
	return d != nil && (d.TextTokens != nil && *d.TextTokens != 0 ||
		d.ImageTokens != nil && *d.ImageTokens != 0 || d.AudioTokens != nil && *d.AudioTokens != 0)
}

// Clone preserves presence information without sharing mutable usage fields.
func (d InputTokenDetails) Clone() InputTokenDetails {
	if d.CachedTokensDetails != nil {
		cached := *d.CachedTokensDetails
		if cached.TextTokens != nil {
			value := *cached.TextTokens
			cached.TextTokens = &value
		}
		if cached.ImageTokens != nil {
			value := *cached.ImageTokens
			cached.ImageTokens = &value
		}
		if cached.AudioTokens != nil {
			value := *cached.AudioTokens
			cached.AudioTokens = &value
		}
		d.CachedTokensDetails = &cached
	}
	return d
}

// CacheCreationTokensTotal returns the cache-write token count regardless of
// which field the upstream reported it in: Claude-derived conversions populate
// CachedCreationTokens while OpenAI reports cache_write_tokens natively. Both
// are billed at the cache-creation price; when both are present the larger
// value wins so the same tokens are never double-counted. Negative upstream
// values are clamped to zero so they can never lower a charge.
func (d InputTokenDetails) CacheCreationTokensTotal() int {
	total := max(d.CacheWriteTokens, d.CachedCreationTokens)
	if total < 0 {
		return 0
	}
	return total
}

type OutputTokenDetails struct {
	TextTokens      int `json:"text_tokens"`
	AudioTokens     int `json:"audio_tokens"`
	ImageTokens     int `json:"image_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
}

type OpenAIResponsesResponse struct {
	ID                 string             `json:"id"`
	Object             string             `json:"object"`
	CreatedAt          IntValue           `json:"created_at"`
	Status             json.RawMessage    `json:"status"`
	Error              any                `json:"error,omitempty"`
	IncompleteDetails  *IncompleteDetails `json:"incomplete_details,omitempty"`
	Instructions       json.RawMessage    `json:"instructions"`
	MaxOutputTokens    int                `json:"max_output_tokens"`
	Model              string             `json:"model"`
	Output             []ResponsesOutput  `json:"output"`
	ParallelToolCalls  bool               `json:"parallel_tool_calls"`
	PreviousResponseID json.RawMessage    `json:"previous_response_id"`
	Reasoning          *Reasoning         `json:"reasoning"`
	Store              bool               `json:"store"`
	Temperature        float64            `json:"temperature"`
	ToolChoice         json.RawMessage    `json:"tool_choice"`
	Tools              []map[string]any   `json:"tools"`
	TopP               float64            `json:"top_p"`
	Truncation         json.RawMessage    `json:"truncation"`
	Usage              *Usage             `json:"usage"`
	User               json.RawMessage    `json:"user"`
	Metadata           json.RawMessage    `json:"metadata"`
}

// GetOpenAIError 从动态错误类型中提取OpenAIError结构
func (o *OpenAIResponsesResponse) GetOpenAIError() *types.OpenAIError {
	return GetOpenAIError(o.Error)
}

type IncompleteDetails struct {
	Reason string `json:"reason"`
}

type ResponsesOutput struct {
	Type                string                          `json:"type"`
	ID                  string                          `json:"id"`
	Status              string                          `json:"status"`
	Role                string                          `json:"role"`
	Content             []ResponsesOutputContent        `json:"content"`
	Summary             []ResponsesReasoningSummaryPart `json:"summary,omitempty"`
	Quality             string                          `json:"quality"`
	Size                string                          `json:"size"`
	Result              string                          `json:"result,omitempty"`
	CallId              string                          `json:"call_id,omitempty"`
	Name                string                          `json:"name,omitempty"`
	Arguments           json.RawMessage                 `json:"arguments,omitempty"`
	Action              json.RawMessage                 `json:"action,omitempty"`
	Queries             json.RawMessage                 `json:"queries,omitempty"`
	Results             json.RawMessage                 `json:"results,omitempty"`
	Sources             json.RawMessage                 `json:"sources,omitempty"`
	Code                json.RawMessage                 `json:"code,omitempty"`
	Outputs             json.RawMessage                 `json:"outputs,omitempty"`
	ContainerID         string                          `json:"container_id,omitempty"`
	PendingSafetyChecks json.RawMessage                 `json:"pending_safety_checks,omitempty"`
	Caller              json.RawMessage                 `json:"caller,omitempty"`
	ServerLabel         string                          `json:"server_label,omitempty"`
	Output              json.RawMessage                 `json:"output,omitempty"`
	ItemError           json.RawMessage                 `json:"error,omitempty"`
	ApprovalRequestID   string                          `json:"approval_request_id,omitempty"`
	MCPTools            json.RawMessage                 `json:"tools,omitempty"`
}

// MarshalJSON keeps hosted-tool variants within their protocol-specific
// schemas. ResponsesOutput also represents messages, images, and function
// calls, whose fields must not leak into web_search_call or mcp_call items.
func (r ResponsesOutput) MarshalJSON() ([]byte, error) {
	switch r.Type {
	case "web_search_call":
		return kitutil.Marshal(struct {
			Type   string          `json:"type"`
			ID     string          `json:"id"`
			Status string          `json:"status,omitempty"`
			Action json.RawMessage `json:"action,omitempty"`
		}{Type: r.Type, ID: r.ID, Status: r.Status, Action: r.Action})
	case "mcp_call":
		return kitutil.Marshal(struct {
			Type              string          `json:"type"`
			ID                string          `json:"id"`
			Name              string          `json:"name"`
			ServerLabel       string          `json:"server_label"`
			Arguments         json.RawMessage `json:"arguments"`
			Status            string          `json:"status,omitempty"`
			Output            json.RawMessage `json:"output,omitempty"`
			Error             json.RawMessage `json:"error,omitempty"`
			ApprovalRequestID string          `json:"approval_request_id,omitempty"`
		}{
			Type:              r.Type,
			ID:                r.ID,
			Name:              r.Name,
			ServerLabel:       r.ServerLabel,
			Arguments:         r.Arguments,
			Status:            r.Status,
			Output:            r.Output,
			Error:             r.ItemError,
			ApprovalRequestID: r.ApprovalRequestID,
		})
	default:
		type responsesOutputAlias ResponsesOutput
		return kitutil.Marshal(responsesOutputAlias(r))
	}
}

// NormalizeResponsesWebSearchAction validates and canonicalizes the current
// Responses web_search_call action union. Claude emits {"query": ...}; the
// Responses representation additionally requires a discriminator.
func NormalizeResponsesWebSearchAction(raw json.RawMessage) (json.RawMessage, error) {
	var action struct {
		Type    string          `json:"type"`
		Query   string          `json:"query"`
		Queries []string        `json:"queries"`
		Sources json.RawMessage `json:"sources"`
		URL     string          `json:"url"`
		Pattern string          `json:"pattern"`
	}
	if err := kitutil.Unmarshal(raw, &action); err != nil {
		return nil, fmt.Errorf("decode Responses web-search action: %w", err)
	}
	action.Type = strings.TrimSpace(action.Type)
	action.Query = strings.TrimSpace(action.Query)
	action.URL = strings.TrimSpace(action.URL)
	action.Pattern = strings.TrimSpace(action.Pattern)
	for index := range action.Queries {
		action.Queries[index] = strings.TrimSpace(action.Queries[index])
		if action.Queries[index] == "" {
			return nil, fmt.Errorf("Responses web-search action queries[%d] must not be empty", index)
		}
	}
	if action.Type == "" && (action.Query != "" || len(action.Queries) > 0) {
		action.Type = "search"
	}

	var canonical any
	switch action.Type {
	case "search":
		if action.Query == "" && len(action.Queries) == 0 {
			return nil, fmt.Errorf("Responses web-search action %q requires query or queries", action.Type)
		}
		if len(action.Sources) > 0 && kitutil.GetJsonType(action.Sources) != "array" && kitutil.GetJsonType(action.Sources) != "null" {
			return nil, fmt.Errorf("Responses web-search action sources must be an array")
		}
		canonical = struct {
			Type    string          `json:"type"`
			Query   string          `json:"query,omitempty"`
			Queries []string        `json:"queries,omitempty"`
			Sources json.RawMessage `json:"sources,omitempty"`
		}{Type: action.Type, Query: action.Query, Queries: action.Queries, Sources: action.Sources}
	case "open_page":
		if action.URL == "" {
			return nil, fmt.Errorf("Responses web-search action %q requires url", action.Type)
		}
		canonical = struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		}{Type: action.Type, URL: action.URL}
	case "find", "find_in_page":
		if action.URL == "" || action.Pattern == "" {
			return nil, fmt.Errorf("Responses web-search action %q requires url and pattern", action.Type)
		}
		canonical = struct {
			Type    string `json:"type"`
			URL     string `json:"url"`
			Pattern string `json:"pattern"`
		}{Type: "find_in_page", URL: action.URL, Pattern: action.Pattern}
	default:
		return nil, fmt.Errorf("unsupported Responses web-search action type %q", action.Type)
	}
	encoded, err := kitutil.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("encode Responses web-search action: %w", err)
	}
	return encoded, nil
}

// ArgumentsString returns function call arguments in the string form expected by Chat Completions.
func (r *ResponsesOutput) ArgumentsString() string {
	if r == nil {
		return ""
	}
	return ResponsesArgumentsString(r.Arguments)
}

// ResponsesArgumentsString returns function call arguments in the string form expected by Chat Completions.
func ResponsesArgumentsString(arguments json.RawMessage) string {
	return kitutil.JsonRawMessageToString(arguments)
}

type ResponsesOutputContent struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
}

type ResponsesReasoningSummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

const (
	BuildInToolWebSearchPreview = "web_search_preview"
	BuildInToolWebSearch        = "web_search"
	BuildInToolFileSearch       = "file_search"
	BuildInToolGoogleSearch     = "google_search"
	BuildInToolImageGeneration  = "image_generation"
)

const (
	BuildInCallWebSearchCall  = "web_search_call"
	BuildInCallFileSearchCall = "file_search_call"
	BuildInCallFunctionCall   = "function_call"
	BuildInCallToolUse        = "tool_use"
)

const (
	ResponsesOutputTypeItemAdded = "response.output_item.added"
	ResponsesOutputTypeItemDone  = "response.output_item.done"
)

// ResponsesStreamResponse 用于处理 /v1/responses 流式响应
type ResponsesStreamResponse struct {
	Type            string                   `json:"type"`
	Response        *OpenAIResponsesResponse `json:"response,omitempty"`
	Code            string                   `json:"code,omitempty"`
	Message         string                   `json:"message,omitempty"`
	Param           string                   `json:"param,omitempty"`
	Delta           string                   `json:"delta,omitempty"`
	Arguments       *string                  `json:"arguments,omitempty"`
	Name            string                   `json:"name,omitempty"`
	Text            *string                  `json:"text,omitempty"`
	Item            *ResponsesOutput         `json:"item,omitempty"`
	SequenceNumber  *int                     `json:"sequence_number,omitempty"`
	Annotation      json.RawMessage          `json:"annotation,omitempty"`
	AnnotationIndex *int                     `json:"annotation_index,omitempty"`
	Obfuscation     string                   `json:"obfuscation,omitempty"`
	// - response.function_call_arguments.delta
	// - response.function_call_arguments.done
	OutputIndex  *int                           `json:"output_index,omitempty"`
	ContentIndex *int                           `json:"content_index,omitempty"`
	SummaryIndex *int                           `json:"summary_index,omitempty"`
	ItemID       string                         `json:"item_id,omitempty"`
	Part         *ResponsesReasoningSummaryPart `json:"part,omitempty"`
}

// GetOpenAIError 从动态错误类型中提取OpenAIError结构
func GetOpenAIError(errorField any) *types.OpenAIError {
	if errorField == nil {
		return nil
	}

	switch err := errorField.(type) {
	case types.OpenAIError:
		return &err
	case *types.OpenAIError:
		return err
	case map[string]any:
		// 处理从JSON解析来的map结构
		openaiErr := &types.OpenAIError{}
		if errType, ok := err["type"].(string); ok {
			openaiErr.Type = errType
		}
		if errMsg, ok := err["message"].(string); ok {
			openaiErr.Message = errMsg
		}
		if errParam, ok := err["param"].(string); ok {
			openaiErr.Param = errParam
		}
		if errCode, ok := err["code"]; ok {
			openaiErr.Code = errCode
		}
		return openaiErr
	case string:
		// 处理简单字符串错误
		return &types.OpenAIError{
			Type:    "error",
			Message: err,
		}
	default:
		// 未知类型，尝试转换为字符串
		return &types.OpenAIError{
			Type:    "unknown_error",
			Message: fmt.Sprintf("%v", err),
		}
	}
}
