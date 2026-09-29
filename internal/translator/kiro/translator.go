// Package kiro translates standard OpenAI/Claude requests into Kiro / CodeWhisperer conversation payload formats.
package kiro

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const (
	minimalFallbackUserContent    = "."
	toolResultsContinuationPrefix = "Tool results:"
	toolResultImagePlaceholder    = "[Tool returned an image; the image is attached to this message.]"
	maxToolDescLen                = 10237
	maxPayloadBytes               = 900 * 1024
	truncationPlaceholder         = "[Earlier conversation history was truncated to fit the model's input limit. Older messages and tool activity have been omitted.]"
	minRecentHistoryTurns         = 4
)

// KiroUserMessage represents a user message in Kiro payload.
type KiroUserMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId,omitempty"`
	Origin                  string                   `json:"origin,omitempty"`
	Images                  []KiroImage              `json:"images,omitempty"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// KiroAssistantMessage represents an assistant message in Kiro payload.
type KiroAssistantMessage struct {
	Content  string        `json:"content"`
	ToolUses []KiroToolUse `json:"toolUses,omitempty"`
}

// KiroHistoryItem represents a turn in Kiro history.
type KiroHistoryItem struct {
	UserInputMessage         *KiroUserMessage      `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *KiroAssistantMessage `json:"assistantResponseMessage,omitempty"`
}

// KiroCurrentMessage wraps the current user input message.
type KiroCurrentMessage struct {
	UserInputMessage KiroUserMessage `json:"userInputMessage"`
}

// KiroConversationState represents the conversationState field in GenerateAssistantResponse.
type KiroConversationState struct {
	AgentContinuationID string             `json:"agentContinuationId,omitempty"`
	AgentTaskType       string             `json:"agentTaskType,omitempty"`
	ChatTriggerType     string             `json:"chatTriggerType"`
	ConversationID      string             `json:"conversationId"`
	CurrentMessage      KiroCurrentMessage `json:"currentMessage"`
	History             []KiroHistoryItem  `json:"history,omitempty"`
}

// KiroInferenceConfig represents optional generation controls accepted by Kiro.
type KiroInferenceConfig struct {
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
}

// KiroGenerateRequest represents the top-level payload for GenerateAssistantResponse.
type KiroGenerateRequest struct {
	ConversationState KiroConversationState `json:"conversationState"`
	ProfileArn        string                `json:"profileArn,omitempty"`
	InferenceConfig   *KiroInferenceConfig  `json:"inferenceConfig,omitempty"`
}

type UserInputMessageContext struct {
	Tools       []KiroToolWrapper `json:"tools,omitempty"`
	ToolResults []KiroToolResult  `json:"toolResults,omitempty"`
}

type KiroToolWrapper struct {
	ToolSpecification struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		InputSchema InputSchema `json:"inputSchema"`
	} `json:"toolSpecification"`
}

type InputSchema struct {
	JSON any `json:"json"`
}

type KiroToolResult struct {
	ToolUseID string              `json:"toolUseId"`
	Content   []KiroResultContent `json:"content"`
	Status    string              `json:"status"`
}

type KiroResultContent struct {
	Text string `json:"text"`
}

type KiroImage struct {
	Format string `json:"format"`
	Source struct {
		Bytes string `json:"bytes"`
	} `json:"source"`
}

type KiroToolUse struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
}

// OpenAIInputMessage represents standard OpenAI message structure plus Claude-compatible blocks.
type OpenAIInputMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type OpenAITool struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
	Function    struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
	InputSchema any `json:"input_schema,omitempty"`
}

