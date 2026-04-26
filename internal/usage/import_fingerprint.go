package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

func canonicalSummarySnapshotForImport(snapshot StatisticsSnapshot) StatisticsSnapshot {
	canonical := StatisticsSnapshot{
		TotalRequests: snapshot.TotalRequests,
		SuccessCount:  snapshot.SuccessCount,
		FailureCount:  snapshot.FailureCount,
		TotalTokens:   snapshot.TotalTokens,
		APIs:          make(map[string]APISnapshot, len(snapshot.APIs)),
		RequestsByDay: cloneStringInt64Map(snapshot.RequestsByDay),
		RequestsByHour: canonicalHourSeries(
			snapshot.RequestsByHour,
		),
		TokensByDay: cloneStringInt64Map(snapshot.TokensByDay),
		TokensByHour: canonicalHourSeries(
			snapshot.TokensByHour,
		),
	}

	if canonical.TotalRequests == 0 {
		canonical.TotalRequests = canonical.SuccessCount + canonical.FailureCount
	}

	for apiName, apiSnapshot := range snapshot.APIs {
		copiedAPI := APISnapshot{
			TotalRequests: apiSnapshot.TotalRequests,
			TotalTokens:   apiSnapshot.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(apiSnapshot.Models)),
		}
		if canonical.TotalRequests == 0 {
			canonical.TotalRequests += apiSnapshot.TotalRequests
		}
		if canonical.TotalTokens == 0 {
			canonical.TotalTokens += apiSnapshot.TotalTokens
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			copiedAPI.Models[modelName] = ModelSnapshot{
				TotalRequests:  modelSnapshot.TotalRequests,
				TotalTokens:    modelSnapshot.TotalTokens,
				TokenBreakdown: normaliseTokenStats(modelSnapshot.TokenBreakdown),
				Latency:        modelSnapshot.Latency,
			}
		}
		canonical.APIs[apiName] = copiedAPI
	}

	if canonical.SuccessCount == 0 && canonical.FailureCount == 0 && canonical.TotalRequests > 0 {
		canonical.SuccessCount = canonical.TotalRequests
	}

	return canonical
}

func fingerprintSummarySnapshot(snapshot StatisticsSnapshot) string {
	return fingerprintImportValue(canonicalSummarySnapshotForImport(snapshot))
}

func filterImportedAggregatedUsageSnapshot(snapshot AggregatedUsageSnapshot) AggregatedUsageSnapshot {
	filtered := AggregatedUsageSnapshot{
		GeneratedAt: snapshot.GeneratedAt,
		ModelNames:  append([]string(nil), snapshot.ModelNames...),
		Windows:     make(map[string]AggregatedUsageWindow, 1),
	}
	if allWindow, ok := snapshot.Windows["all"]; ok {
		filtered.Windows["all"] = cloneAggregatedUsageWindow(allWindow)
	}
	return filtered
}

func normalizedImportedAggregatedSnapshotForFingerprint(snapshot AggregatedUsageSnapshot) AggregatedUsageSnapshot {
	filtered := filterImportedAggregatedUsageSnapshot(snapshot)
	if len(filtered.Windows) == 0 {
		return filtered
	}

	filtered.GeneratedAt = time.Time{}
	sort.Strings(filtered.ModelNames)

	if allWindow, ok := filtered.Windows["all"]; ok {
		filtered.Windows["all"] = normalizeAggregatedUsageWindowForFingerprint(allWindow)
	}

	return filtered
}

func fingerprintImportedAggregatedSnapshot(snapshot AggregatedUsageSnapshot) string {
	filtered := normalizedImportedAggregatedSnapshotForFingerprint(snapshot)
	if len(filtered.Windows) == 0 {
		return ""
	}
	return fingerprintImportValue(filtered)
}

func normalizeAggregatedUsageWindowForFingerprint(window AggregatedUsageWindow) AggregatedUsageWindow {
	normalized := cloneAggregatedUsageWindow(window)
	sort.Strings(normalized.ModelNames)
	sort.Slice(normalized.APIs, func(i, j int) bool {
		return normalized.APIs[i].Endpoint < normalized.APIs[j].Endpoint
	})
	sort.Slice(normalized.Models, func(i, j int) bool {
		return normalized.Models[i].Model < normalized.Models[j].Model
	})
	sort.Slice(normalized.Credentials, func(i, j int) bool {
		left := normalized.Credentials[i].Source + "\x00" + normalized.Credentials[i].AuthIndex
		right := normalized.Credentials[j].Source + "\x00" + normalized.Credentials[j].AuthIndex
		return left < right
	})
	return normalized
}

func cloneStringInt64Map(src map[string]int64) map[string]int64 {
	if len(src) == 0 {
		return map[string]int64{}
	}
	dst := make(map[string]int64, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func canonicalHourSeries(src map[string]int64) map[string]int64 {
	if len(src) == 0 {
		return map[string]int64{}
	}
	dst := make(map[string]int64, len(src))
	for hourKey, value := range src {
		hour, ok := parseSnapshotHour(hourKey)
		if !ok {
			continue
		}
		dst[formatHour(hour)] += value
	}
	return dst
}

func fingerprintImportValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
