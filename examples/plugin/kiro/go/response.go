package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

func newAccumulator(payload *kiroPayload) *responseAccumulator {
	return &responseAccumulator{
		Model:                payload.ConversationState.CurrentMessage.UserInputMessage.ModelID,
		ID:                   "msg_" + strings.ReplaceAll(randomUUID(), "-", ""),
		ToolNames:            payload.ToolNameMap,
		EstimatedInputTokens: payload.EstimatedInputTokens,
	}
}

func (a *responseAccumulator) accept(event kiroEvent) ([]claudeContentBlock, error) {
	a.InputTokens, a.OutputTokens = updateUsage(event.Payload, a.InputTokens, a.OutputTokens)
	switch event.Type {
	case "assistantResponseEvent":
		if text := firstStringField(event.Payload, "content", "text"); text != "" {
			block := claudeContentBlock{Type: "text", Text: text}
			a.appendFragment(block)
			return []claudeContentBlock{block}, nil
		}
	case "reasoningContentEvent":
		// Kiro does not return an Anthropic-verifiable signature. Omit its
		// reasoning instead of emitting a Claude thinking block that clients
		// cannot safely replay.
		return nil, nil
	case "toolUseEvent":
		tools, errTools := a.pendingTools.accept(event.Payload)
		if errTools != nil {
			return nil, errTools
		}
		blocks := make([]claudeContentBlock, 0, len(tools))
		for _, tool := range tools {
			name := tool.Name
			if original := a.ToolNames[name]; original != "" {
				name = original
			}
			block := claudeContentBlock{Type: "tool_use", ID: tool.ToolUseID, Name: name, Input: tool.Input}
			a.Blocks = append(a.Blocks, block)
			blocks = append(blocks, block)
		}
		return blocks, nil
	case "metadataEvent":
		if reason := firstStringField(event.Payload, "stopReason", "stop_reason"); reason != "" {
			a.StopReason = mapStopReason(reason)
		}
	case "meteringEvent":
		if credits, ok := event.Payload["usage"].(float64); ok {
			a.Credits += credits
		}
	case "contextUsageEvent":
		if percentage, ok := readFloat(event.Payload, "contextUsagePercentage", "context_usage_percentage"); ok && percentage > 0 {
			a.ContextUsagePercentage = percentage
		}
	}
	return nil, nil
}

func (a *responseAccumulator) appendFragment(block claudeContentBlock) {
	if len(a.Blocks) > 0 {
		last := &a.Blocks[len(a.Blocks)-1]
		if last.Type == block.Type {
			switch block.Type {
			case "text":
				last.Text += block.Text
				return
			case "thinking":
				last.Thinking += block.Thinking
				return
			}
		}
	}
	a.Blocks = append(a.Blocks, block)
}

func (a *responseAccumulator) finish() error {
	tools, errTools := a.pendingTools.flush()
	if errTools != nil {
		return errTools
	}
	for _, tool := range tools {
		name := tool.Name
		if original := a.ToolNames[name]; original != "" {
			name = original
		}
		a.Blocks = append(a.Blocks, claudeContentBlock{Type: "tool_use", ID: tool.ToolUseID, Name: name, Input: tool.Input})
	}
	if len(a.Blocks) == 0 {
		return fmt.Errorf("Kiro stream ended before producing output")
	}
	if a.StopReason == "" {
		for _, block := range a.Blocks {
			if block.Type == "tool_use" {
				a.StopReason = "tool_use"
				break
			}
		}
		if a.StopReason == "" {
			a.StopReason = "end_turn"
		}
	}
	if a.InputTokens <= 0 {
		a.InputTokens = a.currentInputTokens()
	}
	if a.OutputTokens <= 0 {
		a.OutputTokens = estimateClaudeOutputTokens(a.Blocks)
	}
	return nil
}

func (a *responseAccumulator) currentInputTokens() int {
	if a.InputTokens > 0 {
		return a.InputTokens
	}
	if a.ContextUsagePercentage > 0 {
		return int(a.ContextUsagePercentage * float64(contextWindowTokens(a.Model)) / 100)
	}
	return a.EstimatedInputTokens
}

func (a *responseAccumulator) responseJSON() ([]byte, error) {
	return json.Marshal(claudeResponse{
		ID:           a.ID,
		Type:         "message",
		Role:         "assistant",
		Content:      a.Blocks,
		Model:        a.Model,
		StopReason:   a.StopReason,
		StopSequence: nil,
		Usage:        claudeUsage{InputTokens: a.InputTokens, OutputTokens: a.OutputTokens},
	})
}

func mapStopReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "max_tokens", "max-tokens", "max tokens", "length", "max_tokens_reached":
		return "max_tokens"
	case "tool_use", "tool-use", "tool use", "tool_call":
		return "tool_use"
	case "stop_sequence", "stop-sequence":
		return "stop_sequence"
	default:
		return "end_turn"
	}
}

