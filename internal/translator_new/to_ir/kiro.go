/**
 * @file Kiro (Amazon Q) response parser
 * @description Converts Kiro API responses (JSON and EventStream) into unified format.
 */

package to_ir

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
	"github.com/tidwall/gjson"
)

var (
	embeddedToolCallPattern = regexp.MustCompile(`\[Called\s+(\w+)\s+with\s+args:\s*`)
	trailingCommaPattern    = regexp.MustCompile(`,\s*([}\]])`)
	unquotedKeyPattern      = regexp.MustCompile(`([{,]\s*)([a-zA-Z_][a-zA-Z0-9_]*)\s*:`)
)

// ParseKiroResponse converts a non-streaming Kiro API response to unified format.
func ParseKiroResponse(rawJSON []byte) ([]ir.Message, *ir.Usage, error) {
	if !gjson.ValidBytes(rawJSON) {
		return nil, nil, &json.UnmarshalTypeError{Value: "invalid json"}
	}
	parsed := gjson.ParseBytes(rawJSON)

	// Try finding assistant response in various paths
	var resp gjson.Result
	if r := parsed.Get("conversationState.currentMessage.assistantResponseMessage"); r.Exists() {
		resp = r
	} else if r := parsed.Get("assistantResponseMessage"); r.Exists() {
		resp = r
	} else {
		return nil, nil, nil
	}

	msg := &ir.Message{Role: ir.RoleAssistant}
	if content := resp.Get("content").String(); content != "" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content})
	}

	for _, tool := range resp.Get("toolUsages").Array() {
		msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{
			ID:   convertToolID(tool.Get("toolUseId").String()),
			Name: tool.Get("name").String(),
			Args: tool.Get("input").String(),
		})
	}

	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
		return nil, nil, nil
	}
	return []ir.Message{*msg}, nil, nil
}

// KiroStreamState tracks state for Kiro streaming response parsing.
type KiroStreamState struct {
	Usage              *ir.Usage
	CurrentTool        *ir.ToolCall
	AccumulatedContent string
	CurrentToolInput   string
	ToolCalls          []ir.ToolCall
}

func NewKiroStreamState() *KiroStreamState {
	return &KiroStreamState{ToolCalls: make([]ir.ToolCall, 0)}
}

// ProcessChunk processes a Kiro stream chunk and returns events.
func (s *KiroStreamState) ProcessChunk(rawJSON []byte) ([]ir.UnifiedEvent, error) {
	if len(rawJSON) == 0 {
		return nil, nil
	}
	if !gjson.ValidBytes(rawJSON) {
		return nil, nil
	}
	parsed := gjson.ParseBytes(rawJSON)

	s.parseUsage(parsed)

	if parsed.Get("toolUseId").Exists() && parsed.Get("name").Exists() {
		return s.processToolEvent(parsed), nil
	}

	return s.processRegularEvents(parsed), nil
}

func (s *KiroStreamState) parseUsage(parsed gjson.Result) {
	usageNode := parsed.Get("supplementaryWebLinksEvent")
	if !usageNode.Exists() {
		if parsed.Get("inputTokens").Exists() || parsed.Get("outputTokens").Exists() {
			usageNode = parsed
		}
	}

	if !usageNode.Exists() {
		return
	}

	inTokens := usageNode.Get("inputTokens").Int()
	outTokens := usageNode.Get("outputTokens").Int()

	if inTokens > 0 || outTokens > 0 {
		s.Usage = &ir.Usage{
			PromptTokens:     int(inTokens),
			CompletionTokens: int(outTokens),
			TotalTokens:      int(inTokens + outTokens),
		}
	}
}

func (s *KiroStreamState) processToolEvent(parsed gjson.Result) []ir.UnifiedEvent {
	id := convertToolID(parsed.Get("toolUseId").String())
	name := parsed.Get("name").String()

	var events []ir.UnifiedEvent
	isNewTool := s.CurrentTool == nil || s.CurrentTool.ID != id
	toolIndex := len(s.ToolCalls)

	if isNewTool {
		s.CurrentTool = &ir.ToolCall{ID: id, Name: name}
		s.CurrentToolInput = ""
	}

	inputNode := parsed.Get("input")
	var inputDelta string
	if inputNode.IsObject() {
		inputDelta = inputNode.Raw
	} else {
		inputDelta = inputNode.String()
	}
	s.CurrentToolInput += inputDelta

	if isNewTool || inputDelta != "" {
		tc := &ir.ToolCall{Args: inputDelta}
		if isNewTool {
			tc.ID = s.CurrentTool.ID
			tc.Name = s.CurrentTool.Name
		}
		events = append(events, ir.UnifiedEvent{
			Type:          ir.EventTypeToolCall,
			ToolCall:      tc,
			ToolCallIndex: toolIndex,
		})
	}

	if parsed.Get("stop").Bool() {
		s.CurrentTool.Args = s.CurrentToolInput
		if s.CurrentTool.Args == "" {
			s.CurrentTool.Args = "{}"
		}
		s.ToolCalls = append(s.ToolCalls, *s.CurrentTool)
		s.CurrentTool = nil
		s.CurrentToolInput = ""
	}

	return events
}

