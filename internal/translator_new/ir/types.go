package ir

import "github.com/tidwall/gjson"

// EventType defines the type of event in the unified stream.
type EventType string

const (
	EventTypeToken            EventType = "token"
	EventTypeReasoning        EventType = "reasoning"         // For model reasoning/thinking content
	EventTypeReasoningSummary EventType = "reasoning_summary" // For reasoning summary (Responses API)
	EventTypeToolCall         EventType = "tool_call"         // Complete tool call
	EventTypeToolCallDelta    EventType = "tool_call_delta"   // Incremental tool call arguments (Responses API)
	EventTypeImage            EventType = "image"             // For inline image content
	EventTypeFinish           EventType = "finish"
	EventTypeError            EventType = "error"
)

// FinishReason defines why the model stopped generating.
type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"           // Natural completion
	FinishReasonLength        FinishReason = "length"         // Max tokens reached
	FinishReasonToolCalls     FinishReason = "tool_calls"     // Model wants to call tools
	FinishReasonContentFilter FinishReason = "content_filter" // Content filtered by safety
	FinishReasonError         FinishReason = "error"          // Error occurred
	FinishReasonUnknown       FinishReason = "unknown"        // Unknown reason
)

// Role defines the role of the message sender.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// ContentType defines the type of content part.
type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeReasoning  ContentType = "reasoning" // For model reasoning/thinking content
	ContentTypeImage      ContentType = "image"
	ContentTypeFile       ContentType = "file" // For file inputs (PDF, etc.) - Responses API
	ContentTypeToolResult ContentType = "tool_result"
)

// Usage represents token usage statistics.
type Usage struct {
	PromptTokens             int
	CompletionTokens         int
	TotalTokens              int
	ThoughtsTokenCount       int // Reasoning/thinking token count (for completion_tokens_details)
	CachedTokens             int // Cached input tokens (Responses API prompt caching)
	AudioTokens              int // Audio input tokens
	AcceptedPredictionTokens int // Accepted prediction tokens
	RejectedPredictionTokens int // Rejected prediction tokens
}

// ResponseMeta contains metadata from upstream response for passthrough.
// Used to preserve original response fields like responseId, createTime, finishReason.
type ResponseMeta struct {
	ResponseID         string // Original response ID from upstream (e.g., Gemini responseId)
	NativeFinishReason string // Original finish reason string from upstream (e.g., "STOP", "MAX_TOKENS")
	CreateTime         int64  // Unix timestamp from upstream (parsed from createTime)
}

// OpenAIMeta contains metadata specific to OpenAI-like responses.
// This struct is used to pass through original response fields.
type OpenAIMeta struct {
	ResponseID         string // Original response ID
	CreateTime         int64  // Creation timestamp
	NativeFinishReason string // Original finish reason string
	ThoughtsTokenCount int    // Token count for reasoning thoughts
}

// ToolCall represents a request from the model to execute a tool.
type ToolCall struct {
	ID               string
	ItemID           string // Codex internal item_id (used for mapping in streaming)
	Name             string
	Args             string // JSON string of arguments (or raw text for custom tools)
	PartialArgs      string // Raw partial arguments (e.g. Gemini partialArgs)
	ThoughtSignature string // Gemini thought signature for this tool call
	IsCustom         bool   // True for custom tools (e.g., apply_patch with grammar format)
}

// ImagePart represents an image content part.
type ImagePart struct {
	MimeType string
	Data     string // Base64 encoded data
	URL      string // URL for remote images (Responses API)
}

// FilePart represents a file input (PDF, etc.) for Responses API.
type FilePart struct {
	FileID   string // File ID from uploaded file
	FileURL  string // URL for remote file
	Filename string // Original filename
	FileData string // Base64 encoded data (data:application/pdf;base64,...)
}

// ToolResultPart represents the result of a tool execution.
type ToolResultPart struct {
	ToolCallID string
	ToolName   string       // Name of the tool that was called (for custom tool detection)
	Result     string       // JSON string result
	Images     []*ImagePart // Multimodal tool result (images)
	Files      []*FilePart  // Multimodal tool result (files)
}

// ContentPart represents a discrete part of a message (e.g., a block of text, an image).
type ContentPart struct {
	Type             ContentType
	Text             string          // Populated if Type == ContentTypeText
	Reasoning        string          // Populated if Type == ContentTypeReasoning
	ThoughtSignature string          // Gemini thought signature
	Image            *ImagePart      // Populated if Type == ContentTypeImage
	File             *FilePart       // Populated if Type == ContentTypeFile (Responses API)
	ToolResult       *ToolResultPart // Populated if Type == ContentTypeToolResult
}

// Message represents a single message in the conversation history.
type Message struct {
	Role      Role
	Content   []ContentPart
	ToolCalls []ToolCall // Populated if Role == RoleAssistant and there are tool calls
}

// ToolDefinition represents a tool capability exposed to the model.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]interface{} // JSON Schema object (cleaned)
	Format      map[string]interface{} // Grammar format for custom tools (e.g., apply_patch)
	IsCustom    bool                   // True for custom/freeform tools that use raw text input
}

