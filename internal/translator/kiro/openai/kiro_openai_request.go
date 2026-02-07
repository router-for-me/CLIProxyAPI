// Package openai provides request translation from OpenAI Chat Completions to Kiro format.
// It handles parsing and transforming OpenAI API requests into the Kiro/Amazon Q API format,
// extracting model information, system instructions, message contents, and tool declarations.
package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	kiroclaude "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/claude"
	kirocommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/common"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Kiro API request structs - reuse from kiroclaude package structure

// KiroPayload is the top-level request structure for Kiro API
type KiroPayload struct {
	ConversationState KiroConversationState `json:"conversationState"`
	ProfileArn        string                `json:"profileArn,omitempty"`
	InferenceConfig   *KiroInferenceConfig  `json:"inferenceConfig,omitempty"`
}

// KiroInferenceConfig contains inference parameters for the Kiro API.
type KiroInferenceConfig struct {
	MaxTokens   int     `json:"maxTokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"topP,omitempty"`
}

// KiroConversationState holds the conversation context
type KiroConversationState struct {
	ChatTriggerType string               `json:"chatTriggerType"` // Required: "MANUAL"
	ConversationID  string               `json:"conversationId"`
	CurrentMessage  KiroCurrentMessage   `json:"currentMessage"`
	History         []KiroHistoryMessage `json:"history,omitempty"`
}

// KiroCurrentMessage wraps the current user message
type KiroCurrentMessage struct {
	UserInputMessage KiroUserInputMessage `json:"userInputMessage"`
}

