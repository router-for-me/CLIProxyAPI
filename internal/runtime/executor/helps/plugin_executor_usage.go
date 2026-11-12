package helps

import (
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
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
		return parseResponsesPluginExecutorUsage(payload)
	default:
		return ParseOpenAIUsage(payload)
	}
}

// parseResponsesPluginExecutorUsage reads both streaming-event usage (response.usage)
// and a completed Responses object (top-level usage). A service tier without
// response.usage must not hide the completed object's token counts.
func parseResponsesPluginExecutorUsage(payload []byte) usage.Detail {
	detail, ok := ParseCodexUsage(payload)
	if ok && hasNonZeroTokenUsage(detail) {
		return detail
	}
	openAIDetail := ParseOpenAIUsage(payload)
	if hasNonZeroTokenUsage(openAIDetail) {
		if openAIDetail.ResponseServiceTier == "" {
			openAIDetail.ResponseServiceTier = detail.ResponseServiceTier
		}
		return openAIDetail
	}
	if ok {
		return detail
	}
	return openAIDetail
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
				// A service tier without response.usage is not token usage. Fall through
				// so a completed Responses object's top-level usage is still recorded.
				if detail, ok := ParseCodexUsage(jsonBytes); ok && hasNonZeroTokenUsage(detail) {
					buffer.Observe(detail, true)
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

	outputExcludesReasoning, knownOutput := detailOutputExcludesReasoning(update)
	if !knownOutput {
		outputExcludesReasoning, knownOutput = detailOutputExcludesReasoning(existing)
	}
	inputIncludesCache, knownInput := detailInputIncludesCache(update)
	if !knownInput {
		inputIncludesCache, knownInput = detailInputIncludesCache(existing)
	}

	if outputExcludesReasoning || (inputIncludesCache && !knownOutput) {
		calculatedTotal, okTotal := safeUsageTokenSum(merged.InputTokens, merged.OutputTokens, merged.ReasoningTokens)
		if !okTotal {
			merged.TokenBreakdown = invalidUsageTokenBreakdown(merged.TotalTokens)
			return merged
		}
		if merged.TotalTokens == 0 || merged.TotalTokens < calculatedTotal {
			merged.TotalTokens = calculatedTotal
		}
		merged.TokenBreakdown = usage.NewSeparateReasoningTokenBreakdown(
			merged.InputTokens,
			merged.CacheReadTokens,
			merged.CacheCreationTokens,
			merged.OutputTokens,
			merged.ReasoningTokens,
			merged.TotalTokens,
		)
		return merged
	}

	if inputIncludesCache {
		calculatedTotal, okTotal := safeUsageTokenSum(merged.InputTokens, merged.OutputTokens)
		if !okTotal {
			merged.TokenBreakdown = invalidUsageTokenBreakdown(merged.TotalTokens)
			return merged
		}
		if merged.TotalTokens == 0 || merged.TotalTokens < calculatedTotal {
			merged.TotalTokens = calculatedTotal
		}
		merged.TokenBreakdown = usage.NewSubsetTokenBreakdown(
			merged.InputTokens,
			merged.CacheReadTokens,
			merged.CacheCreationTokens,
			merged.OutputTokens,
			merged.ReasoningTokens,
			merged.TotalTokens,
		)
		return merged
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

func detailInputIncludesCache(detail usage.Detail) (bool, bool) {
	cache, okCache := safeUsageTokenSum(detail.CacheReadTokens, detail.CacheCreationTokens)
	if !okCache || (cache == 0 && detail.CachedTokens < 0) {
		return false, false
	}
	if cache == 0 {
		cache = detail.CachedTokens
	}
	if cache <= 0 || !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete {
		return false, false
	}
	if detail.TokenBreakdown.Input.TotalTokens == detail.InputTokens &&
		detail.TokenBreakdown.Input.UncachedTokens == detail.InputTokens-cache {
		return true, true
	}
	if independentTotal, okTotal := safeUsageTokenSum(detail.InputTokens, cache); okTotal &&
		detail.TokenBreakdown.Input.TotalTokens == independentTotal &&
		detail.TokenBreakdown.Input.UncachedTokens == detail.InputTokens {
		return false, true
	}
	return false, false
}

func detailOutputExcludesReasoning(detail usage.Detail) (bool, bool) {
	if detail.ReasoningTokens <= 0 || !detail.TokenBreakdown.Valid() {
		return false, false
	}
	if detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete ||
		detail.TokenBreakdown.Output.ReasoningTokens != detail.ReasoningTokens {
		return false, false
	}
	if detail.OutputTokens > 0 {
		if separateTotal, okTotal := safeUsageTokenSum(detail.OutputTokens, detail.ReasoningTokens); okTotal &&
			detail.TokenBreakdown.Output.TotalTokens == separateTotal &&
			detail.TokenBreakdown.Output.NonReasoningTokens == detail.OutputTokens {
			return true, true
		}
		if detail.TokenBreakdown.Output.TotalTokens == detail.OutputTokens &&
			detail.TokenBreakdown.Output.NonReasoningTokens == detail.OutputTokens-detail.ReasoningTokens {
			return false, true
		}
		return false, false
	}
	if detail.OutputTokens == 0 && detail.TokenBreakdown.Output.TotalTokens == detail.ReasoningTokens &&
		detail.TokenBreakdown.Output.NonReasoningTokens == 0 && detail.TotalTokens == detail.TokenBreakdown.TotalTokens {
		return true, true
	}
	return false, false
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
