package usage

import (
	"sort"
	"strings"
	"time"
)

func canonicalDetailedSnapshotForImport(snapshot StatisticsSnapshot) StatisticsSnapshot {
	canonical := canonicalSummarySnapshotForImport(snapshot)
	if canonical.APIs == nil {
		canonical.APIs = make(map[string]APISnapshot)
	}

	for apiName, apiSnapshot := range snapshot.APIs {
		apiCopy := canonical.APIs[apiName]
		if apiCopy.Models == nil {
			apiCopy.Models = make(map[string]ModelSnapshot, len(apiSnapshot.Models))
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelCopy := apiCopy.Models[modelName]
			if len(modelSnapshot.Details) > 0 {
				modelCopy.Details = make([]RequestDetail, 0, len(modelSnapshot.Details))
				for _, detail := range modelSnapshot.Details {
					modelCopy.Details = append(modelCopy.Details, normalizeImportedRequestDetail(detail))
				}
			}
			apiCopy.Models[modelName] = modelCopy
		}
		canonical.APIs[apiName] = apiCopy
	}

	return canonical
}

func normalizeImportedRequestDetail(detail RequestDetail) RequestDetail {
	detail.Timestamp = detail.Timestamp.UTC()
	detail.Tokens = normaliseTokenStats(detail.Tokens)
	if detail.LatencyMs < 0 {
		detail.LatencyMs = 0
	}
	detail.Source = strings.TrimSpace(detail.Source)
	detail.AuthIndex = strings.TrimSpace(detail.AuthIndex)
	detail.ModelReasoningEffort = strings.TrimSpace(detail.ModelReasoningEffort)
	return detail
}

func fingerprintDetailedSnapshot(snapshot StatisticsSnapshot) string {
	return fingerprintImportValue(canonicalDetailedSnapshotForImport(snapshot))
}

func aggregatedUsageSnapshotFromDetailedImport(snapshot StatisticsSnapshot, now time.Time) AggregatedUsageSnapshot {
	snapshot = canonicalDetailedSnapshotForImport(snapshot)
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	result := AggregatedUsageSnapshot{
		GeneratedAt: now,
		Windows:     make(map[string]AggregatedUsageWindow, len(aggregatedUsageWindowConfigs)),
	}
	accumulators := make(map[string]*aggregatedUsageWindowAccumulator, len(aggregatedUsageWindowConfigs))
	for _, cfg := range aggregatedUsageWindowConfigs {
		accumulators[cfg.key] = newAggregatedUsageWindowAccumulator(cfg, now)
	}

	modelNames := make(map[string]struct{})
	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			apiName = "unknown"
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = strings.TrimSpace(modelName)
			if modelName == "" {
				modelName = "unknown"
			}
			for _, detail := range modelSnapshot.Details {
				if detail.Timestamp.IsZero() {
					continue
				}
				modelNames[modelName] = struct{}{}
				for _, cfg := range aggregatedUsageWindowConfigs {
					acc := accumulators[cfg.key]
					if acc == nil || !acc.includes(detail.Timestamp) {
						continue
					}
					acc.addRecord(apiName, modelName, detail)
				}
			}
		}
	}

	result.ModelNames = sortedStringKeys(modelNames)
	for _, cfg := range aggregatedUsageWindowConfigs {
		if acc := accumulators[cfg.key]; acc != nil {
			result.Windows[cfg.key] = acc.build()
		}
	}

	fullAllWindow := aggregatedAllWindowSnapshotFromSummary(canonicalSummarySnapshotForImport(snapshot), now)
	if allWindow, ok := fullAllWindow.Windows["all"]; ok {
		result.Windows["all"] = allWindow
	}
	result.ModelNames = mergeStringSlices(result.ModelNames, fullAllWindow.ModelNames)
	return result
}

func aggregatedAllWindowSnapshotFromSummary(summary StatisticsSnapshot, generatedAt time.Time) AggregatedUsageSnapshot {
	summary = canonicalSummarySnapshotForImport(summary)
	if statisticsSnapshotEmpty(summary) {
		return AggregatedUsageSnapshot{
			GeneratedAt: generatedAt.UTC(),
			Windows:     map[string]AggregatedUsageWindow{},
		}
	}

	generatedAt = generatedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}

	window := AggregatedUsageWindow{
		TotalRequests: summary.TotalRequests,
		SuccessCount:  summary.SuccessCount,
		FailureCount:  summary.FailureCount,
		TotalTokens:   summary.TotalTokens,
	}

	modelAggs := make(map[string]AggregatedUsageModelStats)
	modelNames := make(map[string]struct{})
	apiNames := make([]string, 0, len(summary.APIs))
	for apiName := range summary.APIs {
		apiNames = append(apiNames, apiName)
	}
	sort.Strings(apiNames)

	for _, apiName := range apiNames {
		apiSnapshot := summary.APIs[apiName]
		apiWindow := AggregatedUsageAPIStats{
			Endpoint:      apiName,
			TotalRequests: apiSnapshot.TotalRequests,
			TotalTokens:   apiSnapshot.TotalTokens,
			Models:        make(map[string]AggregatedUsageAPIModelStats, len(apiSnapshot.Models)),
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
			failureCount := modelSnapshot.TotalRequests - successCount
			if failureCount < 0 {
				failureCount = 0
			}

			apiModel := AggregatedUsageAPIModelStats{
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
			apiWindow.TokenBreakdown = sumTokenStats(apiWindow.TokenBreakdown, modelSnapshot.TokenBreakdown)
			apiWindow.Latency = sumLatencyStats(apiWindow.Latency, modelSnapshot.Latency)

			aggregate := modelAggs[modelName]
			aggregate.Model = modelName
			aggregate.Requests += modelSnapshot.TotalRequests
			aggregate.SuccessCount += successCount
			aggregate.FailureCount += failureCount
			aggregate.Tokens += modelSnapshot.TotalTokens
			aggregate.TokenBreakdown = sumTokenStats(aggregate.TokenBreakdown, modelSnapshot.TokenBreakdown)
			aggregate.Latency = sumLatencyStats(aggregate.Latency, modelSnapshot.Latency)
			modelAggs[modelName] = aggregate
		}

		if apiWindow.SuccessCount == 0 && apiWindow.FailureCount == 0 && apiWindow.TotalRequests > 0 {
			apiWindow.SuccessCount = apiWindow.TotalRequests
		}
		window.TokenBreakdown = sumTokenStats(window.TokenBreakdown, apiWindow.TokenBreakdown)
		window.Latency = sumLatencyStats(window.Latency, apiWindow.Latency)
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

	return AggregatedUsageSnapshot{
		GeneratedAt: generatedAt,
		ModelNames:  append([]string(nil), modelNamesSorted...),
		Windows: map[string]AggregatedUsageWindow{
			"all": window,
		},
	}
}

func statisticsSnapshotEmpty(snapshot StatisticsSnapshot) bool {
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

func sumTokenStats(left, right TokenStats) TokenStats {
	out := left
	mergeTokenStats(&out, right)
	return out
}

func sumLatencyStats(left, right LatencyStats) LatencyStats {
	out := left
	mergeLatencyStats(&out, right)
	return out
}
