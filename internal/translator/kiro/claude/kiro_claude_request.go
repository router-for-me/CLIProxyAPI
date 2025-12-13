// Package claude provides request translation functionality for Claude API to Kiro format.
// It handles parsing and transforming Claude API requests into the Kiro/Amazon Q API format,
// extracting model information, system instructions, message contents, and tool declarations.
package claude

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


// Kiro API request structs - field order determines JSON key order

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
	ChatTriggerType string               `json:"chatTriggerType"` // Required: "MANUAL" - must be first field
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

// ConvertClaudeRequestToKiro converts a Claude API request to Kiro format.
// This is the main entry point for request translation.
func ConvertClaudeRequestToKiro(modelName string, inputRawJSON []byte, stream bool) []byte {
	// For Kiro, we pass through the Claude format since buildKiroPayload
	// expects Claude format and does the conversion internally.
	// The actual conversion happens in the executor when building the HTTP request.
	return inputRawJSON
}

// BuildKiroPayload constructs the Kiro API request payload from Claude format.
// Supports tool calling - tools are passed via userInputMessageContext.
// origin parameter determines which quota to use: "CLI" for Amazon Q, "AI_EDITOR" for Kiro IDE.
// isAgentic parameter enables chunked write optimization prompt for -agentic model variants.
// isChatOnly parameter disables tool calling for -chat model variants (pure conversation mode).
// Supports thinking mode - when Claude API thinking parameter is present, injects thinkingHint.
func BuildKiroPayload(claudeBody []byte, modelID, profileArn, origin string, isAgentic, isChatOnly bool) []byte {
	// Extract max_tokens for potential use in inferenceConfig
	var maxTokens int64
	if mt := gjson.GetBytes(claudeBody, "max_tokens"); mt.Exists() {
		maxTokens = mt.Int()
	}

	// Extract temperature if specified
	var temperature float64
	var hasTemperature bool
	if temp := gjson.GetBytes(claudeBody, "temperature"); temp.Exists() {
		temperature = temp.Float()
		hasTemperature = true
	}

	// Normalize origin value for Kiro API compatibility
	origin = normalizeOrigin(origin)
	log.Debugf("kiro: normalized origin value: %s", origin)

	messages := gjson.GetBytes(claudeBody, "messages")

	// For chat-only mode, don't include tools
	var tools gjson.Result
	if !isChatOnly {
		tools = gjson.GetBytes(claudeBody, "tools")
	}

	// Extract system prompt
	systemPrompt := extractSystemPrompt(claudeBody)

	// Check for thinking mode
	thinkingEnabled, budgetTokens := checkThinkingMode(claudeBody)

	// Inject timestamp context
	timestamp := time.Now().Format("2006-01-02 15:04:05 MST")
	timestampContext := fmt.Sprintf("[Context: Current time is %s]", timestamp)
	if systemPrompt != "" {
		systemPrompt = timestampContext + "\n\n" + systemPrompt
	} else {
		systemPrompt = timestampContext
	}
	log.Debugf("kiro: injected timestamp context: %s", timestamp)

	// Inject agentic optimization prompt for -agentic model variants
	if isAgentic {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		systemPrompt += kirocommon.KiroAgenticSystemPrompt
	}

	// Inject thinking hint when thinking mode is enabled
	if thinkingEnabled {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		dynamicThinkingHint := fmt.Sprintf("<thinking_mode>interleaved</thinking_mode><max_thinking_length>%d</max_thinking_length>", budgetTokens)
		systemPrompt += dynamicThinkingHint
		log.Debugf("kiro: injected dynamic thinking hint into system prompt, max_thinking_length: %d", budgetTokens)
	}

	// Convert Claude tools to Kiro format
	kiroTools := convertClaudeToolsToKiro(tools)

	// Process messages and build history
	history, currentUserMsg, currentToolResults := processMessages(messages, modelID, origin)

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
		log.Debugf("kiro: failed to marshal payload: %v", err)
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

// extractSystemPrompt extracts system prompt from Claude request
func extractSystemPrompt(claudeBody []byte) string {
	systemField := gjson.GetBytes(claudeBody, "system")
	if systemField.IsArray() {
		var sb strings.Builder
		for _, block := range systemField.Array() {
			if block.Get("type").String() == "text" {
				sb.WriteString(block.Get("text").String())
			} else if block.Type == gjson.String {
				sb.WriteString(block.String())
			}
		}
		return sb.String()
	}
	return systemField.String()
}

// checkThinkingMode checks if thinking mode is enabled in the Claude request
func checkThinkingMode(claudeBody []byte) (bool, int64) {
	thinkingEnabled := false
	var budgetTokens int64 = 16000

	thinkingField := gjson.GetBytes(claudeBody, "thinking")
	if thinkingField.Exists() {
		thinkingType := thinkingField.Get("type").String()
		if thinkingType == "enabled" {
			thinkingEnabled = true
			if bt := thinkingField.Get("budget_tokens"); bt.Exists() {
				budgetTokens = bt.Int()
				if budgetTokens <= 0 {
					thinkingEnabled = false
					log.Debugf("kiro: thinking mode disabled via budget_tokens <= 0")
				}
			}
			if thinkingEnabled {
				log.Debugf("kiro: thinking mode enabled via Claude API parameter, budget_tokens: %d", budgetTokens)
			}
		}
	}

	return thinkingEnabled, budgetTokens
}

// convertClaudeToolsToKiro converts Claude tools to Kiro format
func convertClaudeToolsToKiro(tools gjson.Result) []KiroToolWrapper {
	var kiroTools []KiroToolWrapper
	if !tools.IsArray() {
		return kiroTools
	}

	for _, tool := range tools.Array() {
		name := tool.Get("name").String()
		description := tool.Get("description").String()
		inputSchema := tool.Get("input_schema").Value()

		// CRITICAL FIX: Kiro API requires non-empty description
		if strings.TrimSpace(description) == "" {
			description = fmt.Sprintf("Tool: %s", name)
			log.Debugf("kiro: tool '%s' has empty description, using default: %s", name, description)
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
				InputSchema: KiroInputSchema{JSON: inputSchema},
			},
		})
	}

	return kiroTools
}

