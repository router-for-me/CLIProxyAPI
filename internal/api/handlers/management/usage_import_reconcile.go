package management

import (
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func usageSummaryOnlySnapshot(snapshot usage.StatisticsSnapshot) usage.StatisticsSnapshot {
	out := usage.StatisticsSnapshot{
		TotalRequests:  snapshot.TotalRequests,
		SuccessCount:   snapshot.SuccessCount,
		FailureCount:   snapshot.FailureCount,
		TotalTokens:    snapshot.TotalTokens,
		APIs:           make(map[string]usage.APISnapshot, len(snapshot.APIs)),
		RequestsByDay:  cloneUsageStringInt64Map(snapshot.RequestsByDay),
		RequestsByHour: cloneUsageStringInt64Map(snapshot.RequestsByHour),
		TokensByDay:    cloneUsageStringInt64Map(snapshot.TokensByDay),
		TokensByHour:   cloneUsageStringInt64Map(snapshot.TokensByHour),
	}

	for apiName, apiSnapshot := range snapshot.APIs {
		apiCopy := usage.APISnapshot{
			TotalRequests: apiSnapshot.TotalRequests,
			TotalTokens:   apiSnapshot.TotalTokens,
			Models:        make(map[string]usage.ModelSnapshot, len(apiSnapshot.Models)),
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			apiCopy.Models[modelName] = usage.ModelSnapshot{
				TotalRequests:  modelSnapshot.TotalRequests,
				TotalTokens:    modelSnapshot.TotalTokens,
				TokenBreakdown: modelSnapshot.TokenBreakdown,
				Latency:        modelSnapshot.Latency,
			}
		}
		out.APIs[apiName] = apiCopy
	}

	if out.TotalRequests == 0 {
		for _, apiSnapshot := range out.APIs {
			out.TotalRequests += apiSnapshot.TotalRequests
		}
	}
	if out.TotalTokens == 0 {
		for _, apiSnapshot := range out.APIs {
			out.TotalTokens += apiSnapshot.TotalTokens
		}
	}
	if out.SuccessCount == 0 && out.FailureCount == 0 && out.TotalRequests > 0 {
		out.SuccessCount = out.TotalRequests
	}

	return out
}

func usageDetailSummarySnapshot(snapshot usage.StatisticsSnapshot) usage.StatisticsSnapshot {
	stats := usage.NewRequestStatistics()
	_ = stats.MergeSnapshot(snapshot)
	return stats.SnapshotSummary()
}

func usageResidualSummarySnapshot(snapshot usage.StatisticsSnapshot) usage.StatisticsSnapshot {
	full := usageSummaryOnlySnapshot(snapshot)
	detail := usageDetailSummarySnapshot(snapshot)
	return subtractUsageStatisticsSnapshot(full, detail)
}

func subtractUsageStatisticsSnapshot(full, detail usage.StatisticsSnapshot) usage.StatisticsSnapshot {
	out := usage.StatisticsSnapshot{
		TotalRequests:  clampUsageInt64(full.TotalRequests - detail.TotalRequests),
		SuccessCount:   clampUsageInt64(full.SuccessCount - detail.SuccessCount),
		FailureCount:   clampUsageInt64(full.FailureCount - detail.FailureCount),
		TotalTokens:    clampUsageInt64(full.TotalTokens - detail.TotalTokens),
		APIs:           make(map[string]usage.APISnapshot),
		RequestsByDay:  subtractUsageStringInt64Maps(full.RequestsByDay, detail.RequestsByDay),
		RequestsByHour: subtractUsageStringInt64Maps(full.RequestsByHour, detail.RequestsByHour),
		TokensByDay:    subtractUsageStringInt64Maps(full.TokensByDay, detail.TokensByDay),
		TokensByHour:   subtractUsageStringInt64Maps(full.TokensByHour, detail.TokensByHour),
	}

	for apiName, fullAPI := range full.APIs {
		detailAPI, hasDetailAPI := detail.APIs[apiName]
		apiOut := usage.APISnapshot{
			TotalRequests: clampUsageInt64(fullAPI.TotalRequests - detailAPI.TotalRequests),
			TotalTokens:   clampUsageInt64(fullAPI.TotalTokens - detailAPI.TotalTokens),
			Models:        make(map[string]usage.ModelSnapshot),
		}
		for modelName, fullModel := range fullAPI.Models {
			detailModel := usage.ModelSnapshot{}
			if hasDetailAPI {
				detailModel = detailAPI.Models[modelName]
			}
			modelOut := usage.ModelSnapshot{
				TotalRequests:  clampUsageInt64(fullModel.TotalRequests - detailModel.TotalRequests),
				TotalTokens:    clampUsageInt64(fullModel.TotalTokens - detailModel.TotalTokens),
				TokenBreakdown: subtractUsageTokenStats(fullModel.TokenBreakdown, detailModel.TokenBreakdown),
				Latency:        subtractUsageLatencyStats(fullModel.Latency, detailModel.Latency),
			}
			if usageModelSnapshotEmpty(modelOut) {
				continue
			}
			apiOut.Models[modelName] = modelOut
		}
		if usageAPISnapshotEmpty(apiOut) {
			continue
		}
		out.APIs[apiName] = apiOut
	}

	return out
}

func aggregatedAllWindowSnapshotFromSummary(summary usage.StatisticsSnapshot, generatedAt time.Time) usage.AggregatedUsageSnapshot {
	summary = usageSummaryOnlySnapshot(summary)
	if usageSummarySnapshotEmpty(summary) {
		return usage.AggregatedUsageSnapshot{
			GeneratedAt: generatedAt.UTC(),
			Windows:     map[string]usage.AggregatedUsageWindow{},
		}
	}

	generatedAt = generatedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}

	window := usage.AggregatedUsageWindow{
		TotalRequests: summary.TotalRequests,
		SuccessCount:  summary.SuccessCount,
		FailureCount:  summary.FailureCount,
		TotalTokens:   summary.TotalTokens,
	}

	modelAggs := make(map[string]usage.AggregatedUsageModelStats)
	modelNames := make(map[string]struct{})
	apiNames := make([]string, 0, len(summary.APIs))
	for apiName := range summary.APIs {
		apiNames = append(apiNames, apiName)
	}
	sort.Strings(apiNames)

	for _, apiName := range apiNames {
		apiSnapshot := summary.APIs[apiName]
		apiWindow := usage.AggregatedUsageAPIStats{
			Endpoint:      apiName,
			TotalRequests: apiSnapshot.TotalRequests,
			TotalTokens:   apiSnapshot.TotalTokens,
			Models:        make(map[string]usage.AggregatedUsageAPIModelStats),
		}

		modelNamesForAPI := make([]string, 0, len(apiSnapshot.Models))
		for modelName := range apiSnapshot.Models {
			modelNamesForAPI = append(modelNamesForAPI, modelName)
		}
		sort.Strings(modelNamesForAPI)

		for _, modelName := range modelNamesForAPI {
			modelSnapshot := apiSnapshot.Models[modelName]
			modelNames[modelName] = struct{}{}
			successCount := modelSnapshot.Latency.Count
			if successCount > modelSnapshot.TotalRequests {
				successCount = modelSnapshot.TotalRequests
			}
			failureCount := clampUsageInt64(modelSnapshot.TotalRequests - successCount)
			apiModel := usage.AggregatedUsageAPIModelStats{
				Requests:       modelSnapshot.TotalRequests,
				SuccessCount:   successCount,
				FailureCount:   failureCount,
				Tokens:         modelSnapshot.TotalTokens,
				TokenBreakdown: modelSnapshot.TokenBreakdown,
				Latency:        modelSnapshot.Latency,
			}
			apiWindow.Models[modelName] = apiModel
			apiWindow.SuccessCount += successCount
			apiWindow.FailureCount += failureCount
			apiWindow.Latency = mergeUsageLatencyStats(apiWindow.Latency, modelSnapshot.Latency)
			apiWindow.TokenBreakdown = addUsageTokenStats(apiWindow.TokenBreakdown, modelSnapshot.TokenBreakdown)

			aggregate := modelAggs[modelName]
			aggregate.Model = modelName
			aggregate.Requests += modelSnapshot.TotalRequests
			aggregate.SuccessCount += successCount
			aggregate.FailureCount += failureCount
			aggregate.Tokens += modelSnapshot.TotalTokens
			aggregate.TokenBreakdown = addUsageTokenStats(aggregate.TokenBreakdown, modelSnapshot.TokenBreakdown)
			aggregate.Latency = mergeUsageLatencyStats(aggregate.Latency, modelSnapshot.Latency)
			modelAggs[modelName] = aggregate
		}

		if apiWindow.SuccessCount == 0 && apiWindow.FailureCount == 0 && apiWindow.TotalRequests > 0 {
			apiWindow.SuccessCount = apiWindow.TotalRequests
		}
		window.TokenBreakdown = addUsageTokenStats(window.TokenBreakdown, apiWindow.TokenBreakdown)
		window.Latency = mergeUsageLatencyStats(window.Latency, apiWindow.Latency)
		window.APIs = append(window.APIs, apiWindow)
	}

	modelNamesSorted := make([]string, 0, len(modelNames))
	for modelName := range modelNames {
		modelNamesSorted = append(modelNamesSorted, modelName)
	}
	sort.Strings(modelNamesSorted)
	window.ModelNames = append([]string(nil), modelNamesSorted...)
	for _, modelName := range modelNamesSorted {
		window.Models = append(window.Models, modelAggs[modelName])
	}

	return usage.AggregatedUsageSnapshot{
		GeneratedAt: generatedAt,
		ModelNames:  append([]string(nil), modelNamesSorted...),
		Windows: map[string]usage.AggregatedUsageWindow{
			"all": window,
		},
	}
}