type claudeSSEWriter struct {
	accumulator *responseAccumulator
	started     bool
	blockIndex  int
	openType    string
	openIndex   int
}

func newClaudeSSEWriter(accumulator *responseAccumulator) *claudeSSEWriter {
	return &claudeSSEWriter{accumulator: accumulator}
}

func (w *claudeSSEWriter) start() ([][]byte, error) {
	if w.started {
		return nil, nil
	}
	w.started = true
	message := map[string]any{
		"id": w.accumulator.ID, "type": "message", "role": "assistant", "model": w.accumulator.Model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": w.accumulator.currentInputTokens(), "output_tokens": 0},
	}
	return marshalSSE("message_start", map[string]any{"type": "message_start", "message": message})
}

func (w *claudeSSEWriter) blocks(blocks []claudeContentBlock) ([][]byte, error) {
	var frames [][]byte
	for _, block := range blocks {
		switch block.Type {
		case "thinking":
			blockFrames, errBlock := w.fragment("thinking", map[string]any{"type": "thinking", "thinking": ""}, map[string]any{"type": "thinking_delta", "thinking": block.Thinking})
			if errBlock != nil {
				return nil, errBlock
			}
			frames = append(frames, blockFrames...)
		case "tool_use":
			closeFrames, errClose := w.closeOpenBlock()
			if errClose != nil {
				return nil, errClose
			}
			frames = append(frames, closeFrames...)

			index := w.blockIndex
			w.blockIndex++
			contentBlock := map[string]any{"type": "tool_use", "id": block.ID, "name": block.Name, "input": map[string]any{}}
			inputJSON, errMarshal := json.Marshal(block.Input)
			if errMarshal != nil {
				return nil, errMarshal
			}
			delta := map[string]any{"type": "input_json_delta", "partial_json": string(inputJSON)}
			blockFrames, errBlock := marshalCompleteContentBlock(index, contentBlock, delta)
			if errBlock != nil {
				return nil, errBlock
			}
			frames = append(frames, blockFrames...)
		default:
			blockFrames, errBlock := w.fragment("text", map[string]any{"type": "text", "text": ""}, map[string]any{"type": "text_delta", "text": block.Text})
			if errBlock != nil {
				return nil, errBlock
			}
			frames = append(frames, blockFrames...)
		}
	}
	return frames, nil
}

func (w *claudeSSEWriter) fragment(blockType string, contentBlock, delta any) ([][]byte, error) {
	var frames [][]byte
	if w.openType != blockType {
		closeFrames, errClose := w.closeOpenBlock()
		if errClose != nil {
			return nil, errClose
		}
		frames = append(frames, closeFrames...)
		w.openType = blockType
		w.openIndex = w.blockIndex
		w.blockIndex++
		startFrames, errStart := marshalSSE("content_block_start", map[string]any{"type": "content_block_start", "index": w.openIndex, "content_block": contentBlock})
		if errStart != nil {
			return nil, errStart
		}
		frames = append(frames, startFrames...)
	}
	deltaFrames, errDelta := marshalSSE("content_block_delta", map[string]any{"type": "content_block_delta", "index": w.openIndex, "delta": delta})
	if errDelta != nil {
		return nil, errDelta
	}
	return append(frames, deltaFrames...), nil
}

func (w *claudeSSEWriter) closeOpenBlock() ([][]byte, error) {
	if w.openType == "" {
		return nil, nil
	}
	index := w.openIndex
	w.openType = ""
	return marshalSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
}

func marshalCompleteContentBlock(index int, contentBlock, delta any) ([][]byte, error) {
	startFrames, errStart := marshalSSE("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": contentBlock})
	if errStart != nil {
		return nil, errStart
	}
	deltaFrames, errDelta := marshalSSE("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": delta})
	if errDelta != nil {
		return nil, errDelta
	}
	frames := append(startFrames, deltaFrames...)
	stopFrames, errStop := marshalSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
	if errStop != nil {
		return nil, errStop
	}
	return append(frames, stopFrames...), nil
}

func (w *claudeSSEWriter) finish() ([][]byte, error) {
	closeFrames, errClose := w.closeOpenBlock()
	if errClose != nil {
		return nil, errClose
	}
	deltaFrames, errDelta := marshalSSE("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": w.accumulator.StopReason, "stop_sequence": nil},
		"usage": map[string]int{"input_tokens": w.accumulator.InputTokens, "output_tokens": w.accumulator.OutputTokens},
	})
	if errDelta != nil {
		return nil, errDelta
	}
	stopFrames, errStop := marshalSSE("message_stop", map[string]any{"type": "message_stop"})
	if errStop != nil {
		return nil, errStop
	}
	frames := append(closeFrames, deltaFrames...)
	return append(frames, stopFrames...), nil
}

func marshalSSE(event string, value any) ([][]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return [][]byte{[]byte("event: " + event + "\ndata: " + string(raw) + "\n\n")}, nil
}