// KiroHistoryMessage represents a message in the conversation history
type KiroHistoryMessage struct {
	UserInputMessage         *KiroUserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *KiroAssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

// KiroImage represents an image in Kiro API format
type KiroImage struct {
	Format string          `json:"format"`
	Source KiroImageSource `json:"source"`
}

// KiroImageSource contains the image data
type KiroImageSource struct {
	Bytes string `json:"bytes"` // base64 encoded image data
}

// KiroUserInputMessage represents a user message
type KiroUserInputMessage struct {
	Content                 string                       `json:"content"`
	ModelID                 string                       `json:"modelId"`
	Origin                  string                       `json:"origin"`
	Images                  []KiroImage                  `json:"images,omitempty"`
	UserInputMessageContext *KiroUserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// KiroUserInputMessageContext contains tool-related context
type KiroUserInputMessageContext struct {
	ToolResults []KiroToolResult  `json:"toolResults,omitempty"`
	Tools       []KiroToolWrapper `json:"tools,omitempty"`
}

// KiroToolResult represents a tool execution result
type KiroToolResult struct {
	Content   []KiroTextContent `json:"content"`
	Status    string            `json:"status"`
	ToolUseID string            `json:"toolUseId"`
}

// KiroTextContent represents text content
type KiroTextContent struct {
	Text string `json:"text"`
}

// KiroToolWrapper wraps a tool specification
type KiroToolWrapper struct {
	ToolSpecification KiroToolSpecification `json:"toolSpecification"`
}

// KiroToolSpecification defines a tool's schema
type KiroToolSpecification struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema KiroInputSchema `json:"inputSchema"`
}

// KiroInputSchema wraps the JSON schema for tool input
type KiroInputSchema struct {
	JSON interface{} `json:"json"`
}

// KiroAssistantResponseMessage represents an assistant message
type KiroAssistantResponseMessage struct {
	Content  string        `json:"content"`
	ToolUses []KiroToolUse `json:"toolUses,omitempty"`
}

// KiroToolUse represents a tool invocation by the assistant
type KiroToolUse struct {
	ToolUseID string                 `json:"toolUseId"`
	Name      string                 `json:"name"`
	Input     map[string]interface{} `json:"input"`
}

// ConvertOpenAIRequestToKiro converts an OpenAI Chat Completions request to Kiro format.
// This is the main entry point for request translation.
// Note: The actual payload building happens in the executor, this just passes through
// the OpenAI format which will be converted by BuildKiroPayloadFromOpenAI.
func ConvertOpenAIRequestToKiro(modelName string, inputRawJSON []byte, stream bool) []byte {
	// Pass through the OpenAI format - actual conversion happens in BuildKiroPayloadFromOpenAI
	return inputRawJSON
}

// BuildKiroPayloadFromOpenAI constructs the Kiro API request payload from OpenAI format.
// Supports tool calling - tools are passed via userInputMessageContext.
// origin parameter determines which quota to use: "CLI" for Amazon Q, "AI_EDITOR" for Kiro IDE.
// isAgentic parameter enables chunked write optimization prompt for -agentic model variants.
// isChatOnly parameter disables tool calling for -chat model variants (pure conversation mode).
// headers parameter allows checking Anthropic-Beta header for thinking mode detection.
// metadata parameter is kept for API compatibility but no longer used for thinking configuration.
// Returns the payload and a boolean indicating whether thinking mode was injected.
func BuildKiroPayloadFromOpenAI(openaiBody []byte, modelID, profileArn, origin string, isAgentic, isChatOnly bool, headers http.Header, metadata map[string]any) ([]byte, bool) {
	// Extract max_tokens for potential use in inferenceConfig
	// Handle -1 as "use maximum" (Kiro max output is ~32000 tokens)
	const kiroMaxOutputTokens = 32000
	var maxTokens int64
	if mt := gjson.GetBytes(openaiBody, "max_tokens"); mt.Exists() {
		maxTokens = mt.Int()
		if maxTokens == -1 {
			maxTokens = kiroMaxOutputTokens
			log.Debugf("kiro-openai: max_tokens=-1 converted to %d", kiroMaxOutputTokens)
		}
	}

	// Extract temperature if specified
	var temperature float64
	var hasTemperature bool
	if temp := gjson.GetBytes(openaiBody, "temperature"); temp.Exists() {
		temperature = temp.Float()
		hasTemperature = true
	}

	// Extract top_p if specified
	var topP float64
	var hasTopP bool
	if tp := gjson.GetBytes(openaiBody, "top_p"); tp.Exists() {
		topP = tp.Float()
		hasTopP = true
		log.Debugf("kiro-openai: extracted top_p: %.2f", topP)
	}

	// Normalize origin value for Kiro API compatibility
	origin = normalizeOrigin(origin)
	log.Debugf("kiro-openai: normalized origin value: %s", origin)

	messages := gjson.GetBytes(openaiBody, "messages")

	// For chat-only mode, don't include tools
	var tools gjson.Result
	if !isChatOnly {
		tools = gjson.GetBytes(openaiBody, "tools")
	}

	// Extract system prompt from messages
	systemPrompt := extractSystemPromptFromOpenAI(messages)

	// Inject timestamp context
	timestamp := time.Now().Format("2006-01-02 15:04:05 MST")
	timestampContext := fmt.Sprintf("[Context: Current time is %s]", timestamp)
	if systemPrompt != "" {
		systemPrompt = timestampContext + "\n\n" + systemPrompt
	} else {
		systemPrompt = timestampContext
	}
	log.Debugf("kiro-openai: injected timestamp context: %s", timestamp)

	// Inject agentic optimization prompt for -agentic model variants
	if isAgentic {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		systemPrompt += kirocommon.KiroAgenticSystemPrompt
	}

	// Handle tool_choice parameter - Kiro doesn't support it natively, so we inject system prompt hints
	// OpenAI tool_choice values: "none", "auto", "required", or {"type":"function","function":{"name":"..."}}
	toolChoiceHint := extractToolChoiceHint(openaiBody)
	if toolChoiceHint != "" {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		systemPrompt += toolChoiceHint
		log.Debugf("kiro-openai: injected tool_choice hint into system prompt")
	}

	// Handle response_format parameter - Kiro doesn't support it natively, so we inject system prompt hints
	// OpenAI response_format: {"type": "json_object"} or {"type": "json_schema", "json_schema": {...}}
	responseFormatHint := extractResponseFormatHint(openaiBody)
	if responseFormatHint != "" {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		systemPrompt += responseFormatHint
		log.Debugf("kiro-openai: injected response_format hint into system prompt")
	}

	// Check for thinking mode
	// Supports OpenAI reasoning_effort parameter, model name hints, and Anthropic-Beta header
	thinkingEnabled := checkThinkingModeFromOpenAIWithHeaders(openaiBody, headers)

	// Convert OpenAI tools to Kiro format
	kiroTools := convertOpenAIToolsToKiro(tools)

	// Thinking mode implementation:
	// Kiro API supports official thinking/reasoning mode via <thinking_mode> tag.
	// When set to "enabled", Kiro returns reasoning content as official reasoningContentEvent
	// rather than inline <thinking> tags in assistantResponseEvent.
	// We use a high max_thinking_length to allow extensive reasoning.
	if thinkingEnabled {
		thinkingHint := `<thinking_mode>enabled</thinking_mode>
<max_thinking_length>200000</max_thinking_length>`
		if systemPrompt != "" {
			systemPrompt = thinkingHint + "\n\n" + systemPrompt
		} else {
			systemPrompt = thinkingHint
		}
		log.Debugf("kiro-openai: injected thinking prompt (official mode)")
	}

	// Process messages and build history
	history, currentUserMsg, currentToolResults := processOpenAIMessages(messages, modelID, origin)

	// Build content with system prompt
	if currentUserMsg != nil {
		currentUserMsg.Content = buildFinalContent(currentUserMsg.Content, systemPrompt, currentToolResults)

		// Deduplicate currentToolResults
		currentToolResults = deduplicateToolResults(currentToolResults)

		// Build userInputMessageContext with tools and tool results
		if len(kiroTools) > 0 || len(currentToolResults) > 0 {
			currentUserMsg.UserInputMessageContext = &KiroUserInputMessageContext{
				Tools:       kiroTools,
				ToolResults: currentToolResults,
			}
		}
	}

	// Build payload
	var currentMessage KiroCurrentMessage
	if currentUserMsg != nil {
		currentMessage = KiroCurrentMessage{UserInputMessage: *currentUserMsg}
	} else {
		fallbackContent := ""
		if systemPrompt != "" {
			fallbackContent = "--- SYSTEM PROMPT ---\n" + systemPrompt + "\n--- END SYSTEM PROMPT ---\n"
		}
		currentMessage = KiroCurrentMessage{UserInputMessage: KiroUserInputMessage{
			Content: fallbackContent,
			ModelID: modelID,
			Origin:  origin,
		}}
	}

	// Build inferenceConfig if we have any inference parameters
	// Note: Kiro API doesn't actually use max_tokens for thinking budget
	var inferenceConfig *KiroInferenceConfig
	if maxTokens > 0 || hasTemperature || hasTopP {
		inferenceConfig = &KiroInferenceConfig{}
		if maxTokens > 0 {
			inferenceConfig.MaxTokens = int(maxTokens)
		}
		if hasTemperature {
			inferenceConfig.Temperature = temperature
		}
		if hasTopP {
			inferenceConfig.TopP = topP
		}
	}

	payload := KiroPayload{
		ConversationState: KiroConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  uuid.New().String(),
			CurrentMessage:  currentMessage,
			History:         history,
		},
		ProfileArn:      profileArn,
		InferenceConfig: inferenceConfig,
	}

	result, err := json.Marshal(payload)
	if err != nil {
		log.Debugf("kiro-openai: failed to marshal payload: %v", err)
		return nil, false
	}

	return result, thinkingEnabled
}

