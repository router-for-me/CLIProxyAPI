package main

import (
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- SHA-1 is used only for deterministic UUID-compatible identifiers.
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	maxToolDescriptionLength = 10237
	maxKiroPayloadBytes      = 900 * 1024
)

var (
	modelDashVersionPattern = regexp.MustCompile(`^claude-(opus|sonnet|haiku)-(\d+)-(\d{1,2})([^0-9].*)?$`)
	invalidToolNamePattern  = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
)

func claudeToKiro(raw []byte, requestedModel string) (*kiroPayload, *claudeRequest, error) {
	var request claudeRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, nil, fmt.Errorf("decode Claude request: %w", errUnmarshal)
	}
	model := normalizeKiroModel(firstNonEmpty(requestedModel, request.Model))
	if model == "" {
		return nil, nil, fmt.Errorf("model is required")
	}
	if len(request.Messages) == 0 {
		return nil, nil, fmt.Errorf("messages must not be empty")
	}
	if lastRole := strings.ToLower(strings.TrimSpace(request.Messages[len(request.Messages)-1].Role)); lastRole != "user" {
		return nil, nil, fmt.Errorf("last message must have role user; Kiro does not support assistant prefills")
	}

	systemPrompt := extractSystemPrompt(request.System)
	if request.Thinking != nil {
		kind := strings.ToLower(strings.TrimSpace(request.Thinking.Type))
		if kind == "enabled" || kind == "adaptive" {
			thinkingBudget := 200000
			if request.Thinking.BudgetTokens > 0 {
				thinkingBudget = request.Thinking.BudgetTokens
			}
			systemPrompt = strings.TrimSpace(fmt.Sprintf("<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>%d</max_thinking_length>\n\n%s", thinkingBudget, systemPrompt))
		}
	}

	payload := &kiroPayload{}
	payload.ConversationState.ChatTriggerType = "MANUAL"
	payload.ConversationState.AgentTaskType = "vibe"
	payload.ConversationState.AgentContinuationID = randomUUID()
	payload.ConversationState.ConversationID = conversationID(model, systemPrompt, firstUserAnchor(request.Messages))

	history := make([]kiroHistoryMessage, 0, len(request.Messages)+2)
	if systemPrompt != "" {
		history = append(history,
			kiroHistoryMessage{UserInputMessage: &kiroUserInputMessage{Content: systemPrompt, ModelID: model, Origin: "KIRO_CLI"}},
			kiroHistoryMessage{AssistantResponseMessage: &kiroAssistantResponseMessage{Content: "I will follow these instructions."}},
		)
	}

	tools, nameMap, requestToolNames := convertTools(request.Tools)
	var currentText string
	var currentImages []kiroImage
	var currentToolResults []kiroToolResult
	for index, message := range request.Messages {
		last := index == len(request.Messages)-1
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "user":
			text, images, toolResults := extractUserContent(message.Content)
			if last {
				currentText, currentImages, currentToolResults = text, images, toolResults
				continue
			}
			userMessage := &kiroUserInputMessage{Content: fallbackContent(text, len(images) > 0), ModelID: model, Origin: "KIRO_CLI", Images: images}
			if len(toolResults) > 0 {
				userMessage.UserInputMessageContext = &kiroUserMessageContext{ToolResults: toolResults}
			}
			history = append(history, kiroHistoryMessage{UserInputMessage: userMessage})
		case "assistant":
			text, assistantTools := extractAssistantContent(message.Content, requestToolNames)
			history = append(history, kiroHistoryMessage{AssistantResponseMessage: &kiroAssistantResponseMessage{Content: text, ToolUses: assistantTools}})
		}
	}

	payload.ToolNameMap = nameMap
	if len(currentToolResults) > 0 {
		currentText = joinNonEmpty(currentText, readableToolResults(currentToolResults))
	}
	current := kiroUserInputMessage{
		Content: fallbackContent(currentText, len(currentImages) > 0),
		ModelID: model,
		Origin:  "KIRO_CLI",
		Images:  currentImages,
	}
	if len(tools) > 0 || len(currentToolResults) > 0 {
		current.UserInputMessageContext = &kiroUserMessageContext{Tools: tools, ToolResults: currentToolResults}
	}
	payload.ConversationState.CurrentMessage.UserInputMessage = current
	payload.ConversationState.History = trimLeadingAssistant(history)
	if request.MaxTokens > 0 || request.Temperature != nil || request.TopP != nil {
		payload.InferenceConfig = &kiroInferenceConfig{MaxTokens: request.MaxTokens, Temperature: request.Temperature, TopP: request.TopP}
	}
	if errTruncate := truncatePayload(payload, systemPrompt != ""); errTruncate != nil {
		return nil, nil, errTruncate
	}
	payload.EstimatedInputTokens = estimateJSONTokens(payload)
	return payload, &request, nil
}

