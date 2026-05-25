package executor

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func normalizeThinkingHistory(body []byte, provider string) ([]byte, bool, bool, error) {
	return normalizeThinkingHistoryForModel(body, provider, "")
}

func normalizeThinkingHistoryForModel(body []byte, provider string, model string) ([]byte, bool, bool, error) {
	requireCompleteHistory := requiresReturnedThinkingHistory(model)
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return normalizeOpenAIThinkingHistory(body, requireCompleteHistory)
	case "claude":
		return normalizeClaudeThinkingHistory(body, requireCompleteHistory)
	default:
		return body, false, false, nil
	}
}

func requiresReturnedThinkingHistory(model string) bool {
	modelName := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	return strings.HasPrefix(modelName, "deepseek-v4") || strings.Contains(modelName, "deepseek-reasoner")
}

func normalizeOpenAIThinkingHistory(body []byte, requireCompleteHistory bool) ([]byte, bool, bool, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, false, false, nil
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, false, false, nil
	}

	out := body
	latestReasoning := ""
	patched := 0
	unrepaired := 0

	for idx, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}
		reasoning := strings.TrimSpace(msg.Get("reasoning_content").String())
		if reasoning != "" {
			latestReasoning = reasoning
		}
		hasToolCalls := false
		if toolCalls := msg.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
			hasToolCalls = true
		}
		if !requireCompleteHistory && !hasToolCalls {
			continue
		}
		if reasoning != "" {
			continue
		}
		fallback := latestReasoning
		if fallback == "" {
			fallback = assistantOpenAIText(msg)
		}
		if fallback == "" && requireCompleteHistory {
			fallback = "[reasoning unavailable]"
		}
		if fallback == "" {
			unrepaired++
			continue
		}
		next, err := sjson.SetBytes(out, fmt.Sprintf("messages.%d.reasoning_content", idx), fallback)
		if err != nil {
			return body, false, false, err
		}
		out = next
		latestReasoning = fallback
		patched++
	}

	downgraded := false
	if unrepaired > 0 && openAIThinkingEnabled(out) {
		out = thinking.StripThinkingConfig(out, "openai")
		downgraded = true
	}
	if patched > 0 || downgraded {
		log.WithFields(log.Fields{
			"patched_reasoning_messages": patched,
			"downgraded_thinking":        downgraded,
		}).Debug("executor: normalized openai thinking history")
	}
	return out, patched > 0 || downgraded, downgraded, nil
}

func normalizeClaudeThinkingHistory(body []byte, requireCompleteHistory bool) ([]byte, bool, bool, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, false, false, nil
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, false, false, nil
	}

	out := body
	latestThinking := ""
	patched := 0
	unrepaired := 0

	for idx, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}
		content := msg.Get("content")
		if !content.Exists() {
			continue
		}
		if content.Type == gjson.String {
			if !requireCompleteHistory {
				continue
			}
			fallback := latestThinking
			text := strings.TrimSpace(content.String())
			if fallback == "" {
				fallback = text
			}
			if fallback == "" {
				fallback = "[thinking unavailable]"
			}
			rebuilt := []byte(`[]`)
			block := []byte(`{"type":"thinking","thinking":""}`)
			block, _ = sjson.SetBytes(block, "thinking", fallback)
			rebuilt, _ = sjson.SetRawBytes(rebuilt, "-1", block)
			if text != "" {
				textBlock := []byte(`{"type":"text","text":""}`)
				textBlock, _ = sjson.SetBytes(textBlock, "text", text)
				rebuilt, _ = sjson.SetRawBytes(rebuilt, "-1", textBlock)
			}
			next, err := sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content", idx), rebuilt)
			if err != nil {
				return body, false, false, err
			}
			out = next
			latestThinking = fallback
			patched++
			continue
		}
		if !content.IsArray() {
			continue
		}

		hasToolUse := false
		hasThinking := false
		textParts := make([]string, 0, len(content.Array()))
		for _, part := range content.Array() {
			switch strings.TrimSpace(part.Get("type").String()) {
			case "thinking":
				thinkingText := strings.TrimSpace(part.Get("thinking").String())
				if thinkingText != "" {
					latestThinking = thinkingText
					hasThinking = true
				}
			case "text":
				text := strings.TrimSpace(part.Get("text").String())
				if text != "" {
					textParts = append(textParts, text)
				}
			case "tool_use":
				hasToolUse = true
			}
		}
		if hasThinking {
			continue
		}
		if !requireCompleteHistory && !hasToolUse {
			continue
		}
		fallback := latestThinking
		if fallback == "" && len(textParts) > 0 {
			fallback = strings.Join(textParts, "\n")
		}
		if fallback == "" && requireCompleteHistory {
			fallback = "[thinking unavailable]"
		}
		if fallback == "" {
			unrepaired++
			continue
		}
		block := []byte(`{"type":"thinking","thinking":""}`)
		block, _ = sjson.SetBytes(block, "thinking", fallback)
		rebuilt := []byte(`[]`)
		rebuilt, _ = sjson.SetRawBytes(rebuilt, "-1", block)
		for _, part := range content.Array() {
			rebuilt, _ = sjson.SetRawBytes(rebuilt, "-1", []byte(part.Raw))
		}
		next, err := sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content", idx), rebuilt)
		if err != nil {
			return body, false, false, err
		}
		out = next
		latestThinking = fallback
		patched++
	}

	downgraded := false
	if unrepaired > 0 && claudeThinkingEnabled(out) {
		out = thinking.StripThinkingConfig(out, "claude")
		downgraded = true
	}
	if patched > 0 || downgraded {
		log.WithFields(log.Fields{
			"patched_thinking_messages": patched,
			"downgraded_thinking":       downgraded,
		}).Debug("executor: normalized claude thinking history")
	}
	return out, patched > 0 || downgraded, downgraded, nil
}

func assistantOpenAIText(msg gjson.Result) string {
	content := msg.Get("content")
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	parts := make([]string, 0, len(content.Array()))
	for _, item := range content.Array() {
		text := strings.TrimSpace(item.Get("text").String())
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n")
}

func openAIThinkingEnabled(body []byte) bool {
	return strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String()) != ""
}

func claudeThinkingEnabled(body []byte) bool {
	thinkingType := strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String())
	if thinkingType != "" && thinkingType != "disabled" {
		return true
	}
	return strings.TrimSpace(gjson.GetBytes(body, "output_config.effort").String()) != ""
}