// normalizeOrigin normalizes origin value for Kiro API compatibility
func normalizeOrigin(origin string) string {
	switch origin {
	case "KIRO_CLI":
		return "CLI"
	case "KIRO_AI_EDITOR":
		return "AI_EDITOR"
	case "AMAZON_Q":
		return "CLI"
	case "KIRO_IDE":
		return "AI_EDITOR"
	default:
		return origin
	}
}

// extractSystemPromptFromOpenAI extracts system prompt from OpenAI messages
func extractSystemPromptFromOpenAI(messages gjson.Result) string {
	if !messages.IsArray() {
		return ""
	}

	var systemParts []string
	for _, msg := range messages.Array() {
		if msg.Get("role").String() == "system" {
			content := msg.Get("content")
			if content.Type == gjson.String {
				systemParts = append(systemParts, content.String())
			} else if content.IsArray() {
				// Handle array content format
				for _, part := range content.Array() {
					if part.Get("type").String() == "text" {
						systemParts = append(systemParts, part.Get("text").String())
					}
				}
			}
		}
	}

	return strings.Join(systemParts, "\n")
}

// shortenToolNameIfNeeded shortens tool names that exceed 64 characters.
// MCP tools often have long names like "mcp__server-name__tool-name".
// This preserves the "mcp__" prefix and last segment when possible.
func shortenToolNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	// For MCP tools, try to preserve prefix and last segment
	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			cand := "mcp__" + name[idx+2:]
			if len(cand) > limit {
				return cand[:limit]
			}
			return cand
		}
	}
	return name[:limit]
}

