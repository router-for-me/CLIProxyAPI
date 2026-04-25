package usage

import (
	"sort"
	"strings"
	"time"
)

func cloneAggregatedUsageSnapshot(src AggregatedUsageSnapshot) AggregatedUsageSnapshot {
	dst := AggregatedUsageSnapshot{
		GeneratedAt: src.GeneratedAt.UTC(),
		ModelNames:  append([]string(nil), src.ModelNames...),
		Windows:     make(map[string]AggregatedUsageWindow, len(src.Windows)),
	}
	for key, window := range src.Windows {
		dst.Windows[key] = cloneAggregatedUsageWindow(window)
	}
	return dst
}

func mergeAggregatedUsageSnapshot(base, extra AggregatedUsageSnapshot) AggregatedUsageSnapshot {
	if len(base.Windows) == 0 {
		return cloneAggregatedUsageSnapshot(extra)
	}
	if len(extra.Windows) == 0 {
		return cloneAggregatedUsageSnapshot(base)
	}

	dst := cloneAggregatedUsageSnapshot(base)
	if extra.GeneratedAt.After(dst.GeneratedAt) {
		dst.GeneratedAt = extra.GeneratedAt.UTC()
	}
	dst.ModelNames = mergeStringSlices(dst.ModelNames, extra.ModelNames)
	if dst.Windows == nil {
		dst.Windows = make(map[string]AggregatedUsageWindow, len(extra.Windows))
	}
	for key, window := range extra.Windows {
		if current, ok := dst.Windows[key]; ok {
			dst.Windows[key] = mergeAggregatedUsageWindow(current, window)
			continue
		}
		dst.Windows[key] = cloneAggregatedUsageWindow(window)
	}
	return dst
}

func cloneAggregatedUsageWindow(src AggregatedUsageWindow) AggregatedUsageWindow {
	return AggregatedUsageWindow{
		TotalRequests:        src.TotalRequests,
		SuccessCount:         src.SuccessCount,
		FailureCount:         src.FailureCount,
		TotalTokens:          src.TotalTokens,
		TokenBreakdown:       src.TokenBreakdown,
		Latency:              src.Latency,
		Rate30m:              src.Rate30m,
		Sparklines:           cloneAggregatedUsageSparkline(src.Sparklines),
		Requests:             cloneAggregatedUsageModelSeriesSet(src.Requests),
		Tokens:               cloneAggregatedUsageModelSeriesSet(src.Tokens),
		TokenBreakdownSeries: cloneAggregatedUsageTokenBreakdownSeriesSet(src.TokenBreakdownSeries),
		LatencySeries:        cloneAggregatedUsageLatencySeriesSet(src.LatencySeries),
		CostBasis:            cloneAggregatedUsageCostBasisSeriesSet(src.CostBasis),
		APIs:                 cloneAggregatedUsageAPIStatsList(src.APIs),
		Models:               cloneAggregatedUsageModelStatsList(src.Models),
		Credentials:          cloneAggregatedUsageCredentialStatsList(src.Credentials),
		ModelNames:           append([]string(nil), src.ModelNames...),
	}
}

func mergeAggregatedUsageWindow(base, extra AggregatedUsageWindow) AggregatedUsageWindow {
	dst := cloneAggregatedUsageWindow(base)
	dst.TotalRequests += extra.TotalRequests
	dst.SuccessCount += extra.SuccessCount
	dst.FailureCount += extra.FailureCount
	dst.TotalTokens += extra.TotalTokens
	mergeTokenStats(&dst.TokenBreakdown, extra.TokenBreakdown)
	mergeLatencyStats(&dst.Latency, extra.Latency)
	dst.Rate30m = mergeAggregatedUsageRate(dst.Rate30m, extra.Rate30m)
	dst.Sparklines = mergeAggregatedUsageSparkline(dst.Sparklines, extra.Sparklines)
	dst.Requests = mergeAggregatedUsageModelSeriesSet(dst.Requests, extra.Requests)
	dst.Tokens = mergeAggregatedUsageModelSeriesSet(dst.Tokens, extra.Tokens)
	dst.TokenBreakdownSeries = mergeAggregatedUsageTokenBreakdownSeriesSet(dst.TokenBreakdownSeries, extra.TokenBreakdownSeries)
	dst.LatencySeries = mergeAggregatedUsageLatencySeriesSet(dst.LatencySeries, extra.LatencySeries)
	dst.CostBasis = mergeAggregatedUsageCostBasisSeriesSet(dst.CostBasis, extra.CostBasis)
	dst.APIs = mergeAggregatedUsageAPIStatsList(dst.APIs, extra.APIs)
	dst.Models = mergeAggregatedUsageModelStatsList(dst.Models, extra.Models)
	dst.Credentials = mergeAggregatedUsageCredentialStatsList(dst.Credentials, extra.Credentials)
	dst.ModelNames = mergeStringSlices(dst.ModelNames, extra.ModelNames)
	return dst
}