// ThinkingConfig controls the reasoning capabilities of the model.
type ThinkingConfig struct {
	Summary         string // Reasoning summary mode: "auto", "concise", "detailed" (Responses API)
	Effort          string // Reasoning effort: "none", "low", "medium", "high" (Responses API)
	Budget          int    // Token budget for thinking (-1 for auto, 0 for disabled)
	IncludeThoughts bool
}

// FunctionCallingConfig controls function calling behavior.
type FunctionCallingConfig struct {
	Mode                        string   // "AUTO", "ANY", "NONE"
	AllowedFunctionNames        []string // Whitelist of functions
	StreamFunctionCallArguments bool     // Enable streaming of arguments (Gemini 3+)
}

// SafetySetting represents content safety filtering configuration.
type SafetySetting struct {
	Category  string // e.g., "HARM_CATEGORY_HARASSMENT"
	Threshold string // e.g., "OFF", "BLOCK_NONE", "BLOCK_LOW_AND_ABOVE"
}

// ImageConfig controls image generation parameters.
type ImageConfig struct {
	AspectRatio string // e.g., "1:1", "16:9", "9:16"
	ImageSize   string // e.g., "256x256", "512x512"
}

// UnifiedChatRequest represents the unified chat request structure.
// It is the "Esperanto" request format.
type UnifiedChatRequest struct {
	Model              string
	Messages           []Message
	Tools              []ToolDefinition
	Temperature        *float64
	TopP               *float64
	TopK               *int
	MaxTokens          *int
	StopSequences      []string
	Thinking           *ThinkingConfig        // Specific to models that support "thinking"
	SafetySettings     []SafetySetting        // Safety/content filtering settings
	ImageConfig        *ImageConfig           // Image generation configuration
	ResponseModality   []string               // Response modalities (e.g., ["TEXT", "IMAGE"])
	Metadata           map[string]any         // Additional provider-specific metadata
	Instructions       string                 // System instructions (Responses API)
	PreviousResponseID string                 // For conversation continuity (Responses API)
	PromptID           string                 // Prompt template ID (Responses API)
	PromptVersion      string                 // Prompt template version (Responses API)
	PromptVariables    map[string]any         // Variables for prompt template (Responses API)
	PromptCacheKey     string                 // Cache key for prompt caching (Responses API)
	ToolChoice         string                 // Tool choice mode (Responses API)
	ResponseSchema     map[string]any         // JSON Schema for structured output (Gemini/Ollama)
	FunctionCalling    *FunctionCallingConfig // Function calling configuration
	Store              *bool                  // Whether to store the response (Responses API)
	ParallelToolCalls  *bool                  // Whether to allow parallel tool calls (Responses API)
}

// UnifiedEvent represents a single event in the chat stream.
// It is the "Esperanto" response format.
type UnifiedEvent struct {
	Type              EventType
	Content           string       // For EventTypeToken
	Reasoning         string       // For EventTypeReasoning (model thinking/reasoning content)
	ReasoningSummary  string       // For EventTypeReasoningSummary (Responses API)
	ThoughtSignature  string       // For Gemini thought signatures, Claude signatures, OpenAI reasoning_opaque, etc.
	Refusal           string       // Refusal message (if model refuses to answer)
	SystemFingerprint string       // System fingerprint
	ToolCall          *ToolCall    // For EventTypeToolCall
	Image             *ImagePart   // For EventTypeImage (inline image content)
	Usage             *Usage       // Optional usage stats on Finish
	Error             error        // For EventTypeError
	Logprobs          interface{}  // Log probabilities (if requested)
	ContentFilter     interface{}  // Content filter results
	ToolCallIndex     int          // Index for tool call in parallel calls (Responses API)
	FinishReason      FinishReason // Why generation stopped (for EventTypeFinish)
}

// ParseOpenAIUsage parses usage statistics from OpenAI response.
func ParseOpenAIUsage(u gjson.Result) *Usage {
	if !u.Exists() {
		return nil
	}
	usage := &Usage{
		PromptTokens:     int(u.Get("prompt_tokens").Int()),
		CompletionTokens: int(u.Get("completion_tokens").Int()),
		TotalTokens:      int(u.Get("total_tokens").Int()),
	}
	if v := u.Get("prompt_tokens_details.cached_tokens"); v.Exists() {
		usage.CachedTokens = int(v.Int())
	}
	if v := u.Get("prompt_tokens_details.audio_tokens"); v.Exists() {
		usage.AudioTokens = int(v.Int())
	}
	if v := u.Get("completion_tokens_details.reasoning_tokens"); v.Exists() {
		usage.ThoughtsTokenCount = int(v.Int())
	}
	if v := u.Get("completion_tokens_details.accepted_prediction_tokens"); v.Exists() {
		usage.AcceptedPredictionTokens = int(v.Int())
	}
	if v := u.Get("completion_tokens_details.rejected_prediction_tokens"); v.Exists() {
		usage.RejectedPredictionTokens = int(v.Int())
	}
	return usage
}