func ensureKiroInputSchema(parameters interface{}) interface{} {
	if parameters != nil {
		return parameters
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

// convertOpenAIToolsToKiro converts OpenAI tools to Kiro format
func convertOpenAIToolsToKiro(tools gjson.Result) []KiroToolWrapper {
	var kiroTools []KiroToolWrapper
	if !tools.IsArray() {
		return kiroTools
	}

	for _, tool := range tools.Array() {
		// OpenAI tools have type "function" with function definition inside
		if tool.Get("type").String() != "function" {
			continue
		}

		fn := tool.Get("function")
		if !fn.Exists() {
			continue
		}

		name := fn.Get("name").String()
		description := fn.Get("description").String()
		parametersResult := fn.Get("parameters")
		var parameters interface{}
		if parametersResult.Exists() && parametersResult.Type != gjson.Null {
			parameters = parametersResult.Value()
		}
		parameters = ensureKiroInputSchema(parameters)

		// Shorten tool name if it exceeds 64 characters (common with MCP tools)
		originalName := name
		name = shortenToolNameIfNeeded(name)
		if name != originalName {
			log.Debugf("kiro-openai: shortened tool name from '%s' to '%s'", originalName, name)
		}

		// CRITICAL FIX: Kiro API requires non-empty description
		if strings.TrimSpace(description) == "" {
			description = fmt.Sprintf("Tool: %s", name)
			log.Debugf("kiro-openai: tool '%s' has empty description, using default: %s", name, description)
		}

		// Truncate long descriptions
		if len(description) > kirocommon.KiroMaxToolDescLen {
			truncLen := kirocommon.KiroMaxToolDescLen - 30
			for truncLen > 0 && !utf8.RuneStart(description[truncLen]) {
				truncLen--
			}
			description = description[:truncLen] + "... (description truncated)"
		}

		kiroTools = append(kiroTools, KiroToolWrapper{
			ToolSpecification: KiroToolSpecification{
				Name:        name,
				Description: description,
				InputSchema: KiroInputSchema{JSON: parameters},
			},
		})
	}

	return kiroTools
}

// processOpenAIMessages processes OpenAI messages and builds Kiro history
func processOpenAIMessages(messages gjson.Result, modelID, origin string) ([]KiroHistoryMessage, *KiroUserInputMessage, []KiroToolResult) {
	var history []KiroHistoryMessage
	var currentUserMsg *KiroUserInputMessage
	var currentToolResults []KiroToolResult

	if !messages.IsArray() {
		return history, currentUserMsg, currentToolResults
	}

	// Merge adjacent messages with the same role
	messagesArray := kirocommon.MergeAdjacentMessages(messages.Array())

	// Track pending tool results that should be attached to the next user message
	// This is critical for LiteLLM-translated requests where tool results appear
	// as separate "tool" role messages between assistant and user messages
	var pendingToolResults []KiroToolResult

	for i, msg := range messagesArray {
		role := msg.Get("role").String()
		isLastMessage := i == len(messagesArray)-1

		switch role {
		case "system":
			// System messages are handled separately via extractSystemPromptFromOpenAI
			continue

		case "user":
			userMsg, toolResults := buildUserMessageFromOpenAI(msg, modelID, origin)
			// Merge any pending tool results from preceding "tool" role messages
			toolResults = append(pendingToolResults, toolResults...)
			pendingToolResults = nil // Reset pending tool results

			if isLastMessage {
				currentUserMsg = &userMsg
				currentToolResults = toolResults
			} else {
				// CRITICAL: Kiro API requires content to be non-empty for history messages
				if strings.TrimSpace(userMsg.Content) == "" {
					if len(toolResults) > 0 {
						userMsg.Content = "Tool results provided."
					} else {
						userMsg.Content = "Continue"
					}
				}
				// For history messages, embed tool results in context
				if len(toolResults) > 0 {
					userMsg.UserInputMessageContext = &KiroUserInputMessageContext{
						ToolResults: toolResults,
					}
				}
				history = append(history, KiroHistoryMessage{
					UserInputMessage: &userMsg,
				})
			}

		case "assistant":
			assistantMsg := buildAssistantMessageFromOpenAI(msg)

			// If there are pending tool results, we need to insert a synthetic user message
			// before this assistant message to maintain proper conversation structure
			if len(pendingToolResults) > 0 {
				syntheticUserMsg := KiroUserInputMessage{
					Content: "Tool results provided.",
					ModelID: modelID,
					Origin:  origin,
					UserInputMessageContext: &KiroUserInputMessageContext{
						ToolResults: pendingToolResults,
					},
				}
				history = append(history, KiroHistoryMessage{
					UserInputMessage: &syntheticUserMsg,
				})
				pendingToolResults = nil
			}

			if isLastMessage {
				history = append(history, KiroHistoryMessage{
					AssistantResponseMessage: &assistantMsg,
				})
				// Create a "Continue" user message as currentMessage
				currentUserMsg = &KiroUserInputMessage{
					Content: "Continue",
					ModelID: modelID,
					Origin:  origin,
				}
			} else {
				history = append(history, KiroHistoryMessage{
					AssistantResponseMessage: &assistantMsg,
				})
			}

		case "tool":
			// Tool messages in OpenAI format provide results for tool_calls
			// These are typically followed by user or assistant messages
			// Collect them as pending and attach to the next user message
			toolCallID := msg.Get("tool_call_id").String()
			content := msg.Get("content").String()

			if toolCallID != "" {
				toolResult := KiroToolResult{
					ToolUseID: toolCallID,
					Content:   []KiroTextContent{{Text: content}},
					Status:    "success",
				}
				// Collect pending tool results to attach to the next user message
				pendingToolResults = append(pendingToolResults, toolResult)
			}
		}
	}

	// Handle case where tool results are at the end with no following user message
	if len(pendingToolResults) > 0 {
		currentToolResults = append(currentToolResults, pendingToolResults...)
		// If there's no current user message, create a synthetic one for the tool results
		if currentUserMsg == nil {
			currentUserMsg = &KiroUserInputMessage{
				Content: "Tool results provided.",
				ModelID: modelID,
				Origin:  origin,
			}
		}
	}

	// Truncate history if too long to prevent Kiro API errors
	history = truncateHistoryIfNeeded(history)

	return history, currentUserMsg, currentToolResults
}

const kiroMaxHistoryMessages = 50

func truncateHistoryIfNeeded(history []KiroHistoryMessage) []KiroHistoryMessage {
	if len(history) <= kiroMaxHistoryMessages {
		return history
	}

	log.Debugf("kiro-openai: truncating history from %d to %d messages", len(history), kiroMaxHistoryMessages)
	return history[len(history)-kiroMaxHistoryMessages:]
}

// buildUserMessageFromOpenAI builds a user message from OpenAI format and extracts tool results
func buildUserMessageFromOpenAI(msg gjson.Result, modelID, origin string) (KiroUserInputMessage, []KiroToolResult) {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolResults []KiroToolResult
	var images []KiroImage

	if content.IsArray() {
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				contentBuilder.WriteString(part.Get("text").String())
			case "image_url":
				imageURL := part.Get("image_url.url").String()
				if strings.HasPrefix(imageURL, "data:") {
					// Parse data URL: data:image/png;base64,xxxxx
					if idx := strings.Index(imageURL, ";base64,"); idx != -1 {
						mediaType := imageURL[5:idx] // Skip "data:"
						data := imageURL[idx+8:]     // Skip ";base64,"

						format := ""
						if lastSlash := strings.LastIndex(mediaType, "/"); lastSlash != -1 {
							format = mediaType[lastSlash+1:]
						}

						if format != "" && data != "" {
							images = append(images, KiroImage{
								Format: format,
								Source: KiroImageSource{
									Bytes: data,
								},
							})
						}
					}
				}
			}
		}
	} else if content.Type == gjson.String {
		contentBuilder.WriteString(content.String())
	}

	userMsg := KiroUserInputMessage{
		Content: contentBuilder.String(),
		ModelID: modelID,
		Origin:  origin,
	}

	if len(images) > 0 {
		userMsg.Images = images
	}

	return userMsg, toolResults
}

