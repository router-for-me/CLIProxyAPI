package main

import "encoding/json"

type kiroPayload struct {
	ConversationState struct {
		AgentContinuationID string               `json:"agentContinuationId,omitempty"`
		AgentTaskType       string               `json:"agentTaskType,omitempty"`
		ChatTriggerType     string               `json:"chatTriggerType"`
		ConversationID      string               `json:"conversationId"`
		CurrentMessage      kiroCurrentMessage   `json:"currentMessage"`
		History             []kiroHistoryMessage `json:"history,omitempty"`
	} `json:"conversationState"`
	InferenceConfig      *kiroInferenceConfig `json:"inferenceConfig,omitempty"`
	ToolNameMap          map[string]string    `json:"-"`
	EstimatedInputTokens int                  `json:"-"`
}

type kiroCurrentMessage struct {
	UserInputMessage kiroUserInputMessage `json:"userInputMessage"`
}

type kiroUserInputMessage struct {
	Content                 string                  `json:"content"`
	ModelID                 string                  `json:"modelId,omitempty"`
	Origin                  string                  `json:"origin"`
	Images                  []kiroImage             `json:"images,omitempty"`
	UserInputMessageContext *kiroUserMessageContext `json:"userInputMessageContext,omitempty"`
}

type kiroUserMessageContext struct {
	Tools       []kiroToolWrapper `json:"tools,omitempty"`
	ToolResults []kiroToolResult  `json:"toolResults,omitempty"`
}

type kiroToolWrapper struct {
	ToolSpecification kiroToolSpecification `json:"toolSpecification"`
}

type kiroToolSpecification struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema kiroInputSchema `json:"inputSchema"`
}

type kiroInputSchema struct {
	JSON any `json:"json"`
}

type kiroToolResult struct {
	ToolUseID string              `json:"toolUseId"`
	Content   []kiroResultContent `json:"content"`
	Status    string              `json:"status"`
}

type kiroResultContent struct {
	Text string `json:"text"`
}

type kiroImage struct {
	Format string          `json:"format"`
	Source kiroImageSource `json:"source"`
}

type kiroImageSource struct {
	Bytes string `json:"bytes"`
}

type kiroHistoryMessage struct {
	UserInputMessage         *kiroUserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *kiroAssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

type kiroAssistantResponseMessage struct {
	Content  string        `json:"content"`
	ToolUses []kiroToolUse `json:"toolUses,omitempty"`
}

type kiroToolUse struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
}

type kiroInferenceConfig struct {
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
}

type claudeRequest struct {
	Model         string            `json:"model"`
	Messages      []claudeMessage   `json:"messages"`
	MaxTokens     int               `json:"max_tokens"`
	Temperature   *float64          `json:"temperature,omitempty"`
	TopP          *float64          `json:"top_p,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
	System        any               `json:"system,omitempty"`
	StopSequences []string          `json:"stop_sequences,omitempty"`
	ToolChoice    *claudeToolChoice `json:"tool_choice,omitempty"`
	Thinking      *struct {
		Type         string `json:"type,omitempty"`
		BudgetTokens int    `json:"budget_tokens,omitempty"`
	} `json:"thinking,omitempty"`
	Tools []claudeTool `json:"tools,omitempty"`
}

type claudeToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type claudeTool struct {
	Type        string `json:"type,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type claudeContentBlock struct {
	Type     string         `json:"type"`
	Text     string         `json:"text,omitempty"`
	Thinking string         `json:"thinking,omitempty"`
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Input    map[string]any `json:"input,omitempty"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type claudeResponse struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Content      []claudeContentBlock `json:"content"`
	Model        string               `json:"model"`
	StopReason   string               `json:"stop_reason"`
	StopSequence *string              `json:"stop_sequence"`
	Usage        claudeUsage          `json:"usage"`
}

type kiroEvent struct {
	Type    string
	Payload map[string]any
}

type responseAccumulator struct {
	Model                  string
	ID                     string
	Blocks                 []claudeContentBlock
	InputTokens            int
	OutputTokens           int
	StopReason             string
	Credits                float64
	EstimatedInputTokens   int
	ContextUsagePercentage float64
	ToolNames              map[string]string
	pendingTools           pendingToolUses
}

func decodeJSONMap(raw json.RawMessage) map[string]any {
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	return value
}