func (s *KiroStreamState) processRegularEvents(parsed gjson.Result) []ir.UnifiedEvent {
	var events []ir.UnifiedEvent
	data := parsed
	if r := parsed.Get("assistantResponseEvent"); r.Exists() {
		data = r
	}

	if content := data.Get("content").String(); content != "" {
		cleanContent, embeddedTools := ParseEmbeddedToolCalls(content)

		if cleanContent != "" {
			s.AccumulatedContent += cleanContent
			events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToken, Content: cleanContent})
		}

		for _, tc := range embeddedTools {
			if !s.hasToolCall(tc.ID) {
				s.ToolCalls = append(s.ToolCalls, tc)
				tcCopy := tc
				events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToolCall, ToolCall: &tcCopy})
			}
		}
	}

	for _, tool := range data.Get("toolUsages").Array() {
		tc := ir.ToolCall{
			ID:   convertToolID(tool.Get("toolUseId").String()),
			Name: tool.Get("name").String(),
			Args: tool.Get("input").String(),
		}
		if !s.hasToolCall(tc.ID) {
			s.ToolCalls = append(s.ToolCalls, tc)
			events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToolCall, ToolCall: &tc})
		}
	}
	return events
}

func (s *KiroStreamState) hasToolCall(id string) bool {
	for _, tc := range s.ToolCalls {
		if tc.ID == id {
			return true
		}
	}
	return false
}

func (s *KiroStreamState) DetermineFinishReason() ir.FinishReason {
	if len(s.ToolCalls) > 0 {
		return ir.FinishReasonToolCalls
	}
	return ir.FinishReasonStop
}

func convertToolID(id string) string {
	if strings.HasPrefix(id, "tooluse_") {
		return strings.Replace(id, "tooluse_", "call_", 1)
	}
	return id
}

// ParseEmbeddedToolCalls extracts [Called tool_name with args: {...}] format from text.
func ParseEmbeddedToolCalls(text string) (string, []ir.ToolCall) {
	if !strings.Contains(text, "[Called") {
		return text, nil
	}

	var toolCalls []ir.ToolCall
	cleanText := text
	processedIDs := make(map[string]bool)

	matches := embeddedToolCallPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}

	// Process matches in reverse order
	for i := len(matches) - 1; i >= 0; i-- {
		matchStart := matches[i][0]
		toolNameStart := matches[i][2]
		toolNameEnd := matches[i][3]

		if toolNameStart < 0 || toolNameEnd < 0 {
			continue
		}

		toolName := text[toolNameStart:toolNameEnd]
		jsonStart := matches[i][1]

		if jsonStart >= len(text) {
			continue
		}

		// Skip whitespace
		for jsonStart < len(text) && (text[jsonStart] == ' ' || text[jsonStart] == '\t') {
			jsonStart++
		}

		if jsonStart >= len(text) || text[jsonStart] != '{' {
			continue
		}

		jsonEnd := findMatchingBracket(text, jsonStart)
		if jsonEnd < 0 {
			continue
		}

		jsonStr := text[jsonStart : jsonEnd+1]
		closingBracket := jsonEnd + 1
		for closingBracket < len(text) && text[closingBracket] != ']' {
			closingBracket++
		}
		if closingBracket >= len(text) {
			continue
		}

		fullMatch := text[matchStart : closingBracket+1]
		repairedJSON := repairJSON(jsonStr)
		var argsMap map[string]interface{}
		if err := json.Unmarshal([]byte(repairedJSON), &argsMap); err != nil {
			continue
		}

		toolUseID := "call_" + uuid.New().String()[:12]
		dedupeKey := toolName + ":" + repairedJSON
		if processedIDs[dedupeKey] {
			cleanText = strings.Replace(cleanText, fullMatch, "", 1)
			continue
		}
		processedIDs[dedupeKey] = true

		toolCalls = append(toolCalls, ir.ToolCall{
			ID:   toolUseID,
			Name: toolName,
			Args: repairedJSON,
		})

		cleanText = strings.Replace(cleanText, fullMatch, "", 1)
	}

	return strings.TrimSpace(cleanText), toolCalls
}

func findMatchingBracket(text string, startPos int) int {
	if startPos >= len(text) {
		return -1
	}

	openChar := text[startPos]
	var closeChar byte
	switch openChar {
	case '{':
		closeChar = '}'
	case '[':
		closeChar = ']'
	default:
		return -1
	}

	depth := 1
	inString := false
	escapeNext := false

	for i := startPos + 1; i < len(text); i++ {
		char := text[i]

		if escapeNext {
			escapeNext = false
			continue
		}
		if char == '\\' && inString {
			escapeNext = true
			continue
		}
		if char == '"' {
			inString = !inString
			continue
		}

		if !inString {
			if char == openChar {
				depth++
			} else if char == closeChar {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}
	return -1
}

func repairJSON(raw string) string {
	repaired := trailingCommaPattern.ReplaceAllString(raw, "$1")
	repaired = unquotedKeyPattern.ReplaceAllString(repaired, `$1"$2":`)
	return repaired
}
