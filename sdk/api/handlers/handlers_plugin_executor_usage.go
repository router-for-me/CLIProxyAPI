package handlers

import (
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func parsePluginExecutorResponseUsage(protocol string, payload []byte) usage.Detail {
	if len(payload) == 0 {
		return usage.Detail{}
	}
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "claude":
		return parseClaudePayloadUsage(payload)
	case "gemini":
		return helps.ParseGeminiUsage(payload)
	case "interactions", "interactions-response":
		return helps.ParseInteractionsUsage(payload)
	case "antigravity":
		return helps.ParseAntigravityUsage(payload)
	case "codex", "openai-response":
		if detail, ok := helps.ParseCodexUsage(payload); ok {
			return detail
		}
		return helps.ParseOpenAIUsage(payload)
	default:
		return helps.ParseOpenAIUsage(payload)
	}
}

func observePluginExecutorStreamUsage(protocol string, payload []byte, buffer *helps.StreamUsageBuffer) {
	if buffer == nil || len(payload) == 0 {
		return
	}
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "claude":
		iterateStreamLines(payload, func(line []byte) {
			if detail, ok := parseClaudeStreamLine(line); ok {
				observeMergedStreamUsage(buffer, detail)
			}
		})
	case "gemini":
		iterateStreamLines(payload, func(line []byte) {
			if detail, ok := helps.ParseGeminiStreamUsage(line); ok {
				buffer.Observe(detail, ok)
			}
		})
	case "interactions", "interactions-response":
		iterateStreamLines(payload, func(line []byte) {
			if detail, ok := helps.ParseInteractionsStreamUsage(line); ok {
				observeMergedStreamUsage(buffer, detail)
			}
		})
	case "antigravity":
		iterateStreamLines(payload, func(line []byte) {
			if detail, ok := helps.ParseAntigravityStreamUsage(line); ok {
				buffer.Observe(detail, ok)
			}
		})
	case "codex", "openai-response":
		iterateStreamLines(payload, func(line []byte) {
			if jsonBytes := extractStreamJSONPayload(line); len(jsonBytes) > 0 {
				if detail, ok := helps.ParseCodexUsage(jsonBytes); ok {
					buffer.Observe(detail, ok)
					return
				}
			}
			buffer.ObserveOpenAIStream(line)
		})
	default:
		iterateStreamLines(payload, func(line []byte) {
			buffer.ObserveOpenAIStream(line)
		})
	}
}

func parseClaudePayloadUsage(payload []byte) usage.Detail {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		usageNode = gjson.GetBytes(payload, "message.usage")
	}
	if !usageNode.Exists() {
		return usage.Detail{}
	}
	return helps.ParseClaudeUsage([]byte(`{"usage":` + usageNode.Raw + `}`))
}

func parseClaudeStreamLine(line []byte) (usage.Detail, bool) {
	payload := extractStreamJSONPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		usageNode = gjson.GetBytes(payload, "message.usage")
	}
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	detail := helps.ParseClaudeUsage([]byte(`{"usage":` + usageNode.Raw + `}`))
	return detail, true
}

func observeMergedStreamUsage(buffer *helps.StreamUsageBuffer, update usage.Detail) {
	if buffer == nil {
		return
	}
	if existing, ok := buffer.Detail(); ok {
		merged := mergeStreamUsageDetail(existing, update)
		buffer.Observe(merged, true)
		return
	}
	buffer.Observe(update, true)
}

func mergeStreamUsageDetail(existing, update usage.Detail) usage.Detail {
	merged := update
	if merged.InputTokens == 0 && existing.InputTokens > 0 {
		merged.InputTokens = existing.InputTokens
	}
	if merged.CachedTokens == 0 && existing.CachedTokens > 0 {
		merged.CachedTokens = existing.CachedTokens
	}
	if merged.CacheReadTokens == 0 && existing.CacheReadTokens > 0 {
		merged.CacheReadTokens = existing.CacheReadTokens
	}
	if merged.CacheCreationTokens == 0 && existing.CacheCreationTokens > 0 {
		merged.CacheCreationTokens = existing.CacheCreationTokens
	}
	if merged.OutputTokens == 0 && existing.OutputTokens > 0 {
		merged.OutputTokens = existing.OutputTokens
	}
	if merged.ReasoningTokens == 0 && existing.ReasoningTokens > 0 {
		merged.ReasoningTokens = existing.ReasoningTokens
	}
	if merged.ResponseServiceTier == "" {
		merged.ResponseServiceTier = existing.ResponseServiceTier
	}
	cached := merged.CacheReadTokens + merged.CacheCreationTokens
	if cached == 0 {
		cached = merged.CachedTokens
	}
	calculatedTotal := merged.InputTokens + merged.OutputTokens + cached
	if merged.TotalTokens == 0 || merged.TotalTokens < calculatedTotal {
		merged.TotalTokens = calculatedTotal
	}
	nonReasoningOutput := merged.OutputTokens - merged.ReasoningTokens
	if nonReasoningOutput < 0 {
		nonReasoningOutput = 0
	}
	merged.TokenBreakdown = usage.NewIndependentTokenBreakdown(
		merged.InputTokens,
		merged.CacheReadTokens,
		merged.CacheCreationTokens,
		nonReasoningOutput,
		merged.ReasoningTokens,
		merged.TotalTokens,
	)
	return merged
}

func iterateStreamLines(payload []byte, fn func(line []byte)) {
	for _, line := range bytes.Split(payload, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		fn(trimmed)
	}
}

func extractStreamJSONPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	}
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	return trimmed
}
