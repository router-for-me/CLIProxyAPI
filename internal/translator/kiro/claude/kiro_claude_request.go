// Package claude provides request translation functionality for Claude API to Kiro format.
// It handles parsing and transforming Claude API requests into the Kiro/Amazon Q API format,
// extracting model information, system instructions, message contents, and tool declarations.
package claude

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	TopP        float64 `json:"topP,omitempty"`
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
// headers parameter allows checking Anthropic-Beta header for thinking mode detection.
// metadata parameter is kept for API compatibility but no longer used for thinking configuration.
// Supports thinking mode - when enabled, injects thinking tags into system prompt.
// Returns the payload and a boolean indicating whether thinking mode was injected.
func BuildKiroPayload(claudeBody []byte, modelID, profileArn, origin string, isAgentic, isChatOnly bool, headers http.Header, metadata map[string]any) ([]byte, bool) {
	// Extract max_tokens for potential use in inferenceConfig
	// Handle -1 as "use maximum" (Kiro max output is ~32000 tokens)
	const kiroMaxOutputTokens = 32000
	var maxTokens int64
	if mt := gjson.GetBytes(claudeBody, "max_tokens"); mt.Exists() {
		maxTokens = mt.Int()
		if maxTokens == -1 {
			maxTokens = kiroMaxOutputTokens
			log.Debugf("kiro: max_tokens=-1 converted to %d", kiroMaxOutputTokens)
		}
	}

	// Extract temperature if specified
	var temperature float64
	var hasTemperature bool
	if temp := gjson.GetBytes(claudeBody, "temperature"); temp.Exists() {
		temperature = temp.Float()
		hasTemperature = true
	}

	// Extract top_p if specified
	var topP float64
	var hasTopP bool
	if tp := gjson.GetBytes(claudeBody, "top_p"); tp.Exists() {
		topP = tp.Float()
		hasTopP = true
		log.Debugf("kiro: extracted top_p: %.2f", topP)
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

	// Check for thinking mode using the comprehensive IsThinkingEnabledWithHeaders function
	// This supports Claude API format, OpenAI reasoning_effort, AMP/Cursor format, and Anthropic-Beta header
	thinkingEnabled := IsThinkingEnabledWithHeaders(claudeBody, headers)

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

	// Handle tool_choice parameter - Kiro doesn't support it natively, so we inject system prompt hints
	// Claude tool_choice values: {"type": "auto/any/tool", "name": "..."}
	toolChoiceHint := extractClaudeToolChoiceHint(claudeBody)
	if toolChoiceHint != "" {
		if systemPrompt != "" {
			systemPrompt += "\n"
		}
		systemPrompt += toolChoiceHint
		log.Debugf("kiro: injected tool_choice hint into system prompt")
	}

	// Convert Claude tools to Kiro format
	kiroTools, hasWebSearch := convertClaudeToolsToKiro(tools)

	// If web_search was requested but filtered, inject alternative hint
	if hasWebSearch {
		webSearchHint := `[CRITICAL WEB ACCESS INSTRUCTION]
You have the Fetch/read_url_content tool available. When the user asks about current events, weather, news, or any information that requires web access:
- DO NOT say you cannot search the web
- DO NOT refuse to help with web-related queries
- IMMEDIATELY use the Fetch tool to access relevant URLs
- Use well-known official websites, documentation sites, or API endpoints
- Construct appropriate URLs based on the query context

IMPORTANT: Always attempt to fetch information FIRST before declining. You CAN access the web via Fetch.`
		if systemPrompt != "" {
			systemPrompt += "\n" + webSearchHint
		} else {
			systemPrompt = webSearchHint
		}
		log.Infof("kiro: injected web_search alternative hint (tool was filtered)")
	}

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
		log.Infof("kiro: injected thinking prompt (official mode), has_tools: %v", len(kiroTools) > 0)
	}

	// Process messages and build history
	history, currentUserMsg, currentToolResults := processMessages(messages, modelID, origin)

	// Build content with system prompt (only on first turn to avoid re-injection)
	if currentUserMsg != nil {
		effectiveSystemPrompt := systemPrompt
		if len(history) > 0 {
			effectiveSystemPrompt = "" // Don't re-inject on subsequent turns
		}
		currentUserMsg.Content = buildFinalContent(currentUserMsg.Content, effectiveSystemPrompt, currentToolResults)

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
		log.Debugf("kiro: failed to marshal payload: %v", err)
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
	var budgetTokens int64 = 24000

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

// hasThinkingTagInBody checks if the request body already contains thinking configuration tags.
// This is used to prevent duplicate injection when client (e.g., AMP/Cursor) already includes thinking config.
func hasThinkingTagInBody(body []byte) bool {
	bodyStr := string(body)
	return strings.Contains(bodyStr, "<thinking_mode>") || strings.Contains(bodyStr, "<max_thinking_length>")
}

// IsThinkingEnabledFromHeader checks if thinking mode is enabled via Anthropic-Beta header.
// Claude CLI uses "Anthropic-Beta: interleaved-thinking-2025-05-14" to enable thinking.
func IsThinkingEnabledFromHeader(headers http.Header) bool {
	if headers == nil {
		return false
	}
	betaHeader := headers.Get("Anthropic-Beta")
	if betaHeader == "" {
		return false
	}
	// Check for interleaved-thinking beta feature
	if strings.Contains(betaHeader, "interleaved-thinking") {
		log.Debugf("kiro: thinking mode enabled via Anthropic-Beta header: %s", betaHeader)
		return true
	}
	return false
}

// IsThinkingEnabled is a public wrapper to check if thinking mode is enabled.
// This is used by the executor to determine whether to parse <thinking> tags in responses.
// When thinking is NOT enabled in the request, <thinking> tags in responses should be
// treated as regular text content, not as thinking blocks.
//
// Supports multiple formats:
// - Claude API format: thinking.type = "enabled"
// - OpenAI format: reasoning_effort parameter
// - AMP/Cursor format: <thinking_mode>interleaved</thinking_mode> in system prompt
func IsThinkingEnabled(body []byte) bool {
	return IsThinkingEnabledWithHeaders(body, nil)
}

// IsThinkingEnabledWithHeaders checks if thinking mode is enabled from body or headers.
// This is the comprehensive check that supports all thinking detection methods:
// - Claude API format: thinking.type = "enabled"
// - OpenAI format: reasoning_effort parameter
// - AMP/Cursor format: <thinking_mode>interleaved</thinking_mode> in system prompt
// - Anthropic-Beta header: interleaved-thinking-2025-05-14
func IsThinkingEnabledWithHeaders(body []byte, headers http.Header) bool {
	// Check Anthropic-Beta header first (Claude Code uses this)
	if IsThinkingEnabledFromHeader(headers) {
		return true
	}

	// Check Claude API format first (thinking.type = "enabled")
	enabled, _ := checkThinkingMode(body)
	if enabled {
		log.Debugf("kiro: IsThinkingEnabled returning true (Claude API format)")
		return true
	}

	// Check OpenAI format: reasoning_effort parameter
	// Valid values: "low", "medium", "high", "auto" (not "none")
	reasoningEffort := gjson.GetBytes(body, "reasoning_effort")
	if reasoningEffort.Exists() {
		effort := reasoningEffort.String()
		if effort != "" && effort != "none" {
			log.Debugf("kiro: thinking mode enabled via OpenAI reasoning_effort: %s", effort)
			return true
		}
	}

	// Check AMP/Cursor format: <thinking_mode>interleaved</thinking_mode> in system prompt
	// This is how AMP client passes thinking configuration
	bodyStr := string(body)
	if strings.Contains(bodyStr, "<thinking_mode>") && strings.Contains(bodyStr, "</thinking_mode>") {
		// Extract thinking mode value
		startTag := "<thinking_mode>"
		endTag := "</thinking_mode>"
		startIdx := strings.Index(bodyStr, startTag)
		if startIdx >= 0 {
			startIdx += len(startTag)
			endIdx := strings.Index(bodyStr[startIdx:], endTag)
			if endIdx >= 0 {
				thinkingMode := bodyStr[startIdx : startIdx+endIdx]
				if thinkingMode == "interleaved" || thinkingMode == "enabled" {
					log.Debugf("kiro: thinking mode enabled via AMP/Cursor format: %s", thinkingMode)
					return true
				}
			}
		}
	}

	// Check OpenAI format: max_completion_tokens with reasoning (o1-style)
	// Some clients use this to indicate reasoning mode
	if gjson.GetBytes(body, "max_completion_tokens").Exists() {
		// If max_completion_tokens is set, check if model name suggests reasoning
		model := gjson.GetBytes(body, "model").String()
		if strings.Contains(strings.ToLower(model), "thinking") ||
			strings.Contains(strings.ToLower(model), "reason") {
			log.Debugf("kiro: thinking mode enabled via model name hint: %s", model)
			return true
		}
	}

	log.Debugf("kiro: IsThinkingEnabled returning false (no thinking mode detected)")
	return false
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

// convertClaudeToolsToKiro converts Claude tools to Kiro format.
// Returns the converted tools and a boolean indicating if web_search was filtered.
func convertClaudeToolsToKiro(tools gjson.Result) ([]KiroToolWrapper, bool) {
	var kiroTools []KiroToolWrapper
	hasWebSearch := false
	if !tools.IsArray() {
		return kiroTools, hasWebSearch
	}

	for _, tool := range tools.Array() {
		name := tool.Get("name").String()

		// Filter out web_search/websearch tools (Kiro API doesn't support them)
		// This matches the behavior in AIClient-2-API/claude-kiro.js
		nameLower := strings.ToLower(name)
		if nameLower == "web_search" || nameLower == "websearch" {
			log.Debugf("kiro: skipping unsupported tool: %s", name)
			hasWebSearch = true
			continue
		}

		description := tool.Get("description").String()
		inputSchemaResult := tool.Get("input_schema")
		var inputSchema interface{}
		if inputSchemaResult.Exists() && inputSchemaResult.Type != gjson.Null {
			inputSchema = inputSchemaResult.Value()
		}
		inputSchema = ensureKiroInputSchema(inputSchema)

		// Shorten tool name if it exceeds 64 characters (common with MCP tools)
		originalName := name
		name = shortenToolNameIfNeeded(name)
		if name != originalName {
			log.Debugf("kiro: shortened tool name from '%s' to '%s'", originalName, name)
		}

		// CRITICAL FIX: Kiro API requires non-empty description
		if strings.TrimSpace(description) == "" {
			description = fmt.Sprintf("Tool: %s", name)
			log.Debugf("kiro: tool '%s' has empty description, using default: %s", name, description)
		}

		// Truncate long descriptions (individual tool limit)
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

	// Apply dynamic compression if total tools size exceeds threshold
	// This prevents 500 errors when Claude Code sends too many tools
	kiroTools = compressToolsIfNeeded(kiroTools)

	return kiroTools, hasWebSearch
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

// extractClaudeToolChoiceHint extracts tool_choice from Claude request and returns a system prompt hint.
// Claude tool_choice values:
// - {"type": "auto"}: Model decides (default, no hint needed)
// - {"type": "any"}: Must use at least one tool
// - {"type": "tool", "name": "..."}: Must use specific tool
func extractClaudeToolChoiceHint(claudeBody []byte) string {
	toolChoice := gjson.GetBytes(claudeBody, "tool_choice")
	if !toolChoice.Exists() {
		return ""
	}

	toolChoiceType := toolChoice.Get("type").String()
	switch toolChoiceType {
	case "any":
		return "[INSTRUCTION: You MUST use at least one of the available tools to respond. Do not respond with text only - always make a tool call.]"
	case "tool":
		toolName := toolChoice.Get("name").String()
		if toolName != "" {
			return fmt.Sprintf("[INSTRUCTION: You MUST use the tool named '%s' to respond. Do not use any other tool or respond with text only.]", toolName)
		}
	case "auto":
		// Default behavior, no hint needed
		return ""
	}

	return ""
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
