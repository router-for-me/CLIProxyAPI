package helps

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// IsDeepSeekReasoningModel reports whether a model requires DeepSeek reasoning_content round-tripping.
func IsDeepSeekReasoningModel(modelName string) bool {
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if strings.HasSuffix(modelName, ")") {
		if idx := strings.LastIndex(modelName, "("); idx > 0 {
			modelName = strings.TrimSpace(modelName[:idx])
		}
	}
	switch modelName {
	case "deepseek-v4-pro", "deepseek-v4-flash":
		return true
	default:
		return false
	}
}

// PreserveDeepSeekReasoningContent restores Responses reasoning output items onto
// OpenAI Chat Completions assistant messages for DeepSeek V4 thinking models.
func PreserveDeepSeekReasoningContent(modelName string, translatedChatCompletions, sourceRawJSON []byte) []byte {
	if !IsDeepSeekReasoningModel(modelName) || len(translatedChatCompletions) == 0 || len(sourceRawJSON) == 0 {
		return translatedChatCompletions
	}
	if !gjson.ValidBytes(translatedChatCompletions) || !gjson.ValidBytes(sourceRawJSON) {
		return translatedChatCompletions
	}
	messages := gjson.GetBytes(translatedChatCompletions, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return translatedChatCompletions
	}

	targets := deepSeekReasoningTargetsByAssistantOrdinal(sourceRawJSON)
	if len(targets) == 0 {
		return translatedChatCompletions
	}

	out := bytes.Clone(translatedChatCompletions)
	assistantOrdinal := 0
	for messageIndex, message := range messages.Array() {
		if strings.TrimSpace(message.Get("role").String()) != "assistant" {
			continue
		}
		if reasoningContent := targets[assistantOrdinal]; strings.TrimSpace(reasoningContent) != "" {
			existing := message.Get("reasoning_content")
			if !existing.Exists() || strings.TrimSpace(existing.String()) == "" {
				updated, errSet := sjson.SetBytes(out, fmt.Sprintf("messages.%d.reasoning_content", messageIndex), reasoningContent)
				if errSet == nil {
					out = updated
				}
			}
		}
		assistantOrdinal++
	}
	return out
}

func deepSeekReasoningTargetsByAssistantOrdinal(sourceRawJSON []byte) map[int]string {
	input := gjson.GetBytes(sourceRawJSON, "input")
	if !input.Exists() || !input.IsArray() {
		return nil
	}

	targets := make(map[int]string)
	assistantOrdinal := 0
	pendingReasoning := ""
	inFunctionCallGroup := false

	consumePendingReasoning := func() {
		if strings.TrimSpace(pendingReasoning) != "" {
			targets[assistantOrdinal] = pendingReasoning
			pendingReasoning = ""
		}
	}

	for _, item := range input.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType == "" && strings.TrimSpace(item.Get("role").String()) != "" {
			itemType = "message"
		}
		if itemType != "function_call" {
			inFunctionCallGroup = false
		}

		switch itemType {
		case "reasoning":
			if text := responseReasoningItemText(item); strings.TrimSpace(text) != "" {
				pendingReasoning = appendReasoningContent(pendingReasoning, text)
			}
		case "function_call":
			if !inFunctionCallGroup {
				consumePendingReasoning()
				assistantOrdinal++
				inFunctionCallGroup = true
			}
		case "message", "":
			if strings.TrimSpace(item.Get("role").String()) == "assistant" {
				consumePendingReasoning()
				assistantOrdinal++
				continue
			}
			if pendingReasoning != "" {
				pendingReasoning = ""
			}
		case "function_call_output", "custom_tool_call_output":
			if pendingReasoning != "" {
				pendingReasoning = ""
			}
		}
	}

	if len(targets) == 0 {
		return nil
	}
	return targets
}

func responseReasoningItemText(item gjson.Result) string {
	parts := collectReasoningTextParts(item.Get("summary"))
	if len(parts) == 0 {
		parts = collectReasoningTextParts(item.Get("content"))
	}
	if len(parts) == 0 {
		for _, path := range []string{"text", "reasoning_content"} {
			if text := item.Get(path).String(); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func collectReasoningTextParts(value gjson.Result) []string {
	if !value.Exists() || !value.IsArray() {
		return nil
	}
	parts := make([]string, 0)
	for _, part := range value.Array() {
		if text := part.Get("text").String(); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return parts
}

func appendReasoningContent(existing, next string) string {
	if strings.TrimSpace(existing) == "" {
		return next
	}
	if strings.TrimSpace(next) == "" {
		return existing
	}
	return existing + "\n\n" + next
}