func cloneAggregatedUsageSparkline(src AggregatedUsageSparkline) AggregatedUsageSparkline {
	return AggregatedUsageSparkline{
		Timestamps: cloneTimeSlice(src.Timestamps),
		Requests:   append([]int64(nil), src.Requests...),
		Tokens:     append([]int64(nil), src.Tokens...),
	}
}

func cloneAggregatedUsageModelSeriesSet(src AggregatedUsageModelSeriesSet) AggregatedUsageModelSeriesSet {
	return AggregatedUsageModelSeriesSet{
		Hour: cloneAggregatedUsageModelSeries(src.Hour),
		Day:  cloneAggregatedUsageModelSeries(src.Day),
	}
}

func cloneAggregatedUsageModelSeries(src AggregatedUsageModelSeries) AggregatedUsageModelSeries {
	dst := AggregatedUsageModelSeries{
		Timestamps: cloneTimeSlice(src.Timestamps),
		Series:     make(map[string][]int64, len(src.Series)),
	}
	for model, values := range src.Series {
		dst.Series[model] = append([]int64(nil), values...)
	}
	return dst
}

func cloneAggregatedUsageTokenBreakdownSeriesSet(src AggregatedUsageTokenBreakdownSeriesSet) AggregatedUsageTokenBreakdownSeriesSet {
	return AggregatedUsageTokenBreakdownSeriesSet{
		Hour: cloneAggregatedUsageTokenBreakdownSeries(src.Hour),
		Day:  cloneAggregatedUsageTokenBreakdownSeries(src.Day),
	}
}

func cloneAggregatedUsageTokenBreakdownSeries(src AggregatedUsageTokenBreakdownSeries) AggregatedUsageTokenBreakdownSeries {
	return AggregatedUsageTokenBreakdownSeries{
		Timestamps: cloneTimeSlice(src.Timestamps),
		Input:      append([]int64(nil), src.Input...),
		Output:     append([]int64(nil), src.Output...),
		Cached:     append([]int64(nil), src.Cached...),
		Reasoning:  append([]int64(nil), src.Reasoning...),
	}
}

func cloneAggregatedUsageLatencySeriesSet(src AggregatedUsageLatencySeriesSet) AggregatedUsageLatencySeriesSet {
	return AggregatedUsageLatencySeriesSet{
		Hour: cloneAggregatedUsageLatencySeries(src.Hour),
		Day:  cloneAggregatedUsageLatencySeries(src.Day),
	}
}

func cloneAggregatedUsageLatencySeries(src AggregatedUsageLatencySeries) AggregatedUsageLatencySeries {
	values := make([]*float64, len(src.Values))
	for idx, value := range src.Values {
		if value == nil {
			continue
		}
		copyValue := *value
		values[idx] = &copyValue
	}
	return AggregatedUsageLatencySeries{
		Timestamps: cloneTimeSlice(src.Timestamps),
		Values:     values,
		Counts:     append([]int64(nil), src.Counts...),
	}
}

func cloneAggregatedUsageCostBasisSeriesSet(src AggregatedUsageCostBasisSeriesSet) AggregatedUsageCostBasisSeriesSet {
	return AggregatedUsageCostBasisSeriesSet{
		Hour: cloneAggregatedUsageCostBasisSeries(src.Hour),
		Day:  cloneAggregatedUsageCostBasisSeries(src.Day),
	}
}

