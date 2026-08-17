package promptcompat

import (
	"fmt"
	"strings"
)

func ResponsesMessagesFromRequest(req map[string]any) []any {
	if msgs, ok := req["messages"].([]any); ok && len(msgs) > 0 {
		return prependInstructionMessage(msgs, req["instructions"])
	}
	if rawInput, ok := req["input"]; ok {
		if msgs := NormalizeResponsesInputAsMessages(rawInput); len(msgs) > 0 {
			return prependInstructionMessage(msgs, req["instructions"])
		}
	}
	return nil
}

func prependInstructionMessage(messages []any, instructions any) []any {
	sys, _ := instructions.(string)
	sys = strings.TrimSpace(sys)
	if sys == "" {
		return messages
	}
	out := make([]any, 0, len(messages)+1)
	out = append(out, map[string]any{"role": "system", "content": sys})
	out = append(out, messages...)
	return out
}

func NormalizeResponsesInputAsMessages(input any) []any {
	switch v := input.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []any{map[string]any{"role": "user", "content": v}}
	case []any:
		return normalizeResponsesInputArray(v)
	case map[string]any:
		if msg := normalizeResponsesInputItem(v); msg != nil {
			return []any{msg}
		}
		if txt, _ := v["text"].(string); strings.TrimSpace(txt) != "" {
			return []any{map[string]any{"role": "user", "content": txt}}
		}
		if content, ok := v["content"]; ok {
			if strings.TrimSpace(NormalizeOpenAIContentForPrompt(content)) != "" {
				return []any{map[string]any{"role": "user", "content": content}}
			}
		}
	}
	return nil
}

func normalizeResponsesInputArray(items []any) []any {
	if len(items) == 0 {
		return nil
	}
	out := make([]any, 0, len(items))
	callNameByID := map[string]string{}
	fallbackParts := make([]string, 0, len(items))
	pendingAssistantReasoning := ""
	flushFallback := func() {
		if len(fallbackParts) == 0 {
			return
		}
		if pendingAssistantReasoning != "" {
			out = append(out, map[string]any{"role": "assistant", "reasoning_content": pendingAssistantReasoning})
			pendingAssistantReasoning = ""
		}
		out = append(out, map[string]any{"role": "user", "content": strings.Join(fallbackParts, "\n")})
		fallbackParts = fallbackParts[:0]
	}
	flushPendingReasoning := func() {
		if pendingAssistantReasoning == "" {
			return
		}
		out = append(out, map[string]any{"role": "assistant", "reasoning_content": pendingAssistantReasoning})
		pendingAssistantReasoning = ""
	}

	for _, item := range items {
		switch x := item.(type) {
		case map[string]any:
			if msg := normalizeResponsesInputItemWithState(x, callNameByID); msg != nil {
				if reasoning := assistantReasoningOnlyContent(msg); reasoning != "" {
					if pendingAssistantReasoning == "" {
						pendingAssistantReasoning = reasoning
					} else {
						pendingAssistantReasoning += "\n" + reasoning
					}
					continue
				}
				if isAssistantToolCallMessage(msg) && pendingAssistantReasoning != "" {
					if strings.TrimSpace(normalizeOpenAIReasoningContentForPrompt(msg["reasoning_content"])) == "" {
						msg["reasoning_content"] = pendingAssistantReasoning
					}
					pendingAssistantReasoning = ""
				} else {
					flushPendingReasoning()
				}
				flushFallback()
				if isAssistantToolCallMessage(msg) && len(out) > 0 {
					if merged := mergeResponsesAssistantToolCalls(out[len(out)-1], msg); merged {
						continue
					}
				}
				out = append(out, msg)
				continue
			}
			if s := normalizeResponsesFallbackPart(x); s != "" {
				fallbackParts = append(fallbackParts, s)
			}
		default:
			if s := strings.TrimSpace(fmt.Sprintf("%v", item)); s != "" {
				fallbackParts = append(fallbackParts, s)
			}
		}
	}
	flushPendingReasoning()
	flushFallback()
	if len(out) == 0 {
		return nil
	}
	return out
}

func assistantReasoningOnlyContent(msg map[string]any) string {
	if !isAssistantMessage(msg) || isAssistantToolCallMessage(msg) {
		return ""
	}
	if _, hasContent := msg["content"]; hasContent {
		normalizedContent := strings.TrimSpace(NormalizeOpenAIContentForPrompt(msg["content"]))
		reasoningFromContent := strings.TrimSpace(extractOpenAIReasoningContentFromMessage(msg["content"]))
		if normalizedContent != "" && normalizedContent != reasoningFromContent {
			return ""
		}
		if reasoningFromContent != "" {
			return reasoningFromContent
		}
	}
	return strings.TrimSpace(normalizeOpenAIReasoningContentForPrompt(msg["reasoning_content"]))
}

func isAssistantMessage(msg map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(asString(msg["role"])), "assistant")
}

func isAssistantToolCallMessage(msg map[string]any) bool {
	if !isAssistantMessage(msg) {
		return false
	}
	toolCalls, ok := msg["tool_calls"].([]any)
	return ok && len(toolCalls) > 0
}

func mergeResponsesAssistantToolCalls(prev any, next map[string]any) bool {
	prevMsg, ok := prev.(map[string]any)
	if !ok || !isAssistantToolCallMessage(prevMsg) || !isAssistantToolCallMessage(next) {
		return false
	}
	prevCalls, _ := prevMsg["tool_calls"].([]any)
	nextCalls, _ := next["tool_calls"].([]any)
	prevMsg["tool_calls"] = append(prevCalls, nextCalls...)
	if strings.TrimSpace(normalizeOpenAIReasoningContentForPrompt(prevMsg["reasoning_content"])) == "" {
		if reasoning := strings.TrimSpace(normalizeOpenAIReasoningContentForPrompt(next["reasoning_content"])); reasoning != "" {
			prevMsg["reasoning_content"] = reasoning
		}
	}
	return true
}
