package ir

// EventType defines the type of event in the unified stream.
type EventType string

const (
	EventTypeToken            EventType = "token"
	EventTypeReasoning        EventType = "reasoning"         // For model reasoning/thinking content
	EventTypeReasoningSummary EventType = "reasoning_summary" // For reasoning summary (Responses API)
	EventTypeToolCall         EventType = "tool_call"         // Complete tool call
	EventTypeToolCallDelta    EventType = "tool_call_delta"   // Incremental tool call arguments (Responses API)
	EventTypeImage            EventType = "image"             // For inline image content
	EventTypeError            EventType = "error"
	EventTypeFinish           EventType = "finish"
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

// UnifiedEvent represents a single event in the chat stream.
// It is the "Esperanto" response format.
type UnifiedEvent struct {
	Type             EventType
	Content          string       // For EventTypeToken
	Reasoning        string       // For EventTypeReasoning (model thinking/reasoning content)
	ReasoningSummary string       // For EventTypeReasoningSummary (Responses API)
	ThoughtSignature string       // For Gemini thought signatures, Claude signatures, OpenAI reasoning_opaque, etc.
	ToolCall         *ToolCall    // For EventTypeToolCall
	ToolCallIndex    int          // Index for tool call in parallel calls (Responses API)
	Image            *ImagePart   // For EventTypeImage (inline image content)
	Error            error        // For EventTypeError
	Usage            *Usage       // Optional usage stats on Finish
	FinishReason     FinishReason // Why generation stopped (for EventTypeFinish)
	Refusal          string       // Refusal message (if model refuses to answer)
	Logprobs         interface{}  // Log probabilities (if requested)
	ContentFilter    interface{}  // Content filter results
	SystemFingerprint string      // System fingerprint
}

// Usage represents token usage statistics.
type Usage struct {
	PromptTokens       int
	CompletionTokens   int
	TotalTokens        int
	ThoughtsTokenCount int // Reasoning/thinking token count (for completion_tokens_details)
	CachedTokens       int // Cached input tokens (Responses API prompt caching)
	AudioTokens        int // Audio input tokens
	AcceptedPredictionTokens int // Accepted prediction tokens
	RejectedPredictionTokens int // Rejected prediction tokens
}

// ResponseMeta contains metadata from upstream response for passthrough.
// Used to preserve original response fields like responseId, createTime, finishReason.
type ResponseMeta struct {
	ResponseID         string // Original response ID from upstream (e.g., Gemini responseId)
	CreateTime         int64  // Unix timestamp from upstream (parsed from createTime)
	NativeFinishReason string // Original finish reason string from upstream (e.g., "STOP", "MAX_TOKENS")
}

// ToolCall represents a request from the model to execute a tool.
type ToolCall struct {
	ID               string
	Name             string
	Args             string // JSON string of arguments
	PartialArgs      string // Raw partial arguments (e.g. Gemini partialArgs)
	ThoughtSignature string // Gemini thought signature for this tool call
}

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

type ToolResultPart struct {
	ToolCallID string
	Result     string       // JSON string result
	Images     []*ImagePart // Multimodal tool result (images)
	Files      []*FilePart  // Multimodal tool result (files)
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
}

// UnifiedChatRequest represents the unified chat request structure.
// It is the "Esperanto" request format.
type UnifiedChatRequest struct {
	Model            string
	Messages         []Message
	Tools            []ToolDefinition
	Temperature      *float64 // Pointer to allow nil (default)
	TopP             *float64
	TopK             *int
	MaxTokens        *int
	StopSequences    []string
	Thinking         *ThinkingConfig // Specific to models that support "thinking" (e.g. Gemini 2.0 Flash Thinking)
	SafetySettings   []SafetySetting // Safety/content filtering settings
	ImageConfig      *ImageConfig    // Image generation configuration
	ResponseModality []string        // Response modalities (e.g., ["TEXT", "IMAGE"])
	Metadata         map[string]any  // Additional provider-specific metadata

	// Responses API specific fields
	Instructions       string         // System instructions (Responses API)
	PreviousResponseID string         // For conversation continuity (Responses API)
	PromptID           string         // Prompt template ID (Responses API)
	PromptVersion      string         // Prompt template version (Responses API)
	PromptVariables    map[string]any // Variables for prompt template (Responses API)
	PromptCacheKey     string         // Cache key for prompt caching (Responses API)
	Store              *bool          // Whether to store the response (Responses API)
	ParallelToolCalls  *bool          // Whether to allow parallel tool calls (Responses API)
	ToolChoice         string         // Tool choice mode (Responses API)
	ResponseSchema     map[string]any        // JSON Schema for structured output (Gemini/Ollama)
	FunctionCalling    *FunctionCallingConfig // Function calling configuration
}

// FunctionCallingConfig controls function calling behavior.
type FunctionCallingConfig struct {
	Mode                       string   // "AUTO", "ANY", "NONE"
	AllowedFunctionNames       []string // Whitelist of functions
	StreamFunctionCallArguments bool     // Enable streaming of arguments (Gemini 3+)
}

// ThinkingConfig controls the reasoning capabilities of the model.
type ThinkingConfig struct {
	IncludeThoughts bool
	Budget          int    // Token budget for thinking (-1 for auto, 0 for disabled)
	Summary         string // Reasoning summary mode: "auto", "concise", "detailed" (Responses API)
	Effort          string // Reasoning effort: "none", "low", "medium", "high" (Responses API)
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