func cloneAggregatedUsageCostBasisSeries(src AggregatedUsageCostBasisSeries) AggregatedUsageCostBasisSeries {
	dst := AggregatedUsageCostBasisSeries{
		Timestamps: cloneTimeSlice(src.Timestamps),
		Models:     make(map[string]AggregatedUsageTokenSeries, len(src.Models)),
	}
	for model, series := range src.Models {
		dst.Models[model] = cloneAggregatedUsageTokenSeries(series)
	}
	return dst
}

func cloneAggregatedUsageTokenSeries(src AggregatedUsageTokenSeries) AggregatedUsageTokenSeries {
	return AggregatedUsageTokenSeries{
		Input:     append([]int64(nil), src.Input...),
		Output:    append([]int64(nil), src.Output...),
		Cached:    append([]int64(nil), src.Cached...),
		Reasoning: append([]int64(nil), src.Reasoning...),
		Total:     append([]int64(nil), src.Total...),
	}
}

func cloneAggregatedUsageAPIStatsList(src []AggregatedUsageAPIStats) []AggregatedUsageAPIStats {
	dst := make([]AggregatedUsageAPIStats, 0, len(src))
	for _, item := range src {
		copyItem := item
		copyItem.Models = make(map[string]AggregatedUsageAPIModelStats, len(item.Models))
		for model, stats := range item.Models {
			copyItem.Models[model] = stats
		}
		dst = append(dst, copyItem)
	}
	return dst
}

func cloneAggregatedUsageModelStatsList(src []AggregatedUsageModelStats) []AggregatedUsageModelStats {
	return append([]AggregatedUsageModelStats(nil), src...)
}

func cloneAggregatedUsageCredentialStatsList(src []AggregatedUsageCredentialStats) []AggregatedUsageCredentialStats {
	return append([]AggregatedUsageCredentialStats(nil), src...)
}

func mergeAggregatedUsageRate(base, extra AggregatedUsageRate) AggregatedUsageRate {
	dst := base
	if dst.WindowMinutes == 0 {
		dst.WindowMinutes = extra.WindowMinutes
	}
	dst.RequestCount += extra.RequestCount
	dst.TokenCount += extra.TokenCount
	if dst.WindowMinutes > 0 {
		dst.RPM = float64(dst.RequestCount) / float64(dst.WindowMinutes)
		dst.TPM = float64(dst.TokenCount) / float64(dst.WindowMinutes)
	}
	return dst
}

func mergeAggregatedUsageSparkline(base, extra AggregatedUsageSparkline) AggregatedUsageSparkline {
	timestamps := mergeTimeAxes(base.Timestamps, extra.Timestamps)
	requests := make([]int64, len(timestamps))
	tokens := make([]int64, len(timestamps))

	addInt64Series(requests, timestamps, base.Timestamps, base.Requests)
	addInt64Series(requests, timestamps, extra.Timestamps, extra.Requests)
	addInt64Series(tokens, timestamps, base.Timestamps, base.Tokens)
	addInt64Series(tokens, timestamps, extra.Timestamps, extra.Tokens)

	return AggregatedUsageSparkline{
		Timestamps: timestamps,
		Requests:   requests,
		Tokens:     tokens,
	}
}

func mergeAggregatedUsageModelSeriesSet(base, extra AggregatedUsageModelSeriesSet) AggregatedUsageModelSeriesSet {
	return AggregatedUsageModelSeriesSet{
		Hour: mergeAggregatedUsageModelSeries(base.Hour, extra.Hour),
		Day:  mergeAggregatedUsageModelSeries(base.Day, extra.Day),
	}
}

func mergeAggregatedUsageModelSeries(base, extra AggregatedUsageModelSeries) AggregatedUsageModelSeries {
	timestamps := mergeTimeAxes(base.Timestamps, extra.Timestamps)
	series := make(map[string][]int64)
	mergeModelSeriesMap(series, timestamps, base)
	mergeModelSeriesMap(series, timestamps, extra)
	return AggregatedUsageModelSeries{
		Timestamps: timestamps,
		Series:     series,
	}
}

