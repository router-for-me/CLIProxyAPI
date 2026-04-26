package requestinvariants

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const AssistantReasoningPlaceholder = "[reasoning unavailable]"

// NormalizeOpenAIChatToolCallReasoning patches assistant tool-call messages with
// missing reasoning_content in OpenAI chat payloads.
func NormalizeOpenAIChatToolCallReasoning(body []byte, requireThinkingEnabled bool) ([]byte, int, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, 0, nil
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, 0, nil
	}
	if requireThinkingEnabled && !OpenAIChatReasoningEnabled(body) {
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
			reasoningText := strings.TrimSpace(reasoning.String())
			if reasoningText != "" {
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

		reasoningText := FallbackAssistantReasoningFromOpenAIMessage(msg, hasLatestReasoning, latestReasoning)
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

// NormalizeClaudeMessagesToolUseReasoningPrefix ensures thinking-enabled Claude
// assistant tool_use messages start with a readable reasoning prefix when they
// lack an explicit thinking block or existing text prefix.
func NormalizeClaudeMessagesToolUseReasoningPrefix(body []byte, requireThinkingEnabled bool) ([]byte, int, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, 0, nil
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, 0, nil
	}
	if requireThinkingEnabled && !ClaudeMessagesThinkingEnabled(body) {
		return body, 0, nil
	}

	out := body
	patched := 0
	latestReasoning := ""

	for msgIdx, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}

		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			continue
		}

		explicitThinking := extractClaudeThinkingText(content)
		if explicitThinking != "" {
			latestReasoning = explicitThinking
		}

		hasToolUse := false
		existingPrefix := ""
		visibleText := make([]string, 0, len(content.Array()))
		firstToolUseIdx := -1

		for idx, item := range content.Array() {
			itemType := strings.TrimSpace(item.Get("type").String())
			if itemType == "tool_use" {
				hasToolUse = true
				if firstToolUseIdx == -1 {
					firstToolUseIdx = idx
				}
				continue
			}
			if itemType != "text" {
				continue
			}
			text := strings.TrimSpace(item.Get("text").String())
			if text == "" {
				continue
			}
			visibleText = append(visibleText, text)
			if firstToolUseIdx == -1 {
				existingPrefix = text
			}
		}

		if !hasToolUse {
			continue
		}
		if explicitThinking != "" || existingPrefix != "" {
			if existingPrefix != "" {
				latestReasoning = existingPrefix
			}
			continue
		}

		reasoningText := FallbackAssistantReasoning(strings.Join(visibleText, "\n"), latestReasoning)
		next, err := prependClaudeAssistantTextPrefix(out, msgIdx, reasoningText)
		if err != nil {
			return body, patched, err
		}
		out = next
		patched++
		latestReasoning = reasoningText
	}

	return out, patched, nil
}

func OpenAIChatReasoningEnabled(body []byte) bool {
	if hasNonEmptyJSONPath(body, "reasoning_effort") {
		return true
	}
	return hasNonEmptyJSONPath(body, "reasoning.effort")
}

func ClaudeMessagesThinkingEnabled(body []byte) bool {
	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
	if thinkingType == "" || thinkingType == "disabled" {
		return false
	}
	return thinkingType == "enabled" || thinkingType == "adaptive" || thinkingType == "auto"
}

func FallbackAssistantReasoning(visibleText string, latest string) string {
	if text := strings.TrimSpace(visibleText); text != "" {
		return text
	}
	if text := strings.TrimSpace(latest); text != "" {
		return text
	}
	return AssistantReasoningPlaceholder
}

func FallbackAssistantReasoningFromOpenAIMessage(msg gjson.Result, hasLatest bool, latest string) string {
	if hasLatest && strings.TrimSpace(latest) != "" {
		return latest
	}

	content := msg.Get("content")
	if content.Type == gjson.String {
		if text := strings.TrimSpace(content.String()); text != "" {
			return text
		}
	}
	if content.IsArray() {
		parts := make([]string, 0, len(content.Array()))
		for _, item := range content.Array() {
			text := strings.TrimSpace(item.Get("text").String())
			if text == "" {
				continue
			}
			parts = append(parts, text)
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}

	return AssistantReasoningPlaceholder
}

func prependClaudeAssistantTextPrefix(body []byte, msgIdx int, reasoningText string) ([]byte, error) {
	reasoningText = strings.TrimSpace(reasoningText)
	if reasoningText == "" {
		return body, nil
	}

	reasoningPart := []byte(`{"type":"text","text":""}`)
	reasoningPart, _ = sjson.SetBytes(reasoningPart, "text", reasoningText)

	currentPath := fmt.Sprintf("messages.%d.content", msgIdx)
	current := gjson.GetBytes(body, currentPath)
	if current.Exists() && current.IsArray() {
		items := current.Array()
		if len(items) > 0 {
			first := items[0]
			if first.Get("type").String() == "text" && strings.TrimSpace(first.Get("text").String()) == reasoningText {
				return body, nil
			}
		}
	}

	newContent := []byte(`[]`)
	newContent, _ = sjson.SetRawBytes(newContent, "-1", reasoningPart)
	if current.Exists() && current.IsArray() {
		current.ForEach(func(_, item gjson.Result) bool {
			newContent, _ = sjson.SetRawBytes(newContent, "-1", []byte(item.Raw))
			return true
		})
	}

	return sjson.SetRawBytes(body, currentPath, newContent)
}

func extractClaudeThinkingText(content gjson.Result) string {
	if !content.Exists() || !content.IsArray() {
		return ""
	}

	parts := make([]string, 0, len(content.Array()))
	content.ForEach(func(_, item gjson.Result) bool {
		if strings.TrimSpace(item.Get("type").String()) != "thinking" {
			return true
		}
		text := strings.TrimSpace(item.Get("thinking").String())
		if text == "" {
			text = strings.TrimSpace(item.Get("text").String())
		}
		if text != "" {
			parts = append(parts, text)
		}
		return true
	})
	return strings.Join(parts, "\n\n")
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