func usageSummarySnapshotEmpty(snapshot usage.StatisticsSnapshot) bool {
	return snapshot.TotalRequests == 0 &&
		snapshot.SuccessCount == 0 &&
		snapshot.FailureCount == 0 &&
		snapshot.TotalTokens == 0 &&
		len(snapshot.APIs) == 0 &&
		len(snapshot.RequestsByDay) == 0 &&
		len(snapshot.RequestsByHour) == 0 &&
		len(snapshot.TokensByDay) == 0 &&
		len(snapshot.TokensByHour) == 0
}

func usageAPISnapshotEmpty(snapshot usage.APISnapshot) bool {
	return snapshot.TotalRequests == 0 &&
		snapshot.TotalTokens == 0 &&
		len(snapshot.Models) == 0
}

func usageModelSnapshotEmpty(snapshot usage.ModelSnapshot) bool {
	return snapshot.TotalRequests == 0 &&
		snapshot.TotalTokens == 0 &&
		usageTokenStatsZero(snapshot.TokenBreakdown) &&
		usageLatencyStatsZero(snapshot.Latency)
}

func usageTokenStatsZero(stats usage.TokenStats) bool {
	return stats.InputTokens == 0 &&
		stats.OutputTokens == 0 &&
		stats.ReasoningTokens == 0 &&
		stats.CachedTokens == 0 &&
		stats.TotalTokens == 0
}