// processMessages processes Claude messages and builds Kiro history
func processMessages(messages gjson.Result, modelID, origin string) ([]KiroHistoryMessage, *KiroUserInputMessage, []KiroToolResult) {
	var history []KiroHistoryMessage
	var currentUserMsg *KiroUserInputMessage
	var currentToolResults []KiroToolResult

	// Merge adjacent messages with the same role
	messagesArray := kirocommon.MergeAdjacentMessages(messages.Array())
	for i, msg := range messagesArray {
		role := msg.Get("role").String()
		isLastMessage := i == len(messagesArray)-1

		if role == "user" {
			userMsg, toolResults := BuildUserMessageStruct(msg, modelID, origin)
			if isLastMessage {
				currentUserMsg = &userMsg
				currentToolResults = toolResults
			} else {
				// CRITICAL: Kiro API requires content to be non-empty for history messages too
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
		} else if role == "assistant" {
			assistantMsg := BuildAssistantMessageStruct(msg)
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
		}
	}

	return history, currentUserMsg, currentToolResults
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
		log.Debugf("kiro: content was empty, using default: %s", finalContent)
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
			log.Debugf("kiro: skipping duplicate toolResult in currentMessage: %s", tr.ToolUseID)
		}
	}
	return unique
}

// BuildUserMessageStruct builds a user message and extracts tool results
func BuildUserMessageStruct(msg gjson.Result, modelID, origin string) (KiroUserInputMessage, []KiroToolResult) {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolResults []KiroToolResult
	var images []KiroImage

	// Track seen toolUseIds to deduplicate
	seenToolUseIDs := make(map[string]bool)

	if content.IsArray() {
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				contentBuilder.WriteString(part.Get("text").String())
			case "image":
				mediaType := part.Get("source.media_type").String()
				data := part.Get("source.data").String()

				format := ""
				if idx := strings.LastIndex(mediaType, "/"); idx != -1 {
					format = mediaType[idx+1:]
				}

				if format != "" && data != "" {
					images = append(images, KiroImage{
						Format: format,
						Source: KiroImageSource{
							Bytes: data,
						},
					})
				}
			case "tool_result":
				toolUseID := part.Get("tool_use_id").String()

				// Skip duplicate toolUseIds
				if seenToolUseIDs[toolUseID] {
					log.Debugf("kiro: skipping duplicate tool_result with toolUseId: %s", toolUseID)
					continue
				}
				seenToolUseIDs[toolUseID] = true

				isError := part.Get("is_error").Bool()
				resultContent := part.Get("content")

				var textContents []KiroTextContent
				if resultContent.IsArray() {
					for _, item := range resultContent.Array() {
						if item.Get("type").String() == "text" {
							textContents = append(textContents, KiroTextContent{Text: item.Get("text").String()})
						} else if item.Type == gjson.String {
							textContents = append(textContents, KiroTextContent{Text: item.String()})
						}
					}
				} else if resultContent.Type == gjson.String {
					textContents = append(textContents, KiroTextContent{Text: resultContent.String()})
				}

				if len(textContents) == 0 {
					textContents = append(textContents, KiroTextContent{Text: "Tool use was cancelled by the user"})
				}

				status := "success"
				if isError {
					status = "error"
				}

				toolResults = append(toolResults, KiroToolResult{
					ToolUseID: toolUseID,
					Content:   textContents,
					Status:    status,
				})
			}
		}
	} else {
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

// BuildAssistantMessageStruct builds an assistant message with tool uses
func BuildAssistantMessageStruct(msg gjson.Result) KiroAssistantResponseMessage {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolUses []KiroToolUse

	if content.IsArray() {
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				contentBuilder.WriteString(part.Get("text").String())
			case "tool_use":
				toolUseID := part.Get("id").String()
				toolName := part.Get("name").String()
				toolInput := part.Get("input")

				var inputMap map[string]interface{}
				if toolInput.IsObject() {
					inputMap = make(map[string]interface{})
					toolInput.ForEach(func(key, value gjson.Result) bool {
						inputMap[key.String()] = value.Value()
						return true
					})
				}

				toolUses = append(toolUses, KiroToolUse{
					ToolUseID: toolUseID,
					Name:      toolName,
					Input:     inputMap,
				})
			}
		}
	} else {
		contentBuilder.WriteString(content.String())
	}

	return KiroAssistantResponseMessage{
		Content:  contentBuilder.String(),
		ToolUses: toolUses,
	}
}