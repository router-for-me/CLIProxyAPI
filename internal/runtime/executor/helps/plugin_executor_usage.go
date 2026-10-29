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
	if ok && (hasNonZeroTokenUsage(detail) || hasStructuredUsageDetail(detail)) {
		return detail
	}
	openAIDetail := ParseOpenAIUsage(payload)
	if hasNonZeroTokenUsage(openAIDetail) || hasStructuredUsageDetail(openAIDetail) {
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
			buffer.ObserveClaudeStream(line)
		})
	case "gemini":
		IterateStreamLines(payload, func(line []byte) {
			buffer.ObserveGeminiStream(line)
		})
	case "interactions", "interactions-response":
		IterateStreamLines(payload, func(line []byte) {
			if detail, ok := ParseInteractionsStreamUsage(line); ok {
				ObserveMergedStreamUsage(buffer, detail)
			}
		})
	case "antigravity":
		IterateStreamLines(payload, func(line []byte) {
			buffer.ObserveAntigravityStream(line)
		})
	case "codex", "openai-response":
		IterateStreamLines(payload, func(line []byte) {
			if jsonBytes := ExtractStreamJSONPayload(line); len(jsonBytes) > 0 {
				if bytes.Contains(jsonBytes, responsesOutputItemDoneMarker) && buffer.ObserveResponsesStream(jsonBytes) {
					return
				}
				// A service tier without response.usage is not token usage. Fall through
				// so a completed Responses object's top-level usage is still recorded.
				if detail, ok := ParseCodexUsageWithOutputItems(jsonBytes, buffer.responsesOutputByIndex, buffer.responsesOutputFallback); ok && (hasNonZeroTokenUsage(detail) || hasStructuredUsageDetail(detail)) {
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
	root := gjson.ParseBytes(payload)
	usageNode := root.Get("usage")
	if !usageNode.Exists() {
		usageNode = root.Get("message.usage")
	}
	if !usageNode.Exists() {
		return usage.Detail{}
	}
	detail := parseClaudeUsageNode(usageNode)
	applyClaudeContentBlocksUsage(&detail, root.Get("content"))
	return detail
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
	return parseClaudeUsageNode(usageNode), true
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
			mergeStreamStructuredFields(&merged, existing, update)
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
		mergeStreamStructuredFields(&merged, existing, update)
		return merged
	}

	if inputIncludesCache {
		calculatedTotal, okTotal := safeUsageTokenSum(merged.InputTokens, merged.OutputTokens)
		if !okTotal {
			merged.TokenBreakdown = invalidUsageTokenBreakdown(merged.TotalTokens)
			mergeStreamStructuredFields(&merged, existing, update)
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
		mergeStreamStructuredFields(&merged, existing, update)
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
	mergeStreamStructuredFields(&merged, existing, update)
	return merged
}

func mergeStreamStructuredFields(merged *usage.Detail, existing, update usage.Detail) {
	if merged == nil {
		return
	}
	if merged.UpstreamServiceTier == "" {
		merged.UpstreamServiceTier = existing.UpstreamServiceTier
	}
	if merged.Speed == "" {
		merged.Speed = existing.Speed
	}
	if merged.InferenceGeo == "" {
		merged.InferenceGeo = existing.InferenceGeo
	}
	if len(merged.IterationSlice()) == 0 && len(existing.IterationSlice()) > 0 {
		merged.Iterations = usage.IterationsPtr(existing.IterationSlice())
	} else if len(merged.IterationSlice()) > 0 {
		merged.Iterations = usage.IterationsPtr(merged.IterationSlice())
	}
	merged.ToolUsageDetails = mergeToolUsageDetails(existing.ToolUsageDetails, update.ToolUsageDetails)
	if merged.WebSearchRequests == nil && existing.WebSearchRequests != nil {
		merged.WebSearchRequests = usage.CloneInt64Ptr(existing.WebSearchRequests)
	} else if merged.WebSearchRequests != nil && *merged.WebSearchRequests == 0 && existing.WebSearchRequests != nil && *existing.WebSearchRequests > 0 {
		merged.WebSearchRequests = usage.CloneInt64Ptr(existing.WebSearchRequests)
	} else if merged.WebSearchRequests != nil {
		merged.WebSearchRequests = usage.CloneInt64Ptr(merged.WebSearchRequests)
	} else if merged.ToolUsageDetails != nil && merged.ToolUsageDetails.WebSearchCalls != nil {
		merged.WebSearchRequests = usage.CloneInt64Ptr(merged.ToolUsageDetails.WebSearchCalls)
	}
	merged.TextInputTokens = mergeInt64Ptr(existing.TextInputTokens, update.TextInputTokens)
	merged.ImageInputTokens = mergeInt64Ptr(existing.ImageInputTokens, update.ImageInputTokens)
	merged.AudioInputTokens = mergeInt64Ptr(existing.AudioInputTokens, update.AudioInputTokens)
	merged.VideoInputTokens = mergeInt64Ptr(existing.VideoInputTokens, update.VideoInputTokens)
	merged.FileInputTokens = mergeInt64Ptr(existing.FileInputTokens, update.FileInputTokens)
	merged.CachedTextTokens = mergeInt64Ptr(existing.CachedTextTokens, update.CachedTextTokens)
	merged.TextOutputTokens = mergeInt64Ptr(existing.TextOutputTokens, update.TextOutputTokens)
	merged.ImageOutputTokens = mergeInt64Ptr(existing.ImageOutputTokens, update.ImageOutputTokens)
	merged.AudioOutputTokens = mergeInt64Ptr(existing.AudioOutputTokens, update.AudioOutputTokens)
	merged.AcceptedPredictionTokens = mergeInt64Ptr(existing.AcceptedPredictionTokens, update.AcceptedPredictionTokens)
	merged.RejectedPredictionTokens = mergeInt64Ptr(existing.RejectedPredictionTokens, update.RejectedPredictionTokens)
	merged.ImageCount = mergeInt64Ptr(existing.ImageCount, update.ImageCount)
	if update.RequestedImageDetails != nil {
		merged.RequestedImageDetails = update.RequestedImageDetails.Clone()
	} else if existing.RequestedImageDetails != nil {
		merged.RequestedImageDetails = existing.RequestedImageDetails.Clone()
	}
	if update.ImageDetails != nil {
		merged.ImageDetails = update.ImageDetails.Clone()
	} else if existing.ImageDetails != nil {
		merged.ImageDetails = existing.ImageDetails.Clone()
	}
	if update.MediaCounts != nil {
		merged.MediaCounts = update.MediaCounts.Clone()
	} else if existing.MediaCounts != nil {
		merged.MediaCounts = existing.MediaCounts.Clone()
	}
	merged.CostDetails = mergeCostDetails(existing.CostDetails, update.CostDetails)
	cacheDetails := resolveMergedCacheDetails(existing, update, *merged)
	applyCacheDetailsToUsageDetail(merged, cacheDetails)
}

func mergeInt64Ptr(existing, update *int64) *int64 {
	if update != nil {
		if *update == 0 && existing != nil && *existing > 0 {
			return usage.CloneInt64Ptr(existing)
		}
		return usage.CloneInt64Ptr(update)
	}
	return usage.CloneInt64Ptr(existing)
}

func resolveMergedCacheDetails(existing, update, merged usage.Detail) *usage.CacheDetails {
	existingCC := existing.CacheDetails
	updateCC := update.CacheDetails
	if updateCC != nil {
		if update.CacheCreationTokens == 0 && existing.CacheCreationTokens > 0 && existingCC != nil {
			if sum, ok := updateCC.SumTokens(); !ok || sum == 0 {
				return existingCC.Clone()
			}
		}
		if sum, ok := updateCC.SumTokens(); ok && sum == merged.CacheCreationTokens {
			return updateCC.Clone()
		}
		if iterations := merged.IterationSlice(); len(iterations) > 0 {
			if derived := deriveClaudeCacheCreationFromIterations(iterations, merged.CacheCreationTokens); derived != nil {
				return derived
			}
		}
		return updateCC.Clone()
	}
	if iterations := merged.IterationSlice(); len(iterations) > 0 {
		if derived := deriveClaudeCacheCreationFromIterations(iterations, merged.CacheCreationTokens); derived != nil {
			return derived
		}
	}
	if existingCC != nil && merged.CacheCreationTokens == existing.CacheCreationTokens {
		return existingCC.Clone()
	}
	return nil
}

func mergeToolUsageDetails(existing, update *usage.ToolUsageDetails) *usage.ToolUsageDetails {
	if existing == nil {
		return update.Clone()
	}
	if update == nil {
		return existing.Clone()
	}
	out := update.Clone()
	mergeCounter := func(cur, prev *int64) *int64 {
		if cur == nil {
			return usage.CloneInt64Ptr(prev)
		}
		if *cur == 0 && prev != nil && *prev > 0 {
			return usage.CloneInt64Ptr(prev)
		}
		return cur
	}
	out.WebSearchCalls = mergeCounter(out.WebSearchCalls, existing.WebSearchCalls)
	out.WebSearchQueries = mergeCounter(out.WebSearchQueries, existing.WebSearchQueries)
	out.WebFetchCalls = mergeCounter(out.WebFetchCalls, existing.WebFetchCalls)
	out.NumSearchResults = mergeCounter(out.NumSearchResults, existing.NumSearchResults)
	out.NumSourcesUsed = mergeCounter(out.NumSourcesUsed, existing.NumSourcesUsed)
	out.NumServerSideToolsUsed = mergeCounter(out.NumServerSideToolsUsed, existing.NumServerSideToolsUsed)
	out.ToolCallsRequested = mergeCounter(out.ToolCallsRequested, existing.ToolCallsRequested)
	out.ToolCallsExecuted = mergeCounter(out.ToolCallsExecuted, existing.ToolCallsExecuted)
	out.XSearchCalls = mergeCounter(out.XSearchCalls, existing.XSearchCalls)
	out.XPostsFetched = mergeCounter(out.XPostsFetched, existing.XPostsFetched)
	out.XUsersFetched = mergeCounter(out.XUsersFetched, existing.XUsersFetched)
	out.CodeExecutionCalls = mergeCounter(out.CodeExecutionCalls, existing.CodeExecutionCalls)
	out.FileSearchCalls = mergeCounter(out.FileSearchCalls, existing.FileSearchCalls)
	out.MCPCalls = mergeCounter(out.MCPCalls, existing.MCPCalls)
	out.DocumentSearchCalls = mergeCounter(out.DocumentSearchCalls, existing.DocumentSearchCalls)
	out.ImageGenerationCalls = mergeCounter(out.ImageGenerationCalls, existing.ImageGenerationCalls)
	if len(existing.Extra) > 0 {
		if out.Extra == nil {
			out.Extra = make(map[string]int64, len(existing.Extra))
		}
		for k, prev := range existing.Extra {
			if cur, ok := out.Extra[k]; !ok || (cur == 0 && prev > 0) {
				out.Extra[k] = prev
			}
		}
	}
	return out
}

func mergeCostDetails(existing, update *usage.CostDetails) *usage.CostDetails {
	if existing == nil {
		return update.Clone()
	}
	if update == nil {
		return existing.Clone()
	}
	out := update.Clone()
	e := existing.Clone()
	if out.Cost == nil {
		out.Cost = e.Cost
	}
	if out.CostInUSDTicks == nil {
		out.CostInUSDTicks = e.CostInUSDTicks
	}
	if out.IsBYOK == nil {
		out.IsBYOK = e.IsBYOK
	}
	if out.UpstreamInferenceCost == nil {
		out.UpstreamInferenceCost = e.UpstreamInferenceCost
	}
	if out.UpstreamInferencePromptCost == nil {
		out.UpstreamInferencePromptCost = e.UpstreamInferencePromptCost
	}
	if out.UpstreamInferenceCompletionsCost == nil {
		out.UpstreamInferenceCompletionsCost = e.UpstreamInferenceCompletionsCost
	}
	return out
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