// buildAssistantMessageFromOpenAI builds an assistant message from OpenAI format
func buildAssistantMessageFromOpenAI(msg gjson.Result) KiroAssistantResponseMessage {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolUses []KiroToolUse

	// Handle content
	if content.Type == gjson.String {
		contentBuilder.WriteString(content.String())
	} else if content.IsArray() {
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				contentBuilder.WriteString(part.Get("text").String())
			case "tool_use":
				// Handle tool_use in content array (Anthropic/OpenCode format)
				// This is different from OpenAI's tool_calls format
				toolUseID := part.Get("id").String()
				toolName := part.Get("name").String()
				inputData := part.Get("input")

				inputMap := make(map[string]interface{})
				if inputData.Exists() && inputData.IsObject() {
					inputData.ForEach(func(key, value gjson.Result) bool {
						inputMap[key.String()] = value.Value()
						return true
					})
				}

				toolUses = append(toolUses, KiroToolUse{
					ToolUseID: toolUseID,
					Name:      toolName,
					Input:     inputMap,
				})
				log.Debugf("kiro-openai: extracted tool_use from content array: %s", toolName)
			}
		}
	}

	// Handle tool_calls (OpenAI format)
	toolCalls := msg.Get("tool_calls")
	if toolCalls.IsArray() {
		for _, tc := range toolCalls.Array() {
			if tc.Get("type").String() != "function" {
				continue
			}

			toolUseID := tc.Get("id").String()
			toolName := tc.Get("function.name").String()
			toolArgs := tc.Get("function.arguments").String()

			var inputMap map[string]interface{}
			if err := json.Unmarshal([]byte(toolArgs), &inputMap); err != nil {
				log.Debugf("kiro-openai: failed to parse tool arguments: %v", err)
				inputMap = make(map[string]interface{})
			}

			toolUses = append(toolUses, KiroToolUse{
				ToolUseID: toolUseID,
				Name:      toolName,
				Input:     inputMap,
			})
		}
	}

	// CRITICAL FIX: Kiro API requires non-empty content for assistant messages
	// This can happen with compaction requests or error recovery scenarios
	finalContent := contentBuilder.String()
	if strings.TrimSpace(finalContent) == "" {
		if len(toolUses) > 0 {
			finalContent = kirocommon.DefaultAssistantContentWithTools
		} else {
			finalContent = kirocommon.DefaultAssistantContent
		}
		log.Debugf("kiro-openai: assistant content was empty, using default: %s", finalContent)
	}

	return KiroAssistantResponseMessage{
		Content:  finalContent,
		ToolUses: toolUses,
	}
}

