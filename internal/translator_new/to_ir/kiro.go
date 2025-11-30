/**
 * @file Kiro (Amazon Q) response parser
 * @description Converts Kiro API responses (JSON and EventStream) into unified format.
 */

package to_ir

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
	"github.com/tidwall/gjson"
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
	AccumulatedContent string
	ToolCalls          []ir.ToolCall
	CurrentTool        *ir.ToolCall
	CurrentToolInput   string
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

	// Handle structured tool call event (incremental)
	if parsed.Get("toolUseId").Exists() && parsed.Get("name").Exists() {
		return s.processToolEvent(parsed), nil
	}

	// Handle regular events (content or completed tool usages)
	return s.processRegularEvents(parsed), nil
}

func (s *KiroStreamState) processToolEvent(parsed gjson.Result) []ir.UnifiedEvent {
	id := convertToolID(parsed.Get("toolUseId").String())
	if s.CurrentTool == nil || s.CurrentTool.ID != id {
		s.CurrentTool = &ir.ToolCall{ID: id, Name: parsed.Get("name").String()}
		s.CurrentToolInput = ""
	}

	s.CurrentToolInput += parsed.Get("input").String()

	if parsed.Get("stop").Bool() {
		s.CurrentTool.Args = s.CurrentToolInput
		if s.CurrentTool.Args == "" {
			s.CurrentTool.Args = "{}"
		}
		s.ToolCalls = append(s.ToolCalls, *s.CurrentTool)
		event := ir.UnifiedEvent{Type: ir.EventTypeToolCall, ToolCall: s.CurrentTool}
		s.CurrentTool = nil
		s.CurrentToolInput = ""
		return []ir.UnifiedEvent{event}
	}
	return nil
}

func (s *KiroStreamState) processRegularEvents(parsed gjson.Result) []ir.UnifiedEvent {
	var events []ir.UnifiedEvent
	// Unwrap if needed
	data := parsed
	if r := parsed.Get("assistantResponseEvent"); r.Exists() {
		data = r
	}

	if content := data.Get("content").String(); content != "" {
		s.AccumulatedContent += content
		events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToken, Content: content})
	}

	// Handle completed tool usages in array
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