func mergeAggregatedUsageTokenBreakdownSeriesSet(base, extra AggregatedUsageTokenBreakdownSeriesSet) AggregatedUsageTokenBreakdownSeriesSet {
	return AggregatedUsageTokenBreakdownSeriesSet{
		Hour: mergeAggregatedUsageTokenBreakdownSeries(base.Hour, extra.Hour),
		Day:  mergeAggregatedUsageTokenBreakdownSeries(base.Day, extra.Day),
	}
}

func mergeAggregatedUsageTokenBreakdownSeries(base, extra AggregatedUsageTokenBreakdownSeries) AggregatedUsageTokenBreakdownSeries {
	timestamps := mergeTimeAxes(base.Timestamps, extra.Timestamps)
	input := make([]int64, len(timestamps))
	output := make([]int64, len(timestamps))
	cached := make([]int64, len(timestamps))
	reasoning := make([]int64, len(timestamps))

	addInt64Series(input, timestamps, base.Timestamps, base.Input)
	addInt64Series(input, timestamps, extra.Timestamps, extra.Input)
	addInt64Series(output, timestamps, base.Timestamps, base.Output)
	addInt64Series(output, timestamps, extra.Timestamps, extra.Output)
	addInt64Series(cached, timestamps, base.Timestamps, base.Cached)
	addInt64Series(cached, timestamps, extra.Timestamps, extra.Cached)
	addInt64Series(reasoning, timestamps, base.Timestamps, base.Reasoning)
	addInt64Series(reasoning, timestamps, extra.Timestamps, extra.Reasoning)

	return AggregatedUsageTokenBreakdownSeries{
		Timestamps: timestamps,
		Input:      input,
		Output:     output,
		Cached:     cached,
		Reasoning:  reasoning,
	}
}

func mergeAggregatedUsageLatencySeriesSet(base, extra AggregatedUsageLatencySeriesSet) AggregatedUsageLatencySeriesSet {
	return AggregatedUsageLatencySeriesSet{
		Hour: mergeAggregatedUsageLatencySeries(base.Hour, extra.Hour),
		Day:  mergeAggregatedUsageLatencySeries(base.Day, extra.Day),
	}
}

func mergeAggregatedUsageLatencySeries(base, extra AggregatedUsageLatencySeries) AggregatedUsageLatencySeries {
	timestamps := mergeTimeAxes(base.Timestamps, extra.Timestamps)
	totals := make([]float64, len(timestamps))
	counts := make([]int64, len(timestamps))

	addLatencySeries(totals, counts, timestamps, base)
	addLatencySeries(totals, counts, timestamps, extra)

	values := make([]*float64, len(timestamps))
	for idx := range timestamps {
		if counts[idx] <= 0 {
			continue
		}
		avg := totals[idx] / float64(counts[idx])
		values[idx] = &avg
	}

	return AggregatedUsageLatencySeries{
		Timestamps: timestamps,
		Values:     values,
		Counts:     counts,
	}
}

func mergeAggregatedUsageCostBasisSeriesSet(base, extra AggregatedUsageCostBasisSeriesSet) AggregatedUsageCostBasisSeriesSet {
	return AggregatedUsageCostBasisSeriesSet{
		Hour: mergeAggregatedUsageCostBasisSeries(base.Hour, extra.Hour),
		Day:  mergeAggregatedUsageCostBasisSeries(base.Day, extra.Day),
	}
}

func mergeAggregatedUsageCostBasisSeries(base, extra AggregatedUsageCostBasisSeries) AggregatedUsageCostBasisSeries {
	timestamps := mergeTimeAxes(base.Timestamps, extra.Timestamps)
	models := make(map[string]AggregatedUsageTokenSeries)
	mergeCostBasisModels(models, timestamps, base)
	mergeCostBasisModels(models, timestamps, extra)
	return AggregatedUsageCostBasisSeries{
		Timestamps: timestamps,
		Models:     models,
	}
}

