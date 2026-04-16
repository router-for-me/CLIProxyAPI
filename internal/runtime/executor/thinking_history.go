package executor

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func normalizeThinkingHistory(body []byte, provider string) ([]byte, bool, bool, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return normalizeOpenAIThinkingHistory(body)
	case "claude":
		return normalizeClaudeThinkingHistory(body)
	default:
		return body, false, false, nil
	}
}

func normalizeOpenAIThinkingHistory(body []byte) ([]byte, bool, bool, error) {
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
		toolCalls := msg.Get("tool_calls")
		if !toolCalls.Exists() || !toolCalls.IsArray() || len(toolCalls.Array()) == 0 {
			continue
		}
		if reasoning != "" {
			continue
		}
		fallback := latestReasoning
		if fallback == "" {
			fallback = assistantOpenAIText(msg)
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

func normalizeClaudeThinkingHistory(body []byte) ([]byte, bool, bool, error) {
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
		if !content.Exists() || !content.IsArray() {
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
		if !hasToolUse || hasThinking {
			continue
		}
		fallback := latestThinking
		if fallback == "" && len(textParts) > 0 {
			fallback = strings.Join(textParts, "\n")
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