func normalizeKiroModel(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimSuffix(model, "-thinking")
	if model == "claude-sonnet-4-20250514" {
		return "claude-sonnet-4"
	}
	if match := modelDashVersionPattern.FindStringSubmatch(model); len(match) == 5 {
		return fmt.Sprintf("claude-%s-%s.%s%s", match[1], match[2], match[3], match[4])
	}
	return model
}

func extractSystemPrompt(value any) string {
	switch system := value.(type) {
	case string:
		return strings.TrimSpace(system)
	case []any:
		parts := make([]string, 0, len(system))
		for _, item := range system {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, okText := block["text"].(string); okText && text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func extractUserContent(content any) (string, []kiroImage, []kiroToolResult) {
	if text, ok := content.(string); ok {
		return text, nil, nil
	}
	var texts []string
	var images []kiroImage
	var results []kiroToolResult
	for _, block := range contentBlocks(content) {
		typeName, _ := block["type"].(string)
		switch typeName {
		case "text", "input_text":
			if text, ok := block["text"].(string); ok {
				texts = append(texts, text)
			}
		case "image", "input_image":
			if image := extractImage(block); image != nil {
				images = append(images, *image)
			}
		case "tool_result":
			toolUseID, _ := block["tool_use_id"].(string)
			text, resultImages := extractToolResultContent(block["content"])
			images = append(images, resultImages...)
			status := "success"
			if isError, _ := block["is_error"].(bool); isError {
				status = "error"
			}
			results = append(results, kiroToolResult{ToolUseID: toolUseID, Content: []kiroResultContent{{Text: fallbackContent(text, len(resultImages) > 0)}}, Status: status})
		}
	}
	return strings.Join(texts, ""), images, results
}

func extractAssistantContent(content any, toolNames map[string]string) (string, []kiroToolUse) {
	if text, ok := content.(string); ok {
		return text, nil
	}
	var texts []string
	var tools []kiroToolUse
	for _, block := range contentBlocks(content) {
		typeName, _ := block["type"].(string)
		switch typeName {
		case "text":
			if text, ok := block["text"].(string); ok {
				texts = append(texts, text)
			}
		case "tool_use":
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			kiroName := toolNames[name]
			if kiroName == "" {
				kiroName = sanitizeToolName(name)
			}
			input, _ := block["input"].(map[string]any)
			if input == nil {
				input = map[string]any{}
			}
			tools = append(tools, kiroToolUse{ToolUseID: id, Name: kiroName, Input: input})
		}
	}
	return strings.Join(texts, ""), tools
}

func contentBlocks(content any) []map[string]any {
	items, ok := content.([]any)
	if !ok {
		return nil
	}
	blocks := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if block, okBlock := item.(map[string]any); okBlock {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func extractImage(block map[string]any) *kiroImage {
	source, ok := block["source"].(map[string]any)
	if !ok {
		return nil
	}
	data, _ := source["data"].(string)
	mediaType, _ := source["media_type"].(string)
	if data == "" {
		return nil
	}
	if _, errDecode := base64.StdEncoding.DecodeString(data); errDecode != nil {
		return nil
	}
	format := strings.TrimPrefix(strings.ToLower(mediaType), "image/")
	if format == "jpg" {
		format = "jpeg"
	}
	if format != "png" && format != "jpeg" && format != "gif" && format != "webp" {
		return nil
	}
	return &kiroImage{Format: format, Source: kiroImageSource{Bytes: data}}
}

func extractToolResultContent(content any) (string, []kiroImage) {
	if text, ok := content.(string); ok {
		return text, nil
	}
	var texts []string
	var images []kiroImage
	for _, block := range contentBlocks(content) {
		if text, ok := block["text"].(string); ok {
			texts = append(texts, text)
		}
		if image := extractImage(block); image != nil {
			images = append(images, *image)
		}
	}
	return strings.Join(texts, ""), images
}

func convertTools(tools []claudeTool) ([]kiroToolWrapper, map[string]string, map[string]string) {
	out := make([]kiroToolWrapper, 0, len(tools))
	nameMap := make(map[string]string)
	requestNameMap := make(map[string]string)
	used := make(map[string]struct{})
	for _, tool := range tools {
		if strings.HasPrefix(strings.ToLower(tool.Type), "web_search") {
			continue
		}
		name := uniqueToolName(sanitizeToolName(tool.Name), used)
		requestNameMap[tool.Name] = name
		if name != tool.Name {
			nameMap[name] = tool.Name
		}
		description := strings.TrimSpace(tool.Description)
		if description == "" {
			description = "Call " + name + "."
		}
		if len(description) > maxToolDescriptionLength {
			description = description[:maxToolDescriptionLength]
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok || schema == nil {
			schema = map[string]any{"type": "object"}
		}
		if _, exists := schema["type"]; !exists {
			schema["type"] = "object"
		}
		out = append(out, kiroToolWrapper{ToolSpecification: kiroToolSpecification{
			Name: name, Description: description, InputSchema: kiroInputSchema{JSON: schema},
		}})
	}
	if len(nameMap) == 0 {
		nameMap = nil
	}
	if len(requestNameMap) == 0 {
		requestNameMap = nil
	}
	return out, nameMap, requestNameMap
}

func sanitizeToolName(name string) string {
	name = invalidToolNamePattern.ReplaceAllString(strings.TrimSpace(name), "_")
	name = strings.Trim(name, "_")
	if name == "" {
		name = "tool"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func uniqueToolName(name string, used map[string]struct{}) string {
	if _, exists := used[name]; !exists {
		used[name] = struct{}{}
		return name
	}
	for index := 2; ; index++ {
		suffix := fmt.Sprintf("_%d", index)
		base := name
		if len(base)+len(suffix) > 64 {
			base = base[:64-len(suffix)]
		}
		candidate := base + suffix
		if _, exists := used[candidate]; exists {
			continue
		}
		used[candidate] = struct{}{}
		return candidate
	}
}

func readableToolResults(results []kiroToolResult) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		for _, content := range result.Content {
			if text := strings.TrimSpace(content.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Tool results:\n\n" + strings.Join(parts, "\n\n")
}

func fallbackContent(content string, hasImage bool) string {
	if strings.TrimSpace(content) != "" {
		return content
	}
	if hasImage {
		return "Please analyze the attached image."
	}
	return "."
}

func joinNonEmpty(left, right string) string {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + "\n\n" + right
}

func trimLeadingAssistant(history []kiroHistoryMessage) []kiroHistoryMessage {
	for len(history) > 0 && history[0].AssistantResponseMessage != nil {
		history = history[1:]
	}
	return history
}

func firstUserAnchor(messages []claudeMessage) string {
	for _, message := range messages {
		if strings.EqualFold(message.Role, "user") {
			text, _, _ := extractUserContent(message.Content)
			if text = strings.TrimSpace(text); text != "" && text != "." {
				return text
			}
		}
	}
	return ""
}

func conversationID(model, systemPrompt, anchor string) string {
	if strings.TrimSpace(anchor) == "" {
		return randomUUID()
	}
	seed := strings.Join([]string{model, strings.TrimSpace(systemPrompt), strings.TrimSpace(anchor)}, "\n")
	sum := sha1.Sum(append([]byte("6ba7b8119dad11d180b400c04fd430c8"), []byte(seed)...))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

func randomUUID() string {
	b := make([]byte, 16)
	if _, errRead := rand.Read(b); errRead != nil {
		sum := sha1.Sum([]byte(fmt.Sprintf("fallback-%p", &b)))
		b = sum[:16]
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

func formatUUID(b []byte) string {
	hexValue := hex.EncodeToString(b)
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:32]
}

func truncatePayload(payload *kiroPayload, preserveSystemPair bool) error {
	protected := 0
	if preserveSystemPair {
		protected = 2
	}
	for {
		raw, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return fmt.Errorf("encode Kiro payload: %w", errMarshal)
		}
		if len(raw) <= maxKiroPayloadBytes {
			return nil
		}
		if len(payload.ConversationState.History) <= protected {
			return fmt.Errorf("Kiro request payload is %d bytes after truncating removable history; limit is %d bytes", len(raw), maxKiroPayloadBytes)
		}
		removeCount := 1
		if protected+1 < len(payload.ConversationState.History) &&
			payload.ConversationState.History[protected].UserInputMessage != nil &&
			payload.ConversationState.History[protected+1].AssistantResponseMessage != nil {
			removeCount = 2
		}
		payload.ConversationState.History = append(payload.ConversationState.History[:protected], payload.ConversationState.History[protected+removeCount:]...)
	}
}
