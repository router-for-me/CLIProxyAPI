package helps

import (
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// ParsePluginExecutorResponseUsage extracts token usage from a non-streaming plugin executor response.
func ParsePluginExecutorResponseUsage(protocol string, payload []byte) usage.Detail {
	if len(payload) == 0 {
		return usage.Detail{}
	}
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "claude":
		return parseClaudePayloadUsage(payload)
	case "gemini":
		return ParseGeminiUsage(payload)
	case "interactions", "interactions-response":
		return ParseInteractionsUsage(payload)
	case "antigravity":
		return ParseAntigravityUsage(payload)
	case "codex", "openai-response":
		if detail, ok := ParseCodexUsage(payload); ok {
			return detail
		}
		return ParseOpenAIUsage(payload)
	default:
		return ParseOpenAIUsage(payload)
	}
}

// ObservePluginExecutorStreamUsage parses streaming chunks and updates a stream usage buffer.
func ObservePluginExecutorStreamUsage(protocol string, payload []byte, buffer *StreamUsageBuffer) {
	if buffer == nil || len(payload) == 0 {
		return
	}
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "claude":
		IterateStreamLines(payload, func(line []byte) {
			if detail, ok := parseClaudeStreamLine(line); ok {
				ObserveMergedStreamUsage(buffer, detail)
			}
		})
	case "gemini":
		IterateStreamLines(payload, func(line []byte) {
			if detail, ok := ParseGeminiStreamUsage(line); ok {
				buffer.Observe(detail, ok)
			}
		})
	case "interactions", "interactions-response":
		IterateStreamLines(payload, func(line []byte) {
			if detail, ok := ParseInteractionsStreamUsage(line); ok {
				ObserveMergedStreamUsage(buffer, detail)
			}
		})
	case "antigravity":
		IterateStreamLines(payload, func(line []byte) {
			if detail, ok := ParseAntigravityStreamUsage(line); ok {
				buffer.Observe(detail, ok)
			}
		})
	case "codex", "openai-response":
		IterateStreamLines(payload, func(line []byte) {
			if jsonBytes := ExtractStreamJSONPayload(line); len(jsonBytes) > 0 {
				if detail, ok := ParseCodexUsage(jsonBytes); ok {
					buffer.Observe(detail, ok)
					return
				}
			}
			buffer.ObserveOpenAIStream(line)
		})
	default:
		IterateStreamLines(payload, func(line []byte) {
			buffer.ObserveOpenAIStream(line)
		})
	}
}

// ObservePluginExecutorStreamTTFT inspects a streaming payload and records TTFT on the reporter.
func ObservePluginExecutorStreamTTFT(protocol string, reporter *UsageReporter, payload []byte) {
	if reporter == nil || len(payload) == 0 {
		return
	}
	reporter.RecordFirstPacket()
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "claude":
		ObserveClaudeTokenEvent(reporter, payload)
	case "gemini", "antigravity", "interactions", "interactions-response":
		ObserveGeminiTokenEvent(reporter, payload)
	case "codex", "openai-response":
		ObserveResponsesTokenEvent(reporter, payload)
	default:
		ObserveChatTokenEvent(reporter, payload)
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
	return ParseClaudeUsage([]byte(`{"usage":` + usageNode.Raw + `}`))
}

func parseClaudeStreamLine(line []byte) (usage.Detail, bool) {
	payload := ExtractStreamJSONPayload(line)
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
	detail := ParseClaudeUsage([]byte(`{"usage":` + usageNode.Raw + `}`))
	return detail, true
}

// ObserveMergedStreamUsage updates buffer with merged usage details.
func ObserveMergedStreamUsage(buffer *StreamUsageBuffer, update usage.Detail) {
	if buffer == nil {
		return
	}
	if existing, ok := buffer.Detail(); ok {
		merged := MergeStreamUsageDetail(existing, update)
		buffer.Observe(merged, true)
		return
	}
	buffer.Observe(update, true)
}

// MergeStreamUsageDetail merges existing stream usage with a newer update.
func MergeStreamUsageDetail(existing, update usage.Detail) usage.Detail {
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

// IterateStreamLines splits payload by newline and invokes fn for non-empty lines.
func IterateStreamLines(payload []byte, fn func(line []byte)) {
	for _, line := range bytes.Split(payload, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		fn(trimmed)
	}
}

// ExtractStreamJSONPayload extracts SSE data/json payload from a raw line.
func ExtractStreamJSONPayload(line []byte) []byte {
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