func mergeAggregatedUsageAPIStatsList(base, extra []AggregatedUsageAPIStats) []AggregatedUsageAPIStats {
	items := make(map[string]AggregatedUsageAPIStats, len(base)+len(extra))
	for _, item := range base {
		items[item.Endpoint] = item
	}
	for _, item := range extra {
		current, ok := items[item.Endpoint]
		if !ok {
			items[item.Endpoint] = item
			continue
		}
		current.TotalRequests += item.TotalRequests
		current.SuccessCount += item.SuccessCount
		current.FailureCount += item.FailureCount
		current.TotalTokens += item.TotalTokens
		mergeTokenStats(&current.TokenBreakdown, item.TokenBreakdown)
		mergeLatencyStats(&current.Latency, item.Latency)
		if current.Models == nil {
			current.Models = make(map[string]AggregatedUsageAPIModelStats, len(item.Models))
		}
		for model, stats := range item.Models {
			existing := current.Models[model]
			existing.Requests += stats.Requests
			existing.SuccessCount += stats.SuccessCount
			existing.FailureCount += stats.FailureCount
			existing.Tokens += stats.Tokens
			mergeTokenStats(&existing.TokenBreakdown, stats.TokenBreakdown)
			mergeLatencyStats(&existing.Latency, stats.Latency)
			current.Models[model] = existing
		}
		items[item.Endpoint] = current
	}

	result := make([]AggregatedUsageAPIStats, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalRequests == result[j].TotalRequests {
			return result[i].Endpoint < result[j].Endpoint
		}
		return result[i].TotalRequests > result[j].TotalRequests
	})
	return result
}

func mergeAggregatedUsageModelStatsList(base, extra []AggregatedUsageModelStats) []AggregatedUsageModelStats {
	items := make(map[string]AggregatedUsageModelStats, len(base)+len(extra))
	for _, item := range base {
		items[item.Model] = item
	}
	for _, item := range extra {
		current := items[item.Model]
		current.Model = item.Model
		current.Requests += item.Requests
		current.SuccessCount += item.SuccessCount
		current.FailureCount += item.FailureCount
		current.Tokens += item.Tokens
		mergeTokenStats(&current.TokenBreakdown, item.TokenBreakdown)
		mergeLatencyStats(&current.Latency, item.Latency)
		items[item.Model] = current
	}

	result := make([]AggregatedUsageModelStats, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Requests == result[j].Requests {
			return result[i].Model < result[j].Model
		}
		return result[i].Requests > result[j].Requests
	})
	return result
}

func mergeAggregatedUsageCredentialStatsList(base, extra []AggregatedUsageCredentialStats) []AggregatedUsageCredentialStats {
	items := make(map[string]AggregatedUsageCredentialStats, len(base)+len(extra))
	for _, item := range base {
		items[item.Source+"\x00"+item.AuthIndex] = item
	}
	for _, item := range extra {
		key := item.Source + "\x00" + item.AuthIndex
		current := items[key]
		current.Source = item.Source
		current.AuthIndex = item.AuthIndex
		current.TotalRequests += item.TotalRequests
		current.SuccessCount += item.SuccessCount
		current.FailureCount += item.FailureCount
		current.TotalTokens += item.TotalTokens
		items[key] = current
	}

	result := make([]AggregatedUsageCredentialStats, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalRequests == result[j].TotalRequests {
			if result[i].Source == result[j].Source {
				return result[i].AuthIndex < result[j].AuthIndex
			}
			return result[i].Source < result[j].Source
		}
		return result[i].TotalRequests > result[j].TotalRequests
	})
	return result
}

func cloneTimeSlice(src []time.Time) []time.Time {
	dst := make([]time.Time, len(src))
	for idx, ts := range src {
		dst[idx] = ts.UTC()
	}
	return dst
}