// buildFinalContent builds the final content with system prompt
func buildFinalContent(content, systemPrompt string, toolResults []KiroToolResult) string {
	var contentBuilder strings.Builder

	if systemPrompt != "" {
		contentBuilder.WriteString("--- SYSTEM PROMPT ---\n")
		contentBuilder.WriteString(systemPrompt)
		contentBuilder.WriteString("\n--- END SYSTEM PROMPT ---\n\n")
	}

	contentBuilder.WriteString(content)
	finalContent := contentBuilder.String()

	// CRITICAL: Kiro API requires content to be non-empty
	if strings.TrimSpace(finalContent) == "" {
		if len(toolResults) > 0 {
			finalContent = "Tool results provided."
		} else {
			finalContent = "Continue"
		}
		log.Debugf("kiro-openai: content was empty, using default: %s", finalContent)
	}

	return finalContent
}

// checkThinkingModeFromOpenAI checks if thinking mode is enabled in the OpenAI request.
// Returns thinkingEnabled.
// Supports:
// - reasoning_effort parameter (low/medium/high/auto)
// - Model name containing "thinking" or "reason"
// - <thinking_mode> tag in system prompt (AMP/Cursor format)
func checkThinkingModeFromOpenAI(openaiBody []byte) bool {
	return checkThinkingModeFromOpenAIWithHeaders(openaiBody, nil)
}

