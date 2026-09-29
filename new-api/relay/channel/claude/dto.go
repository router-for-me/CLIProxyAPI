package claude

//
//type ClaudeMetadata struct {
//	UserId string `json:"user_id"`
//}
//
//type ClaudeMediaMessage struct {
//	Type        string               `json:"type"`
//	Text        string               `json:"text,omitempty"`
//	Source      *ClaudeMessageSource `json:"source,omitempty"`
//	Usage       *ClaudeUsage         `json:"usage,omitempty"`
//	StopReason  *string              `json:"stop_reason,omitempty"`
//	PartialJson string               `json:"partial_json,omitempty"`
//	Thinking    string               `json:"thinking,omitempty"`
//	Signature   string               `json:"signature,omitempty"`
//	Delta       string               `json:"delta,omitempty"`
//	// tool_calls
//	Id        string `json:"id,omitempty"`
//	Name      string `json:"name,omitempty"`
//	Input     any    `json:"input,omitempty"`
//	Content   string `json:"content,omitempty"`
//	ToolUseId string `json:"tool_use_id,omitempty"`
//}
//
//type ClaudeMessageSource struct {
//	Type      string `json:"type"`
//	MediaType string `json:"media_type"`
//	Data      string `json:"data"`
//}
//
//type ClaudeMessage struct {
//	Role    string `json:"role"`
//	Content any    `json:"content"`
//}
//
//type Tool struct {
//	Name        string                 `json:"name"`
//	Description string                 `json:"description,omitempty"`
//	InputSchema map[string]interface{} `json:"input_schema"`
//}
//
//type InputSchema struct {
//	Type       string `json:"type"`
//	Properties any    `json:"properties,omitempty"`
//	Required   any    `json:"required,omitempty"`
//}
//
//type ClaudeRequest struct {
//	Model             string          `json:"model"`
//	Prompt            string          `json:"prompt,omitempty"`
//	System            string          `json:"system,omitempty"`
//	Messages          []ClaudeMessage `json:"messages,omitempty"`
//	MaxTokens         uint            `json:"max_tokens,omitempty"`
//	MaxTokensToSample uint            `json:"max_tokens_to_sample,omitempty"`
//	StopSequences     []string        `json:"stop_sequences,omitempty"`
//	Temperature       *float64        `json:"temperature,omitempty"`
//	TopP              float64         `json:"top_p,omitempty"`
//	TopK              int             `json:"top_k,omitempty"`
//	//ClaudeMetadata    `json:"metadata,omitempty"`
//	Stream     bool      `json:"stream,omitempty"`
//	Tools      any       `json:"tools,omitempty"`
//	ToolChoice any       `json:"tool_choice,omitempty"`
//	Thinking   *Thinking `json:"thinking,omitempty"`
//}
//
//type Thinking struct {
//	Type         string `json:"type"`
//	BudgetTokens int    `json:"budget_tokens"`
//}
//
//type ClaudeError struct {
//	Type    string `json:"type"`
//	Message string `json:"message"`
//}
//
//type ClaudeResponse struct {
//	Id           string               `json:"id"`
//	Type         string               `json:"type"`
//	Content      []ClaudeMediaMessage `json:"content"`
//	Completion   string               `json:"completion"`
//	StopReason   string               `json:"stop_reason"`
//	Model        string               `json:"model"`
//	Error        ClaudeError          `json:"error"`
//	Usage        ClaudeUsage          `json:"usage"`
//	Index        int                  `json:"index"` // stream only
//	ContentBlock *ClaudeMediaMessage  `json:"content_block"`
//	Delta        *ClaudeMediaMessage  `json:"delta"`   // stream only
//	Message      *ClaudeResponse      `json:"message"` // stream only: message_start
//}
//
//type ClaudeUsage struct {
//	InputTokens  int `json:"input_tokens"`
//	OutputTokens int `json:"output_tokens"`
//}