// OpenAIRequest represents OpenAI chat and Claude Messages request fields consumed by Kiro.
type OpenAIRequest struct {
	Model               string               `json:"model"`
	Messages            []OpenAIInputMessage `json:"messages"`
	MaxTokens           int                  `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                  `json:"max_completion_tokens,omitempty"`
	Temperature         *float64             `json:"temperature,omitempty"`
	TopP                *float64             `json:"top_p,omitempty"`
	System              any                  `json:"system,omitempty"`
	Tools               []OpenAITool         `json:"tools,omitempty"`
}

// TranslateToKiroRequest converts an OpenAI/Claude JSON payload into Kiro's GenerateAssistantResponse format.
func TranslateToKiroRequest(rawBody []byte, profileArn string) ([]byte, string, error) {
	var req OpenAIRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal request: %w", err)
	}

	cleanModel := CleanKiroModelID(req.Model)
	origin := "AI_EDITOR"
	systemPrompt := extractSystemPrompt(req.System)
	var nonSystemMessages []OpenAIInputMessage
	for _, msg := range req.Messages {
		if strings.EqualFold(strings.TrimSpace(msg.Role), "system") {
			if text := extractContentString(msg.Content); strings.TrimSpace(text) != "" {
				if systemPrompt != "" {
					systemPrompt += "\n"
				}
				systemPrompt += text
			}
			continue
		}
		nonSystemMessages = append(nonSystemMessages, msg)
	}

	history := make([]KiroHistoryItem, 0, len(nonSystemMessages)+2)
	var currentContent string
	var currentImages []KiroImage
	var currentToolResults []KiroToolResult

	for i, msg := range nonSystemMessages {
		isLast := i == len(nonSystemMessages)-1
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "user":
			content, images, toolResults := extractUserContent(msg.Content)
			content = normalizeUserContent(content, len(images) > 0)
			if isLast {
				currentContent = content
				currentImages = images
				currentToolResults = toolResults
			} else {
				userMsg := KiroUserMessage{Content: content, ModelID: cleanModel, Origin: origin, Images: images}
				if len(toolResults) > 0 {
					userMsg.UserInputMessageContext = &UserInputMessageContext{ToolResults: toolResults}
				}
				history = append(history, KiroHistoryItem{UserInputMessage: &userMsg})
			}
		case "assistant":
			content, toolUses := extractAssistantContent(msg)
			history = append(history, KiroHistoryItem{AssistantResponseMessage: &KiroAssistantMessage{Content: content, ToolUses: toolUses}})
		case "tool":
			content, images, _ := extractUserContent(msg.Content)
			content = strings.TrimSpace(content)
			if content == "" && len(images) > 0 {
				content = toolResultImagePlaceholder
			}
			result := KiroToolResult{ToolUseID: msg.ToolCallID, Content: []KiroResultContent{{Text: content}}, Status: "success"}
			if isLast {
				currentToolResults = append(currentToolResults, result)
				currentImages = append(currentImages, images...)
			} else {
				userMsg := KiroUserMessage{Content: buildToolResultsContinuation([]KiroToolResult{result}), ModelID: cleanModel, Origin: origin, Images: images, UserInputMessageContext: &UserInputMessageContext{ToolResults: []KiroToolResult{result}}}
				history = append(history, KiroHistoryItem{UserInputMessage: &userMsg})
			}
		}
	}

	history = trimLeadingAssistantHistory(history)
	if strings.TrimSpace(systemPrompt) != "" {
		priming := []KiroHistoryItem{
			{UserInputMessage: &KiroUserMessage{Content: strings.TrimSpace(systemPrompt), ModelID: cleanModel, Origin: origin}},
			{AssistantResponseMessage: &KiroAssistantMessage{Content: "I will follow these instructions."}},
		}
		history = append(priming, history...)
	}

	currentToolResultIDs := collectToolResultIDs(currentToolResults)
	keepCurrentToolResults := currentToolResultsMatchLastAssistant(history, currentToolResultIDs)
	if keepCurrentToolResults {
		history = sanitizeKiroHistory(history, currentToolResultIDs)
	} else {
		history = sanitizeKiroHistory(history, nil)
	}

	finalContent := currentContent
	if len(currentToolResults) > 0 {
		finalContent = joinHistoryText(finalContent, buildToolResultsContinuation(currentToolResults))
	}
	if finalContent == "" {
		if len(currentImages) > 0 {
			finalContent = normalizeUserContent("", true)
		} else {
			finalContent = minimalFallbackUserContent
		}
	}

	kiroTools := convertOpenAITools(req.Tools)
	var attachToolResults []KiroToolResult
	if keepCurrentToolResults {
		attachToolResults = currentToolResults
	}

	kiroReq := KiroGenerateRequest{
		ConversationState: KiroConversationState{
			AgentContinuationID: uuid.New().String(),
			AgentTaskType:       "vibe",
			ChatTriggerType:     "MANUAL",
			ConversationID:      buildConversationID(cleanModel, systemPrompt, firstConversationAnchor(nonSystemMessages)),
			CurrentMessage: KiroCurrentMessage{UserInputMessage: KiroUserMessage{
				Content: finalContent,
				ModelID: cleanModel,
				Origin:  origin,
				Images:  currentImages,
			}},
			History: history,
		},
		ProfileArn: profileArn,
	}
	if len(kiroTools) > 0 || len(attachToolResults) > 0 {
		kiroReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext = &UserInputMessageContext{Tools: kiroTools, ToolResults: attachToolResults}
	}
	if maxTokens := effectiveMaxTokens(req); maxTokens > 0 || req.Temperature != nil || req.TopP != nil {
		kiroReq.InferenceConfig = &KiroInferenceConfig{MaxTokens: maxTokens, Temperature: req.Temperature, TopP: req.TopP}
	}

	truncatePayloadToLimit(&kiroReq)
	outBytes, err := json.Marshal(kiroReq)
	if err != nil {
		return nil, "", fmt.Errorf("failed to marshal Kiro request: %w", err)
	}
	return outBytes, cleanModel, nil
}

func effectiveMaxTokens(req OpenAIRequest) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return req.MaxCompletionTokens
}

// CleanKiroModelID strips provider prefix (kr/, kiro/) and suffix variants (-thinking, -agentic).
func CleanKiroModelID(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, "kr/")
	model = strings.TrimPrefix(model, "kiro/")
	model = strings.TrimSuffix(model, "-thinking-agentic")
	model = strings.TrimSuffix(model, "-thinking")
	model = strings.TrimSuffix(model, "-agentic")
	switch strings.ToLower(model) {
	case "", "kiro", "kiro-auto", "auto":
		return "auto"
	case "claude-sonnet-4-20250514", "claude-sonnet-4-0", "claude-sonnet-4.0":
		return "claude-sonnet-4"
	case "claude-sonnet-4-5":
		return "claude-sonnet-4.5"
	case "claude-3-5-sonnet", "claude-3.5-sonnet", "claude-3-7-sonnet", "claude-3.7-sonnet":
		return model
	case "claude-haiku-4.5", "claude-haiku-4-5":
		return "claude-haiku-4.5"
	case "claude-opus-4.5", "claude-opus-4-5":
		return "claude-opus-4.5"
	case "claude-opus-4.6", "claude-opus-4-6":
		return "claude-opus-4.6"
	case "claude-opus-4.7", "claude-opus-4-7":
		return "claude-opus-4.7"
	case "claude-opus-4.8", "claude-opus-4-8":
		return "claude-opus-4.8"
	case "claude-sonnet-4.6", "claude-sonnet-4-6":
		return "claude-sonnet-4.6"
	case "deepseek-v3.2", "deepseek-3.2", "deepseek-3-2":
		return "deepseek-3.2"
	case "minimax-m2.5", "minimax-m2-5":
		return "minimax-m2.5"
	case "minimax-m2.1", "minimax-m2-1":
		return "minimax-m2.1"
	case "gpt-5.6-sol", "gpt-5-6-sol":
		return "gpt-5.6-sol"
	case "gpt-5.6-terra", "gpt-5-6-terra":
		return "gpt-5.6-terra"
	case "gpt-5.6-luna", "gpt-5-6-luna":
		return "gpt-5.6-luna"
	}
	return normalizeKiroVersionSeparator(model)
}

// kiroDashVersionPattern matches a dashed major-minor version in the model
// families Kiro publishes with dotted versions, for example "claude-opus-5-5"
// or "gpt-5-7-sol". The leading family segment must not itself end in a digit so
// mid-name versions such as "claude-3-5-sonnet" are left untouched.
var kiroDashVersionPattern = regexp.MustCompile(`^([a-z]+(?:-[a-z]+)*)-(\d+)-(\d+)((?:-[a-z0-9.]+)*)$`)

// normalizeKiroVersionSeparator rewrites dashed major-minor versions into the
// dotted form Kiro uses in its catalog. Client SDKs commonly emit the dashed
// form, and handling it generically means newly released models resolve without
// another hardcoded case.
func normalizeKiroVersionSeparator(model string) string {
	match := kiroDashVersionPattern.FindStringSubmatch(strings.ToLower(model))
	if match == nil {
		return model
	}
	return fmt.Sprintf("%s-%s.%s%s", match[1], match[2], match[3], match[4])
}

func extractSystemPrompt(system any) string {
	switch v := system.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				if text, ok := block["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return extractContentString(system)
	}
}

func extractUserContent(content any) (string, []KiroImage, []KiroToolResult) {
	if s, ok := content.(string); ok {
		return s, nil, nil
	}
	var text strings.Builder
	var images []KiroImage
	var toolResults []KiroToolResult
	for _, block := range contentBlocks(content) {
		blockType, _ := block["type"].(string)
		switch blockType {
		case "text", "input_text":
			if t, ok := block["text"].(string); ok {
				text.WriteString(t)
			}
		case "image", "image_url", "input_image", "file", "input_file":
			if img := extractImageFromBlock(block); img != nil {
				images = append(images, *img)
			}
		case "tool_result":
			toolUseID, _ := block["tool_use_id"].(string)
			resultContent, resultImages := extractToolResultContent(block["content"])
			if len(resultImages) > 0 {
				images = append(images, resultImages...)
				if strings.TrimSpace(resultContent) == "" {
					resultContent = toolResultImagePlaceholder
				}
			}
			toolResults = append(toolResults, KiroToolResult{ToolUseID: toolUseID, Content: []KiroResultContent{{Text: resultContent}}, Status: "success"})
		default:
			if t, ok := extractTextPart(block); ok {
				text.WriteString(t)
			}
			if img := extractImageFromBlock(block); img != nil {
				images = append(images, *img)
			}
		}
	}
	if len(images) > 0 {
		return sanitizeImagePlaceholders(text.String()), images, toolResults
	}
	return text.String(), images, toolResults
}

func extractAssistantContent(msg OpenAIInputMessage) (string, []KiroToolUse) {
	text := extractContentString(msg.Content)
	var toolUses []KiroToolUse
	for _, call := range msg.ToolCalls {
		input := make(map[string]any)
		if strings.TrimSpace(call.Function.Arguments) != "" {
			_ = json.Unmarshal([]byte(call.Function.Arguments), &input)
		}
		toolUses = append(toolUses, KiroToolUse{ToolUseID: call.ID, Name: call.Function.Name, Input: input})
	}
	for _, block := range contentBlocks(msg.Content) {
		if blockType, _ := block["type"].(string); blockType != "tool_use" {
			continue
		}
		id, _ := block["id"].(string)
		name, _ := block["name"].(string)
		input, _ := block["input"].(map[string]any)
		if input == nil {
			input = make(map[string]any)
		}
		toolUses = append(toolUses, KiroToolUse{ToolUseID: id, Name: name, Input: input})
	}
	return text, toolUses
}

func extractContentString(content any) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				blockType, _ := m["type"].(string)
				if blockType == "tool_use" || blockType == "tool_result" {
					continue
				}
				if text, ok := m["text"].(string); ok {
					sb.WriteString(text)
				}
			}
		}
		return sb.String()
	case map[string]any:
		if text, ok := extractTextPart(v); ok {
			return text
		}
		if nested, ok := v["content"]; ok {
			return extractContentString(nested)
		}
	}
	return fmt.Sprintf("%v", content)
}

func contentBlocks(content any) []map[string]any {
	switch v := content.(type) {
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				out = append(out, block)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{v}
	default:
		return nil
	}
}

func extractTextPart(part map[string]any) (string, bool) {
	partType, _ := part["type"].(string)
	if partType == "text" || partType == "input_text" || partType == "" {
		if t, ok := part["text"].(string); ok {
			return t, true
		}
	}
	return "", false
}

func extractToolResultContent(content any) (string, []KiroImage) {
	if s, ok := content.(string); ok {
		return s, nil
	}
	var parts []string
	var images []KiroImage
	for _, block := range contentBlocks(content) {
		if img := extractImageFromBlock(block); img != nil {
			images = append(images, *img)
			continue
		}
		if text, ok := extractTextPart(block); ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, ""), images
}

func extractImageFromBlock(block map[string]any) *KiroImage {
	if source, ok := block["source"].(map[string]any); ok {
		if data, ok := source["data"].(string); ok {
			mediaType, _ := source["media_type"].(string)
			format := strings.TrimPrefix(strings.ToLower(mediaType), "image/")
			if img := parseDataURL(data); img != nil {
				return img
			}
			return parseBase64Image(data, format)
		}
		if url, ok := source["url"].(string); ok {
			return parseDataURL(url)
		}
	}
	if raw, ok := block["image_url"]; ok {
		switch v := raw.(type) {
		case string:
			return parseDataURL(v)
		case map[string]any:
			if u, ok := v["url"].(string); ok {
				return parseDataURL(u)
			}
		}
	}
	if raw, ok := block["url"].(string); ok {
		if img := parseDataURL(raw); img != nil {
			return img
		}
	}
	if raw, ok := block["b64_json"].(string); ok {
		return parseBase64Image(raw, "png")
	}
	if raw, ok := block["image_base64"].(string); ok {
		return parseBase64Image(raw, "png")
	}
	if raw, ok := block["data"].(string); ok {
		if img := parseDataURL(raw); img != nil {
			return img
		}
		return parseBase64Image(raw, "png")
	}
	return nil
}

func parseDataURL(raw string) *KiroImage {
	cleaned := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(raw, "\n", ""), "\r", ""))
	if strings.Contains(cleaned, "[Image") {
		return nil
	}
	re := regexp.MustCompile(`^data:image/([a-zA-Z0-9+.-]+)(;[a-zA-Z0-9=._:+-]+)*;base64,(.+)$`)
	matches := re.FindStringSubmatch(cleaned)
	if len(matches) == 4 {
		return parseBase64Image(matches[3], matches[1])
	}
	return nil
}

func parseBase64Image(data, format string) *KiroImage {
	data = strings.TrimSpace(data)
	if data == "" {
		return nil
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		if _, errRaw := base64.RawStdEncoding.DecodeString(data); errRaw != nil {
			if _, errURL := base64.URLEncoding.DecodeString(data); errURL != nil {
				if _, errRawURL := base64.RawURLEncoding.DecodeString(data); errRawURL != nil {
					return nil
				}
			}
		}
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "png"
	}
	if format == "jpg" {
		format = "jpeg"
	}
	img := &KiroImage{Format: format}
	img.Source.Bytes = data
	return img
}

func sanitizeImagePlaceholders(text string) string {
	re := regexp.MustCompile(`\[Image\s+\d+\]`)
	cleaned := re.ReplaceAllString(text, "")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return strings.TrimSpace(cleaned)
}

func normalizeUserContent(text string, hasImages bool) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" && hasImages {
		return "Please analyze the attached image."
	}
	return trimmed
}

func convertOpenAITools(tools []OpenAITool) []KiroToolWrapper {
	if len(tools) == 0 {
		return nil
	}
	out := make([]KiroToolWrapper, 0, len(tools))
	for _, tool := range tools {
		name := tool.Function.Name
		desc := tool.Function.Description
		schema := tool.Function.Parameters
		if name == "" {
			name = tool.Name
		}
		if desc == "" {
			desc = tool.Description
		}
		if schema == nil {
			schema = tool.Parameters
		}
		if schema == nil {
			schema = tool.InputSchema
		}
		if strings.TrimSpace(name) == "" {
			continue
		}
		if tool.Type != "" && tool.Type != "function" && tool.Function.Name == "" && tool.Name == "" {
			continue
		}
		if len(desc) > maxToolDescLen {
			desc = desc[:maxToolDescLen] + "..."
		}
		wrapper := KiroToolWrapper{}
		wrapper.ToolSpecification.Name = shortenToolName(name)
		wrapper.ToolSpecification.Description = normalizeToolDesc(desc, wrapper.ToolSpecification.Name)
		wrapper.ToolSpecification.InputSchema = InputSchema{JSON: ensureObjectSchema(schema)}
		out = append(out, wrapper)
	}
	return out
}

func normalizeToolDesc(desc, name string) string {
	if strings.TrimSpace(desc) != "" {
		return desc
	}
	return "Tool: " + name
}

func shortenToolName(name string) string {
	if len(name) <= 64 {
		return name
	}
	return name[:64]
}

func ensureObjectSchema(schema any) any {
	m, ok := schema.(map[string]any)
	if !ok {
		return map[string]any{"type": "object"}
	}
	cleaned := cloneSchemaMap(m)
	cleanSchema(cleaned)
	if _, hasType := cleaned["type"]; !hasType {
		cleaned["type"] = "object"
	}
	return cleaned
}

func cloneSchemaMap(m map[string]any) map[string]any {
	cloned := make(map[string]any, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case map[string]any:
			cloned[k] = cloneSchemaMap(val)
		case []any:
			items := make([]any, 0, len(val))
			for _, item := range val {
				if child, ok := item.(map[string]any); ok {
					items = append(items, cloneSchemaMap(child))
				} else {
					items = append(items, item)
				}
			}
			cloned[k] = items
		default:
			cloned[k] = v
		}
	}
	return cloned
}

func cleanSchema(m map[string]any) {
	delete(m, "additionalProperties")
	if req, exists := m["required"]; exists {
		switch arr := req.(type) {
		case []any:
			if len(arr) == 0 {
				delete(m, "required")
			}
		case []string:
			if len(arr) == 0 {
				delete(m, "required")
			}
		default:
			delete(m, "required")
		}
	}
	for _, v := range m {
		switch val := v.(type) {
		case map[string]any:
			cleanSchema(val)
		case []any:
			for _, item := range val {
				if child, ok := item.(map[string]any); ok {
					cleanSchema(child)
				}
			}
		}
	}
}

func collectToolResultIDs(toolResults []KiroToolResult) map[string]bool {
	if len(toolResults) == 0 {
		return nil
	}
	ids := make(map[string]bool, len(toolResults))
	for _, tr := range toolResults {
		if id := strings.TrimSpace(tr.ToolUseID); id != "" {
			ids[id] = true
		}
	}
	return ids
}

func currentToolResultsMatchLastAssistant(history []KiroHistoryItem, currentToolResultIDs map[string]bool) bool {
	if len(history) == 0 || len(currentToolResultIDs) == 0 {
		return false
	}
	last := history[len(history)-1]
	if last.AssistantResponseMessage == nil || len(last.AssistantResponseMessage.ToolUses) == 0 {
		return false
	}
	for _, toolUse := range last.AssistantResponseMessage.ToolUses {
		if !currentToolResultIDs[toolUse.ToolUseID] {
			return false
		}
	}
	return true
}

func sanitizeKiroHistory(history []KiroHistoryItem, currentToolResultIDs map[string]bool) []KiroHistoryItem {
	if len(history) == 0 {
		return history
	}
	toolNames := make(map[string]string)
	for i := range history {
		if a := history[i].AssistantResponseMessage; a != nil {
			for _, toolUse := range a.ToolUses {
				if toolUse.ToolUseID != "" && toolUse.Name != "" {
					toolNames[toolUse.ToolUseID] = toolUse.Name
				}
			}
		}
	}
	activeIdx := -1
	if len(currentToolResultIDs) > 0 {
		last := history[len(history)-1]
		if last.AssistantResponseMessage != nil && len(last.AssistantResponseMessage.ToolUses) > 0 {
			allCovered := true
			for _, toolUse := range last.AssistantResponseMessage.ToolUses {
				if !currentToolResultIDs[toolUse.ToolUseID] {
					allCovered = false
					break
				}
			}
			if allCovered {
				activeIdx = len(history) - 1
			}
		}
	}
	for i := range history {
		msg := &history[i]
		if msg.AssistantResponseMessage != nil && len(msg.AssistantResponseMessage.ToolUses) > 0 && i != activeIdx {
			msg.AssistantResponseMessage.ToolUses = nil
		}
		if msg.UserInputMessage != nil && msg.UserInputMessage.UserInputMessageContext != nil {
			ctx := msg.UserInputMessage.UserInputMessageContext
			if len(ctx.ToolResults) > 0 {
				msg.UserInputMessage.Content = joinHistoryText(msg.UserInputMessage.Content, narrateToolResults(ctx.ToolResults, toolNames))
				ctx.ToolResults = nil
			}
			ctx.Tools = nil
			if len(ctx.ToolResults) == 0 && len(ctx.Tools) == 0 {
				msg.UserInputMessage.UserInputMessageContext = nil
			}
		}
		if msg.UserInputMessage != nil && strings.TrimSpace(msg.UserInputMessage.Content) == "" && len(msg.UserInputMessage.Images) == 0 {
			msg.UserInputMessage.Content = minimalFallbackUserContent
		}
	}
	cleaned := history[:0:0]
	for _, msg := range history {
		if msg.AssistantResponseMessage != nil && len(msg.AssistantResponseMessage.ToolUses) == 0 && strings.TrimSpace(msg.AssistantResponseMessage.Content) == "" {
			continue
		}
		cleaned = append(cleaned, msg)
	}
	return trimLeadingAssistantHistory(cleaned)
}

func narrateToolResults(toolResults []KiroToolResult, names map[string]string) string {
	if len(toolResults) == 0 {
		return ""
	}
	parts := make([]string, 0, len(toolResults))
	for _, tr := range toolResults {
		var texts []string
		for _, c := range tr.Content {
			if strings.TrimSpace(c.Text) != "" {
				texts = append(texts, c.Text)
			}
		}
		body := strings.Join(texts, "\n")
		if strings.TrimSpace(body) == "" {
			body = "(no output)"
		}
		if name := names[tr.ToolUseID]; name != "" {
			parts = append(parts, fmt.Sprintf("[%s] %s", name, body))
		} else {
			parts = append(parts, body)
		}
	}
	return toolResultsContinuationPrefix + "\n\n" + strings.Join(parts, "\n\n")
}

func joinHistoryText(existing, narrated string) string {
	existing = strings.TrimSpace(existing)
	narrated = strings.TrimSpace(narrated)
	switch {
	case existing != "" && narrated != "":
		return existing + "\n\n" + narrated
	case narrated != "":
		return narrated
	default:
		return existing
	}
}

func buildToolResultsContinuation(toolResults []KiroToolResult) string {
	if len(toolResults) == 0 {
		return minimalFallbackUserContent
	}
	parts := make([]string, 0, len(toolResults))
	for _, tr := range toolResults {
		for _, c := range tr.Content {
			if strings.TrimSpace(c.Text) != "" {
				parts = append(parts, c.Text)
			}
		}
	}
	if len(parts) == 0 {
		return minimalFallbackUserContent
	}
	joined := toolResultsContinuationPrefix + "\n\n" + strings.Join(parts, "\n\n")
	if len(joined) > 4000 {
		return joined[:4000]
	}
	return joined
}

func trimLeadingAssistantHistory(history []KiroHistoryItem) []KiroHistoryItem {
	idx := 0
	for idx < len(history) && history[idx].AssistantResponseMessage != nil {
		idx++
	}
	if idx == 0 {
		return history
	}
	if idx >= len(history) {
		return nil
	}
	return history[idx:]
}

func firstConversationAnchor(messages []OpenAIInputMessage) string {
	for _, msg := range messages {
		if !strings.EqualFold(msg.Role, "user") {
			continue
		}
		text, _, toolResults := extractUserContent(msg.Content)
		if strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
		if len(toolResults) > 0 {
			continue
		}
	}
	return ""
}

func buildConversationID(modelID, systemPrompt, anchor string) string {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" || anchor == minimalFallbackUserContent {
		return uuid.New().String()
	}
	seed := strings.Join([]string{modelID, strings.TrimSpace(systemPrompt), anchor}, "\n")
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed)).String()
}

func truncatePayloadToLimit(payload *KiroGenerateRequest) {
	if payload == nil || payloadByteSize(payload) <= maxPayloadBytes {
		return
	}
	history := payload.ConversationState.History
	keepFrom := len(history)
	for keepFrom > 0 {
		payload.ConversationState.History = append([]KiroHistoryItem{{UserInputMessage: &KiroUserMessage{Content: truncationPlaceholder, ModelID: payload.ConversationState.CurrentMessage.UserInputMessage.ModelID, Origin: "AI_EDITOR"}}}, history[keepFrom:]...)
		if payloadByteSize(payload) <= maxPayloadBytes || len(history)-keepFrom < minRecentHistoryTurns {
			break
		}
		keepFrom--
	}
	if payloadByteSize(payload) > maxPayloadBytes {
		cur := &payload.ConversationState.CurrentMessage.UserInputMessage
		overhead := payloadByteSize(payload) - len(cur.Content)
		budget := maxPayloadBytes - overhead
		if budget <= 0 {
			cur.Content = minimalFallbackUserContent
		} else if len(cur.Content) > budget {
			cur.Content = cur.Content[:budget]
		}
	}
}

func payloadByteSize(payload *KiroGenerateRequest) int {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0
	}
	return len(raw)
}