// checkThinkingModeFromOpenAIWithHeaders checks if thinking mode is enabled in the OpenAI request.
// Returns thinkingEnabled.
// Supports:
// - Anthropic-Beta header with interleaved-thinking (Claude CLI)
// - reasoning_effort parameter (low/medium/high/auto)
// - Model name containing "thinking" or "reason"
// - <thinking_mode> tag in system prompt (AMP/Cursor format)
func checkThinkingModeFromOpenAIWithHeaders(openaiBody []byte, headers http.Header) bool {
	// Check Anthropic-Beta header first (Claude CLI uses this)
	if kiroclaude.IsThinkingEnabledFromHeader(headers) {
		log.Debugf("kiro-openai: thinking mode enabled via Anthropic-Beta header")
		return true
	}

	// Check OpenAI format: reasoning_effort parameter
	// Valid values: "low", "medium", "high", "auto" (not "none")
	reasoningEffort := gjson.GetBytes(openaiBody, "reasoning_effort")
	if reasoningEffort.Exists() {
		effort := reasoningEffort.String()
		if effort != "" && effort != "none" {
			log.Debugf("kiro-openai: thinking mode enabled via reasoning_effort: %s", effort)
			return true
		}
	}

	// Check AMP/Cursor format: <thinking_mode>interleaved</thinking_mode> in system prompt
	bodyStr := string(openaiBody)
	if strings.Contains(bodyStr, "<thinking_mode>") && strings.Contains(bodyStr, "</thinking_mode>") {
		startTag := "<thinking_mode>"
		endTag := "</thinking_mode>"
		startIdx := strings.Index(bodyStr, startTag)
		if startIdx >= 0 {
			startIdx += len(startTag)
			endIdx := strings.Index(bodyStr[startIdx:], endTag)
			if endIdx >= 0 {
				thinkingMode := bodyStr[startIdx : startIdx+endIdx]
				if thinkingMode == "interleaved" || thinkingMode == "enabled" {
					log.Debugf("kiro-openai: thinking mode enabled via AMP/Cursor format: %s", thinkingMode)
					return true
				}
			}
		}
	}

	// Check model name for thinking hints
	model := gjson.GetBytes(openaiBody, "model").String()
	modelLower := strings.ToLower(model)
	if strings.Contains(modelLower, "thinking") || strings.Contains(modelLower, "-reason") {
		log.Debugf("kiro-openai: thinking mode enabled via model name hint: %s", model)
		return true
	}

	log.Debugf("kiro-openai: no thinking mode detected in OpenAI request")
	return false
}