func usageLatencyStatsZero(stats usage.LatencyStats) bool {
	return stats.Count == 0 &&
		stats.TotalMs == 0 &&
		stats.MinMs == 0 &&
		stats.MaxMs == 0
}

func cloneUsageStringInt64Map(src map[string]int64) map[string]int64 {
	if len(src) == 0 {
		return map[string]int64{}
	}
	dst := make(map[string]int64, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func subtractUsageStringInt64Maps(full, detail map[string]int64) map[string]int64 {
	if len(full) == 0 {
		return map[string]int64{}
	}
	out := make(map[string]int64, len(full))
	for key, fullValue := range full {
		value := clampUsageInt64(fullValue - detail[key])
		if value == 0 {
			continue
		}
		out[key] = value
	}
	return out
}

func subtractUsageTokenStats(full, detail usage.TokenStats) usage.TokenStats {
	return usage.TokenStats{
		InputTokens:     clampUsageInt64(full.InputTokens - detail.InputTokens),
		OutputTokens:    clampUsageInt64(full.OutputTokens - detail.OutputTokens),
		ReasoningTokens: clampUsageInt64(full.ReasoningTokens - detail.ReasoningTokens),
		CachedTokens:    clampUsageInt64(full.CachedTokens - detail.CachedTokens),
		TotalTokens:     clampUsageInt64(full.TotalTokens - detail.TotalTokens),
	}
}

func addUsageTokenStats(left, right usage.TokenStats) usage.TokenStats {
	return usage.TokenStats{
		InputTokens:     left.InputTokens + right.InputTokens,
		OutputTokens:    left.OutputTokens + right.OutputTokens,
		ReasoningTokens: left.ReasoningTokens + right.ReasoningTokens,
		CachedTokens:    left.CachedTokens + right.CachedTokens,
		TotalTokens:     left.TotalTokens + right.TotalTokens,
	}
}

func subtractUsageLatencyStats(full, detail usage.LatencyStats) usage.LatencyStats {
	residualCount := clampUsageInt64(full.Count - detail.Count)
	residualTotal := clampUsageInt64(full.TotalMs - detail.TotalMs)
	if residualCount == 0 {
		return usage.LatencyStats{}
	}
	return usage.LatencyStats{
		Count:   residualCount,
		TotalMs: residualTotal,
		MinMs:   full.MinMs,
		MaxMs:   full.MaxMs,
	}
}

func mergeUsageLatencyStats(left, right usage.LatencyStats) usage.LatencyStats {
	if left.Count == 0 {
		return right
	}
	if right.Count == 0 {
		return left
	}
	out := usage.LatencyStats{
		Count:   left.Count + right.Count,
		TotalMs: left.TotalMs + right.TotalMs,
		MinMs:   left.MinMs,
		MaxMs:   left.MaxMs,
	}
	if out.MinMs == 0 || (right.MinMs > 0 && right.MinMs < out.MinMs) {
		out.MinMs = right.MinMs
	}
	if right.MaxMs > out.MaxMs {
		out.MaxMs = right.MaxMs
	}
	return out
}

func clampUsageInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func usageResidualSourceID(sourceID string) string {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return ""
	}
	return sourceID + "#detail-residual"
}
