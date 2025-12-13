// Package openai provides request translation from OpenAI Chat Completions to Kiro format.
// It handles parsing and transforming OpenAI API requests into the Kiro/Amazon Q API format,
// extracting model information, system instructions, message contents, and tool declarations.
package openai

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
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
func BuildKiroPayloadFromOpenAI(openaiBody []byte, modelID, profileArn, origin string, isAgentic, isChatOnly bool) []byte {
	// Extract max_tokens for potential use in inferenceConfig
	var maxTokens int64
	if mt := gjson.GetBytes(openaiBody, "max_tokens"); mt.Exists() {
		maxTokens = mt.Int()
	}

	// Extract temperature if specified
	var temperature float64
	var hasTemperature bool
	if temp := gjson.GetBytes(openaiBody, "temperature"); temp.Exists() {
		temperature = temp.Float()
		hasTemperature = true
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

	// Convert OpenAI tools to Kiro format
	kiroTools := convertOpenAIToolsToKiro(tools)

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
	var inferenceConfig *KiroInferenceConfig
	if maxTokens > 0 || hasTemperature {
		inferenceConfig = &KiroInferenceConfig{}
		if maxTokens > 0 {
			inferenceConfig.MaxTokens = int(maxTokens)
		}
		if hasTemperature {
			inferenceConfig.Temperature = temperature
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
		return nil
	}

	return result
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
		parameters := fn.Get("parameters").Value()

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

	// Build tool_call_id to name mapping from assistant messages
	toolCallIDToName := make(map[string]string)
	for _, msg := range messagesArray {
		if msg.Get("role").String() == "assistant" {
			toolCalls := msg.Get("tool_calls")
			if toolCalls.IsArray() {
				for _, tc := range toolCalls.Array() {
					if tc.Get("type").String() == "function" {
						id := tc.Get("id").String()
						name := tc.Get("function.name").String()
						if id != "" && name != "" {
							toolCallIDToName[id] = name
						}
					}
				}
			}
		}
	}

	for i, msg := range messagesArray {
		role := msg.Get("role").String()
		isLastMessage := i == len(messagesArray)-1

		switch role {
		case "system":
			// System messages are handled separately via extractSystemPromptFromOpenAI
			continue

		case "user":
			userMsg, toolResults := buildUserMessageFromOpenAI(msg, modelID, origin)
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
			// Process them and merge into the next user message's tool results
			toolCallID := msg.Get("tool_call_id").String()
			content := msg.Get("content").String()

			if toolCallID != "" {
				toolResult := KiroToolResult{
					ToolUseID: toolCallID,
					Content:   []KiroTextContent{{Text: content}},
					Status:    "success",
				}
				// Tool results should be included in the next user message
				// For now, collect them and they'll be handled when we build the current message
				currentToolResults = append(currentToolResults, toolResult)
			}
		}
	}

	return history, currentUserMsg, currentToolResults
}

// buildUserMessageFromOpenAI builds a user message from OpenAI format and extracts tool results
func buildUserMessageFromOpenAI(msg gjson.Result, modelID, origin string) (KiroUserInputMessage, []KiroToolResult) {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolResults []KiroToolResult
	var images []KiroImage

	// Track seen toolCallIds to deduplicate
	seenToolCallIDs := make(map[string]bool)

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

	// Check for tool_calls in the message (shouldn't be in user messages, but handle edge cases)
	_ = seenToolCallIDs // Used for deduplication if needed

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
			if part.Get("type").String() == "text" {
				contentBuilder.WriteString(part.Get("text").String())
			}
		}
	}

	// Handle tool_calls
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

	return KiroAssistantResponseMessage{
		Content:  contentBuilder.String(),
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