// hasThinkingTagInBody checks if the request body already contains thinking configuration tags.
// This is used to prevent duplicate injection when client (e.g., AMP/Cursor) already includes thinking config.
func hasThinkingTagInBody(body []byte) bool {
	bodyStr := string(body)
	return strings.Contains(bodyStr, "<thinking_mode>") || strings.Contains(bodyStr, "<max_thinking_length>")
}


// extractToolChoiceHint extracts tool_choice from OpenAI request and returns a system prompt hint.
// OpenAI tool_choice values:
// - "none": Don't use any tools
// - "auto": Model decides (default, no hint needed)
// - "required": Must use at least one tool
// - {"type":"function","function":{"name":"..."}} : Must use specific tool
func extractToolChoiceHint(openaiBody []byte) string {
	toolChoice := gjson.GetBytes(openaiBody, "tool_choice")
	if !toolChoice.Exists() {
		return ""
	}

	// Handle string values
	if toolChoice.Type == gjson.String {
		switch toolChoice.String() {
		case "none":
			// Note: When tool_choice is "none", we should ideally not pass tools at all
			// But since we can't modify tool passing here, we add a strong hint
			return "[INSTRUCTION: Do NOT use any tools. Respond with text only.]"
		case "required":
			return "[INSTRUCTION: You MUST use at least one of the available tools to respond. Do not respond with text only - always make a tool call.]"
		case "auto":
			// Default behavior, no hint needed
			return ""
		}
	}

	// Handle object value: {"type":"function","function":{"name":"..."}}
	if toolChoice.IsObject() {
		if toolChoice.Get("type").String() == "function" {
			toolName := toolChoice.Get("function.name").String()
			if toolName != "" {
				return fmt.Sprintf("[INSTRUCTION: You MUST use the tool named '%s' to respond. Do not use any other tool or respond with text only.]", toolName)
			}
		}
	}

	return ""
}

// extractResponseFormatHint extracts response_format from OpenAI request and returns a system prompt hint.
// OpenAI response_format values:
// - {"type": "text"}: Default, no hint needed
// - {"type": "json_object"}: Must respond with valid JSON
// - {"type": "json_schema", "json_schema": {...}}: Must respond with JSON matching schema
func extractResponseFormatHint(openaiBody []byte) string {
	responseFormat := gjson.GetBytes(openaiBody, "response_format")
	if !responseFormat.Exists() {
		return ""
	}

	formatType := responseFormat.Get("type").String()
	switch formatType {
	case "json_object":
		return "[INSTRUCTION: You MUST respond with valid JSON only. Do not include any text before or after the JSON. Do not wrap the JSON in markdown code blocks. Output raw JSON directly.]"
	case "json_schema":
		// Extract schema if provided
		schema := responseFormat.Get("json_schema.schema")
		if schema.Exists() {
			schemaStr := schema.Raw
			// Truncate if too long
			if len(schemaStr) > 500 {
				schemaStr = schemaStr[:500] + "..."
			}
			return fmt.Sprintf("[INSTRUCTION: You MUST respond with valid JSON that matches this schema: %s. Do not include any text before or after the JSON. Do not wrap the JSON in markdown code blocks. Output raw JSON directly.]", schemaStr)
		}
		return "[INSTRUCTION: You MUST respond with valid JSON only. Do not include any text before or after the JSON. Do not wrap the JSON in markdown code blocks. Output raw JSON directly.]"
	case "text":
		// Default behavior, no hint needed
		return ""
	}

	return ""
}

// deduplicateToolResults removes duplicate tool results
func deduplicateToolResults(toolResults []KiroToolResult) []KiroToolResult {
	if len(toolResults) == 0 {
		return toolResults
	}

	seenIDs := make(map[string]bool)
	unique := make([]KiroToolResult, 0, len(toolResults))
	for _, tr := range toolResults {
		if !seenIDs[tr.ToolUseID] {
			seenIDs[tr.ToolUseID] = true
			unique = append(unique, tr)
		} else {
			log.Debugf("kiro-openai: skipping duplicate toolResult: %s", tr.ToolUseID)
		}
	}
	return unique
}