func mergeStringSlices(base, extra []string) []string {
	if len(base) == 0 {
		return append([]string(nil), extra...)
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	result := make([]string, 0, len(base)+len(extra))
	for _, item := range base {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	for _, item := range extra {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func mergeTimeAxes(base, extra []time.Time) []time.Time {
	if len(base) == 0 {
		return cloneTimeSlice(extra)
	}
	if len(extra) == 0 {
		return cloneTimeSlice(base)
	}
	seen := make(map[time.Time]struct{}, len(base)+len(extra))
	result := make([]time.Time, 0, len(base)+len(extra))
	appendUnique := func(items []time.Time) {
		for _, ts := range items {
			utc := ts.UTC()
			if _, ok := seen[utc]; ok {
				continue
			}
			seen[utc] = struct{}{}
			result = append(result, utc)
		}
	}
	appendUnique(base)
	appendUnique(extra)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Before(result[j])
	})
	return result
}

func addInt64Series(target []int64, axis, sourceAxis []time.Time, sourceValues []int64) {
	if len(sourceAxis) == 0 || len(sourceValues) == 0 {
		return
	}
	index := make(map[time.Time]int, len(axis))
	for idx, ts := range axis {
		index[ts.UTC()] = idx
	}
	for sourceIdx, ts := range sourceAxis {
		if sourceIdx >= len(sourceValues) {
			break
		}
		targetIdx, ok := index[ts.UTC()]
		if !ok {
			continue
		}
		target[targetIdx] += sourceValues[sourceIdx]
	}
}

func mergeModelSeriesMap(target map[string][]int64, axis []time.Time, source AggregatedUsageModelSeries) {
	if len(source.Series) == 0 {
		return
	}
	index := make(map[time.Time]int, len(axis))
	for idx, ts := range axis {
		index[ts.UTC()] = idx
	}
	for model, values := range source.Series {
		if _, ok := target[model]; !ok {
			target[model] = make([]int64, len(axis))
		}
		for sourceIdx, ts := range source.Timestamps {
			if sourceIdx >= len(values) {
				break
			}
			targetIdx, ok := index[ts.UTC()]
			if !ok {
				continue
			}
			target[model][targetIdx] += values[sourceIdx]
		}
	}
}

func addLatencySeries(totals []float64, counts []int64, axis []time.Time, source AggregatedUsageLatencySeries) {
	if len(source.Timestamps) == 0 || len(source.Values) == 0 {
		return
	}
	index := make(map[time.Time]int, len(axis))
	for idx, ts := range axis {
		index[ts.UTC()] = idx
	}
	for sourceIdx, ts := range source.Timestamps {
		if sourceIdx >= len(source.Values) {
			break
		}
		value := source.Values[sourceIdx]
		if value == nil {
			continue
		}
		targetIdx, ok := index[ts.UTC()]
		if !ok {
			continue
		}
		weight := int64(1)
		if sourceIdx < len(source.Counts) && source.Counts[sourceIdx] > 0 {
			weight = source.Counts[sourceIdx]
		}
		totals[targetIdx] += *value * float64(weight)
		counts[targetIdx] += weight
	}
}

func mergeCostBasisModels(target map[string]AggregatedUsageTokenSeries, axis []time.Time, source AggregatedUsageCostBasisSeries) {
	if len(source.Models) == 0 {
		return
	}
	index := make(map[time.Time]int, len(axis))
	for idx, ts := range axis {
		index[ts.UTC()] = idx
	}
	for model, series := range source.Models {
		current := target[model]
		if len(current.Total) == 0 {
			current = AggregatedUsageTokenSeries{
				Input:     make([]int64, len(axis)),
				Output:    make([]int64, len(axis)),
				Cached:    make([]int64, len(axis)),
				Reasoning: make([]int64, len(axis)),
				Total:     make([]int64, len(axis)),
			}
		}
		for sourceIdx, ts := range source.Timestamps {
			targetIdx, ok := index[ts.UTC()]
			if !ok {
				continue
			}
			if sourceIdx < len(series.Input) {
				current.Input[targetIdx] += series.Input[sourceIdx]
			}
			if sourceIdx < len(series.Output) {
				current.Output[targetIdx] += series.Output[sourceIdx]
			}
			if sourceIdx < len(series.Cached) {
				current.Cached[targetIdx] += series.Cached[sourceIdx]
			}
			if sourceIdx < len(series.Reasoning) {
				current.Reasoning[targetIdx] += series.Reasoning[sourceIdx]
			}
			if sourceIdx < len(series.Total) {
				current.Total[targetIdx] += series.Total[sourceIdx]
			}
		}
		target[model] = current
	}
}
