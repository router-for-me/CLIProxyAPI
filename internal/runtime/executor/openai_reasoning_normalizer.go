package executor

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeAssistantToolCallReasoningContent patches assistant tool-call messages
// with missing reasoning_content in OpenAI chat payloads.
//
// When requireThinkingEnabled is true, patching only happens if the payload
// explicitly enables reasoning.
func normalizeAssistantToolCallReasoningContent(body []byte, requireThinkingEnabled bool) ([]byte, int, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, 0, nil
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, 0, nil
	}
	if requireThinkingEnabled && !openAIChatReasoningEnabled(body) {
		return body, 0, nil
	}

	out := body
	patched := 0
	latestReasoning := ""
	hasLatestReasoning := false

	for msgIdx, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}

		reasoning := msg.Get("reasoning_content")
		if reasoning.Exists() {
			reasoningText := reasoning.String()
			if strings.TrimSpace(reasoningText) != "" {
				latestReasoning = reasoningText
				hasLatestReasoning = true
			}
		}

		toolCalls := msg.Get("tool_calls")
		if !toolCalls.Exists() || !toolCalls.IsArray() || len(toolCalls.Array()) == 0 {
			continue
		}
		if reasoning.Exists() && strings.TrimSpace(reasoning.String()) != "" {
			continue
		}

		reasoningText := fallbackAssistantReasoning(msg, hasLatestReasoning, latestReasoning)
		path := fmt.Sprintf("messages.%d.reasoning_content", msgIdx)
		next, err := sjson.SetBytes(out, path, reasoningText)
		if err != nil {
			return body, patched, err
		}
		out = next
		patched++
		if strings.TrimSpace(reasoningText) != "" {
			latestReasoning = reasoningText
			hasLatestReasoning = true
		}
	}

	return out, patched, nil
}

func openAIChatReasoningEnabled(body []byte) bool {
	if hasNonEmptyJSONPath(body, "reasoning_effort") {
		return true
	}
	return hasNonEmptyJSONPath(body, "reasoning.effort")
}

func hasNonEmptyJSONPath(body []byte, path string) bool {
	value := gjson.GetBytes(body, path)
	if !value.Exists() {
		return false
	}
	switch value.Type {
	case gjson.String:
		return strings.TrimSpace(value.String()) != ""
	default:
		raw := strings.TrimSpace(value.Raw)
		return raw != "" && raw != "null"
	}
}
