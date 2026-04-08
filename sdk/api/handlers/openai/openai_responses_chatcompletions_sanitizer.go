package openai

import (
	"bytes"
	"encoding/json"
)

// sanitizeConvertedResponsesChatCompletions normalizes chat-completions payloads
// produced from /v1/responses-style inputs before they are forwarded upstream.
//
// Some Responses conversations can contain non-tool messages between an
// assistant tool call and the matching tool result. OpenAI-compatible
// chat-completions backends expect every pending tool call to be satisfied by
// tool messages before any other role appears. This sanitizer keeps tool-call
// chains contiguous by buffering interleaved non-tool messages until all
// pending tool results arrive. It also merges consecutive assistant tool-call
// messages into a single assistant turn so parallel calls stay valid for strict
// chat-completions executors.
func sanitizeConvertedResponsesChatCompletions(rawJSON []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(rawJSON))
	decoder.UseNumber()

	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return rawJSON
	}

	rawMessages, ok := payload["messages"].([]any)
	if !ok || len(rawMessages) == 0 {
		return rawJSON
	}

	messages, changed := normalizeChatCompletionsToolMessageOrder(rawMessages)
	if !changed {
		return rawJSON
	}

	payload["messages"] = messages
	out, err := json.Marshal(payload)
	if err != nil {
		return rawJSON
	}
	return out
}

func normalizeChatCompletionsToolMessageOrder(messages []any) ([]any, bool) {
	remaining := append([]any(nil), messages...)
	normalized := make([]any, 0, len(messages))
	buffered := make([]any, 0)
	pendingToolCalls := make(map[string]struct{})
	lastAssistantToolIndex := -1
	changed := false

	prependBuffered := func() {
		if len(buffered) == 0 {
			return
		}
		remaining = append(append(make([]any, 0, len(buffered)+len(remaining)), buffered...), remaining...)
		buffered = buffered[:0]
		lastAssistantToolIndex = -1
	}

	for len(remaining) > 0 {
		if len(pendingToolCalls) == 0 && len(buffered) > 0 {
			prependBuffered()
		}

		rawMessage := remaining[0]
		remaining = remaining[1:]

		message, ok := rawMessage.(map[string]any)
		if !ok {
			if len(pendingToolCalls) > 0 {
				buffered = append(buffered, rawMessage)
				changed = true
			} else {
				normalized = append(normalized, rawMessage)
				lastAssistantToolIndex = -1
			}
			continue
		}

		role, _ := message["role"].(string)
		toolCalls := messageToolCalls(message)

		if role == "assistant" && len(toolCalls) > 0 {
			if canMergeAdjacentAssistantToolCalls(pendingToolCalls, lastAssistantToolIndex, normalized) {
				if previous, ok := normalized[lastAssistantToolIndex].(map[string]any); ok {
					previous["tool_calls"] = append(messageToolCalls(previous), toolCalls...)
					mergeAssistantContent(previous, message)
					addPendingToolCallIDs(pendingToolCalls, toolCalls)
					changed = true
					continue
				}
			}
			if len(pendingToolCalls) > 0 {
				buffered = append(buffered, rawMessage)
				changed = true
				continue
			}

			normalized = append(normalized, message)
			lastAssistantToolIndex = len(normalized) - 1
			addPendingToolCallIDs(pendingToolCalls, toolCalls)
			continue
		}

		if role == "tool" {
			toolCallID, _ := message["tool_call_id"].(string)
			if len(pendingToolCalls) > 0 && len(buffered) > 0 {
				if toolCallID == "" {
					buffered = append(buffered, rawMessage)
					changed = true
					continue
				}
				if _, ok := pendingToolCalls[toolCallID]; !ok {
					buffered = append(buffered, rawMessage)
					changed = true
					continue
				}
			}

			normalized = append(normalized, message)
			if toolCallID != "" {
				delete(pendingToolCalls, toolCallID)
			}
			if len(pendingToolCalls) == 0 {
				lastAssistantToolIndex = -1
			}
			continue
		}

		if len(pendingToolCalls) > 0 {
			buffered = append(buffered, message)
			changed = true
			continue
		}

		normalized = append(normalized, message)
		lastAssistantToolIndex = -1
	}

	if len(buffered) > 0 {
		normalized = append(normalized, buffered...)
	}

	return normalized, changed
}

func messageToolCalls(message map[string]any) []any {
	raw, ok := message["tool_calls"].([]any)
	if !ok {
		return nil
	}
	return raw
}

func canMergeAdjacentAssistantToolCalls(pendingToolCalls map[string]struct{}, lastAssistantToolIndex int, normalized []any) bool {
	if len(pendingToolCalls) == 0 || lastAssistantToolIndex < 0 || lastAssistantToolIndex >= len(normalized) {
		return false
	}
	return lastAssistantToolIndex == len(normalized)-1
}

func addPendingToolCallIDs(pending map[string]struct{}, toolCalls []any) {
	for _, rawToolCall := range toolCalls {
		toolCall, ok := rawToolCall.(map[string]any)
		if !ok {
			continue
		}
		id, _ := toolCall["id"].(string)
		if id == "" {
			continue
		}
		pending[id] = struct{}{}
	}
}

func mergeAssistantContent(dst, src map[string]any) {
	srcContent, srcExists := src["content"]
	if !srcExists || isEmptyMessageContent(srcContent) {
		return
	}

	dstContent, dstExists := dst["content"]
	if !dstExists || isEmptyMessageContent(dstContent) {
		dst["content"] = srcContent
		return
	}

	dstParts, dstPartsOK := dstContent.([]any)
	srcParts, srcPartsOK := srcContent.([]any)
	if dstPartsOK && srcPartsOK {
		dst["content"] = append(dstParts, srcParts...)
		return
	}

	dstText, dstTextOK := dstContent.(string)
	srcText, srcTextOK := srcContent.(string)
	if dstTextOK && srcTextOK {
		switch {
		case dstText == "":
			dst["content"] = srcText
		case srcText == "":
			// Keep dst unchanged.
		default:
			dst["content"] = dstText + "\n" + srcText
		}
		return
	}

	if mergedParts, ok := mergeMessageContentAsParts(dstContent, srcContent); ok {
		dst["content"] = mergedParts
	}
}

func mergeMessageContentAsParts(dstContent, srcContent any) ([]any, bool) {
	dstParts, dstOK := messageContentAsParts(dstContent)
	srcParts, srcOK := messageContentAsParts(srcContent)
	if !dstOK || !srcOK {
		return nil, false
	}
	return append(dstParts, srcParts...), true
}

func messageContentAsParts(content any) ([]any, bool) {
	switch v := content.(type) {
	case nil:
		return nil, false
	case string:
		if v == "" {
			return []any{}, true
		}
		return []any{
			map[string]any{
				"type": "text",
				"text": v,
			},
		}, true
	case []any:
		return append([]any{}, v...), true
	default:
		return nil, false
	}
}

func isEmptyMessageContent(content any) bool {
	switch v := content.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	default:
		return false
	}
}
