package usage

import (
	"sort"
	"strings"
	"time"
)

type AggregatedUsageSnapshot struct {
	GeneratedAt time.Time                        `json:"generated_at"`
	ModelNames  []string                         `json:"model_names,omitempty"`
	Windows     map[string]AggregatedUsageWindow `json:"windows"`
}

type AggregatedUsageWindow struct {
	TotalRequests        int64                                  `json:"total_requests"`
	SuccessCount         int64                                  `json:"success_count"`
	FailureCount         int64                                  `json:"failure_count"`
	TotalTokens          int64                                  `json:"total_tokens"`
	TokenBreakdown       TokenStats                             `json:"token_breakdown"`
	Latency              LatencyStats                           `json:"latency"`
	Rate30m              AggregatedUsageRate                    `json:"rate_30m"`
	Sparklines           AggregatedUsageSparkline               `json:"sparklines"`
	Requests             AggregatedUsageModelSeriesSet          `json:"requests"`
	Tokens               AggregatedUsageModelSeriesSet          `json:"tokens"`
	TokenBreakdownSeries AggregatedUsageTokenBreakdownSeriesSet `json:"token_breakdown_series"`
	LatencySeries        AggregatedUsageLatencySeriesSet        `json:"latency_series"`
	CostBasis            AggregatedUsageCostBasisSeriesSet      `json:"cost_basis"`
	APIs                 []AggregatedUsageAPIStats              `json:"apis"`
	Models               []AggregatedUsageModelStats            `json:"models"`
	Credentials          []AggregatedUsageCredentialStats       `json:"credentials"`
	ModelNames           []string                               `json:"model_names,omitempty"`
}

type AggregatedUsageRate struct {
	WindowMinutes int64   `json:"window_minutes"`
	RequestCount  int64   `json:"request_count"`
	TokenCount    int64   `json:"token_count"`
	RPM           float64 `json:"rpm"`
	TPM           float64 `json:"tpm"`
}

type AggregatedUsageSparkline struct {
	Timestamps []time.Time `json:"timestamps"`
	Requests   []int64     `json:"requests"`
	Tokens     []int64     `json:"tokens"`
}

type AggregatedUsageModelSeriesSet struct {
	Hour AggregatedUsageModelSeries `json:"hour"`
	Day  AggregatedUsageModelSeries `json:"day"`
}

type AggregatedUsageModelSeries struct {
	Timestamps []time.Time        `json:"timestamps"`
	Series     map[string][]int64 `json:"series,omitempty"`
}

type AggregatedUsageTokenBreakdownSeriesSet struct {
	Hour AggregatedUsageTokenBreakdownSeries `json:"hour"`
	Day  AggregatedUsageTokenBreakdownSeries `json:"day"`
}

type AggregatedUsageTokenBreakdownSeries struct {
	Timestamps []time.Time `json:"timestamps"`
	Input      []int64     `json:"input"`
	Output     []int64     `json:"output"`
	Cached     []int64     `json:"cached"`
	Reasoning  []int64     `json:"reasoning"`
}

type AggregatedUsageLatencySeriesSet struct {
	Hour AggregatedUsageLatencySeries `json:"hour"`
	Day  AggregatedUsageLatencySeries `json:"day"`
}

type AggregatedUsageLatencySeries struct {
	Timestamps []time.Time `json:"timestamps"`
	Values     []*float64  `json:"values"`
	Counts     []int64     `json:"counts,omitempty"`
}

type AggregatedUsageCostBasisSeriesSet struct {
	Hour AggregatedUsageCostBasisSeries `json:"hour"`
	Day  AggregatedUsageCostBasisSeries `json:"day"`
}

type AggregatedUsageCostBasisSeries struct {
	Timestamps []time.Time                           `json:"timestamps"`
	Models     map[string]AggregatedUsageTokenSeries `json:"models,omitempty"`
}

type AggregatedUsageTokenSeries struct {
	Input     []int64 `json:"input"`
	Output    []int64 `json:"output"`
	Cached    []int64 `json:"cached"`
	Reasoning []int64 `json:"reasoning"`
	Total     []int64 `json:"total"`
}

type AggregatedUsageAPIStats struct {
	Endpoint       string                                  `json:"endpoint"`
	TotalRequests  int64                                   `json:"total_requests"`
	SuccessCount   int64                                   `json:"success_count"`
	FailureCount   int64                                   `json:"failure_count"`
	TotalTokens    int64                                   `json:"total_tokens"`
	TokenBreakdown TokenStats                              `json:"token_breakdown"`
	Latency        LatencyStats                            `json:"latency"`
	Models         map[string]AggregatedUsageAPIModelStats `json:"models,omitempty"`
}

type AggregatedUsageAPIModelStats struct {
	Requests       int64        `json:"requests"`
	SuccessCount   int64        `json:"success_count"`
	FailureCount   int64        `json:"failure_count"`
	Tokens         int64        `json:"tokens"`
	TokenBreakdown TokenStats   `json:"token_breakdown"`
	Latency        LatencyStats `json:"latency"`
}

type AggregatedUsageModelStats struct {
	Model          string       `json:"model"`
	Requests       int64        `json:"requests"`
	SuccessCount   int64        `json:"success_count"`
	FailureCount   int64        `json:"failure_count"`
	Tokens         int64        `json:"tokens"`
	TokenBreakdown TokenStats   `json:"token_breakdown"`
	Latency        LatencyStats `json:"latency"`
}

type AggregatedUsageCredentialStats struct {
	Source        string `json:"source,omitempty"`
	AuthIndex     string `json:"auth_index,omitempty"`
	TotalRequests int64  `json:"total_requests"`
	SuccessCount  int64  `json:"success_count"`
	FailureCount  int64  `json:"failure_count"`
	TotalTokens   int64  `json:"total_tokens"`
}

type aggregatedUsageWindowConfig struct {
	key             string
	duration        time.Duration
	hourWindowHours int
}

var aggregatedUsageWindowConfigs = []aggregatedUsageWindowConfig{
	{key: "1h", duration: time.Hour, hourWindowHours: 1},
	{key: "3h", duration: 3 * time.Hour, hourWindowHours: 3},
	{key: "6h", duration: 6 * time.Hour, hourWindowHours: 6},
	{key: "12h", duration: 12 * time.Hour, hourWindowHours: 12},
	{key: "24h", duration: 24 * time.Hour, hourWindowHours: 24},
	{key: "7d", duration: 7 * 24 * time.Hour, hourWindowHours: 7 * 24},
	{key: "all", duration: 0, hourWindowHours: 24},
}

type aggregatedUsageWindowAccumulator struct {
	cfg aggregatedUsageWindowConfig

	now           time.Time
	includeStart  time.Time
	hasIncludeCut bool

	rateStart         time.Time
	sparkStart        time.Time
	sparkTimestamps   []time.Time
	requestHourStart  time.Time
	requestHourCount  int
	analysisHourStart time.Time
	analysisBucket    time.Duration
	analysisCount     int
	dayStart          time.Time
	dayEnd            time.Time
	dynamicDayRange   bool
	minDayBucket      time.Time
	maxDayBucket      time.Time
	hasDayData        bool

	totalRequests  int64
	successCount   int64
	failureCount   int64
	totalTokens    int64
	tokenBreakdown TokenStats
	latency        LatencyStats

	rateRequests int64
	rateTokens   int64

	sparkRequests []int64
	sparkTokens   []int64

	requestsHour map[string]map[time.Time]int64
	tokensHour   map[string]map[time.Time]int64
	requestsDay  map[string]map[time.Time]int64
	tokensDay    map[string]map[time.Time]int64

	tokenBreakdownHour map[time.Time]TokenStats
	tokenBreakdownDay  map[time.Time]TokenStats
	latencyHour        map[time.Time]LatencyStats
	latencyDay         map[time.Time]LatencyStats
	costBasisHour      map[string]map[time.Time]TokenStats
	costBasisDay       map[string]map[time.Time]TokenStats

	apiStats    map[string]*aggregatedUsageAPIAccumulator
	modelStats  map[string]*aggregatedUsageModelAccumulator
	credentials map[string]*aggregatedUsageCredentialAccumulator
	modelNames  map[string]struct{}
}

type aggregatedUsageAPIAccumulator struct {
	endpoint       string
	totalRequests  int64
	successCount   int64
	failureCount   int64
	totalTokens    int64
	tokenBreakdown TokenStats
	latency        LatencyStats
	models         map[string]*aggregatedUsageModelAccumulator
}

type aggregatedUsageModelAccumulator struct {
	requests       int64
	successCount   int64
	failureCount   int64
	tokens         int64
	tokenBreakdown TokenStats
	latency        LatencyStats
}

type aggregatedUsageCredentialAccumulator struct {
	source        string
	authIndex     string
	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64
}

func (s *RequestStatistics) AggregatedUsageSnapshot(now time.Time) AggregatedUsageSnapshot {
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result := AggregatedUsageSnapshot{
		GeneratedAt: now,
		Windows:     make(map[string]AggregatedUsageWindow, len(aggregatedUsageWindowConfigs)),
	}
	if s == nil {
		return result
	}

	accumulators := make(map[string]*aggregatedUsageWindowAccumulator, len(aggregatedUsageWindowConfigs))
	for _, cfg := range aggregatedUsageWindowConfigs {
		accumulators[cfg.key] = newAggregatedUsageWindowAccumulator(cfg, now)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	globalModelNames := make(map[string]struct{})
	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			for _, detail := range modelStatsValue.Details {
				normalizedDetail := detail
				normalizedDetail.Timestamp = normalizedDetail.Timestamp.UTC()
				normalizedDetail.Tokens = normaliseTokenStats(normalizedDetail.Tokens)
				if normalizedDetail.Timestamp.IsZero() {
					continue
				}
				if strings.TrimSpace(modelName) != "" {
					globalModelNames[modelName] = struct{}{}
				}
				for _, cfg := range aggregatedUsageWindowConfigs {
					acc := accumulators[cfg.key]
					if acc == nil || !acc.includes(normalizedDetail.Timestamp) {
						continue
					}
					acc.addRecord(apiName, modelName, normalizedDetail)
				}
			}
		}
	}

	result.ModelNames = sortedStringKeys(globalModelNames)
	for _, cfg := range aggregatedUsageWindowConfigs {
		if acc := accumulators[cfg.key]; acc != nil {
			result.Windows[cfg.key] = acc.build()
		}
	}

	if s.importedAggregated != nil {
		result = mergeAggregatedUsageSnapshot(result, *s.importedAggregated)
	}

	return result
}

func newAggregatedUsageWindowAccumulator(cfg aggregatedUsageWindowConfig, now time.Time) *aggregatedUsageWindowAccumulator {
	requestHourCount := cfg.hourWindowHours
	if requestHourCount <= 0 {
		requestHourCount = 24
	}
	requestHourEnd := truncateToHourUTC(now)
	requestHourStart := requestHourEnd.Add(-time.Duration(requestHourCount-1) * time.Hour)

	analysisBucket := resolveAnalysisBucket(cfg.hourWindowHours)
	analysisCount := int((time.Duration(requestHourCount) * time.Hour) / analysisBucket)
	if analysisCount <= 0 {
		analysisCount = 1
	}
	analysisEnd := truncateToBucketUTC(now, analysisBucket)
	analysisStart := analysisEnd.Add(-time.Duration(analysisCount-1) * analysisBucket)

	sparkEnd := now.UTC().Truncate(time.Minute)
	sparkStart := sparkEnd.Add(-59 * time.Minute)
	sparkTimestamps := make([]time.Time, 60)
	for i := range sparkTimestamps {
		sparkTimestamps[i] = sparkStart.Add(time.Duration(i) * time.Minute)
	}

	dayEnd := truncateToDayUTC(now)
	dayStart := dayEnd
	dynamicDayRange := cfg.duration == 0
	if !dynamicDayRange {
		dayStart = truncateToDayUTC(now.Add(-cfg.duration))
	}

	acc := &aggregatedUsageWindowAccumulator{
		cfg:                cfg,
		now:                now,
		rateStart:          now.Add(-30 * time.Minute),
		sparkStart:         sparkStart,
		sparkTimestamps:    sparkTimestamps,
		requestHourStart:   requestHourStart,
		requestHourCount:   requestHourCount,
		analysisHourStart:  analysisStart,
		analysisBucket:     analysisBucket,
		analysisCount:      analysisCount,
		dayStart:           dayStart,
		dayEnd:             dayEnd,
		dynamicDayRange:    dynamicDayRange,
		sparkRequests:      make([]int64, len(sparkTimestamps)),
		sparkTokens:        make([]int64, len(sparkTimestamps)),
		requestsHour:       make(map[string]map[time.Time]int64),
		tokensHour:         make(map[string]map[time.Time]int64),
		requestsDay:        make(map[string]map[time.Time]int64),
		tokensDay:          make(map[string]map[time.Time]int64),
		tokenBreakdownHour: make(map[time.Time]TokenStats),
		tokenBreakdownDay:  make(map[time.Time]TokenStats),
		latencyHour:        make(map[time.Time]LatencyStats),
		latencyDay:         make(map[time.Time]LatencyStats),
		costBasisHour:      make(map[string]map[time.Time]TokenStats),
		costBasisDay:       make(map[string]map[time.Time]TokenStats),
		apiStats:           make(map[string]*aggregatedUsageAPIAccumulator),
		modelStats:         make(map[string]*aggregatedUsageModelAccumulator),
		credentials:        make(map[string]*aggregatedUsageCredentialAccumulator),
		modelNames:         make(map[string]struct{}),
	}
	if cfg.duration > 0 {
		acc.hasIncludeCut = true
		acc.includeStart = now.Add(-cfg.duration)
	}
	return acc
}

func (a *aggregatedUsageWindowAccumulator) includes(ts time.Time) bool {
	if a == nil {
		return false
	}
	if a.hasIncludeCut && ts.Before(a.includeStart) {
		return false
	}
	return !ts.After(a.now)
}

func (a *aggregatedUsageWindowAccumulator) addRecord(apiName, modelName string, detail RequestDetail) {
	if a == nil {
		return
	}

	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		modelName = "unknown"
	}
	tokens := normaliseTokenStats(detail.Tokens)

	a.totalRequests++
	if detail.Failed {
		a.failureCount++
	} else {
		a.successCount++
	}
	a.totalTokens += tokens.TotalTokens
	mergeTokenStats(&a.tokenBreakdown, tokens)
	addLatencySample(&a.latency, detail.LatencyMs)
	a.modelNames[modelName] = struct{}{}

	if !detail.Timestamp.Before(a.rateStart) {
		a.rateRequests++
		a.rateTokens += tokens.TotalTokens
	}

	a.addSparkline(detail.Timestamp, tokens.TotalTokens)
	a.addModelSeries(a.requestsHour, modelName, truncateToHourUTC(detail.Timestamp), 1)
	a.addModelSeries(a.tokensHour, modelName, truncateToHourUTC(detail.Timestamp), tokens.TotalTokens)
	dayBucket := truncateToDayUTC(detail.Timestamp)
	a.addModelSeries(a.requestsDay, modelName, dayBucket, 1)
	a.addModelSeries(a.tokensDay, modelName, dayBucket, tokens.TotalTokens)
	a.addTokenSeries(a.tokenBreakdownHour, truncateToBucketUTC(detail.Timestamp, a.analysisBucket), tokens)
	a.addTokenSeries(a.tokenBreakdownDay, dayBucket, tokens)
	a.addLatencySeries(a.latencyHour, truncateToBucketUTC(detail.Timestamp, a.analysisBucket), detail.LatencyMs)
	a.addLatencySeries(a.latencyDay, dayBucket, detail.LatencyMs)
	a.addCostBasisSeries(a.costBasisHour, modelName, truncateToBucketUTC(detail.Timestamp, a.analysisBucket), tokens)
	a.addCostBasisSeries(a.costBasisDay, modelName, dayBucket, tokens)
	a.trackDayRange(dayBucket)
	a.addAPIAggregate(apiName, modelName, detail, tokens)
	a.addModelAggregate(modelName, detail, tokens)
	a.addCredentialAggregate(detail, tokens)
}

func (a *aggregatedUsageWindowAccumulator) addSparkline(ts time.Time, totalTokens int64) {
	if a == nil {
		return
	}
	bucket := ts.UTC().Truncate(time.Minute)
	if bucket.Before(a.sparkStart) || bucket.After(a.sparkTimestamps[len(a.sparkTimestamps)-1]) {
		return
	}
	index := int(bucket.Sub(a.sparkStart) / time.Minute)
	if index < 0 || index >= len(a.sparkRequests) {
		return
	}
	a.sparkRequests[index]++
	a.sparkTokens[index] += totalTokens
}

func (a *aggregatedUsageWindowAccumulator) addModelSeries(target map[string]map[time.Time]int64, modelName string, bucket time.Time, value int64) {
	if a == nil || value == 0 {
		return
	}
	modelBuckets := target[modelName]
	if modelBuckets == nil {
		modelBuckets = make(map[time.Time]int64)
		target[modelName] = modelBuckets
	}
	modelBuckets[bucket] += value
}

func (a *aggregatedUsageWindowAccumulator) addTokenSeries(target map[time.Time]TokenStats, bucket time.Time, tokens TokenStats) {
	if a == nil {
		return
	}
	current := target[bucket]
	mergeTokenStats(&current, tokens)
	target[bucket] = current
}

func (a *aggregatedUsageWindowAccumulator) addLatencySeries(target map[time.Time]LatencyStats, bucket time.Time, latencyMs int64) {
	if a == nil || latencyMs <= 0 {
		return
	}
	current := target[bucket]
	addLatencySample(&current, latencyMs)
	target[bucket] = current
}

func (a *aggregatedUsageWindowAccumulator) addCostBasisSeries(target map[string]map[time.Time]TokenStats, modelName string, bucket time.Time, tokens TokenStats) {
	if a == nil {
		return
	}
	modelBuckets := target[modelName]
	if modelBuckets == nil {
		modelBuckets = make(map[time.Time]TokenStats)
		target[modelName] = modelBuckets
	}
	current := modelBuckets[bucket]
	mergeTokenStats(&current, tokens)
	modelBuckets[bucket] = current
}

func (a *aggregatedUsageWindowAccumulator) trackDayRange(bucket time.Time) {
	if a == nil {
		return
	}
	if !a.dynamicDayRange {
		return
	}
	if !a.hasDayData {
		a.minDayBucket = bucket
		a.maxDayBucket = bucket
		a.hasDayData = true
		return
	}
	if bucket.Before(a.minDayBucket) {
		a.minDayBucket = bucket
	}
	if bucket.After(a.maxDayBucket) {
		a.maxDayBucket = bucket
	}
}

func (a *aggregatedUsageWindowAccumulator) addAPIAggregate(apiName, modelName string, detail RequestDetail, tokens TokenStats) {
	if a == nil {
		return
	}
	apiName = strings.TrimSpace(apiName)
	if apiName == "" {
		apiName = "unknown"
	}
	apiAcc := a.apiStats[apiName]
	if apiAcc == nil {
		apiAcc = &aggregatedUsageAPIAccumulator{
			endpoint: apiName,
			models:   make(map[string]*aggregatedUsageModelAccumulator),
		}
		a.apiStats[apiName] = apiAcc
	}
	apiAcc.totalRequests++
	if detail.Failed {
		apiAcc.failureCount++
	} else {
		apiAcc.successCount++
	}
	apiAcc.totalTokens += tokens.TotalTokens
	mergeTokenStats(&apiAcc.tokenBreakdown, tokens)
	addLatencySample(&apiAcc.latency, detail.LatencyMs)

	modelAcc := apiAcc.models[modelName]
	if modelAcc == nil {
		modelAcc = &aggregatedUsageModelAccumulator{}
		apiAcc.models[modelName] = modelAcc
	}
	modelAcc.requests++
	if detail.Failed {
		modelAcc.failureCount++
	} else {
		modelAcc.successCount++
	}
	modelAcc.tokens += tokens.TotalTokens
	mergeTokenStats(&modelAcc.tokenBreakdown, tokens)
	addLatencySample(&modelAcc.latency, detail.LatencyMs)
}

func (a *aggregatedUsageWindowAccumulator) addModelAggregate(modelName string, detail RequestDetail, tokens TokenStats) {
	if a == nil {
		return
	}
	modelAcc := a.modelStats[modelName]
	if modelAcc == nil {
		modelAcc = &aggregatedUsageModelAccumulator{}
		a.modelStats[modelName] = modelAcc
	}
	modelAcc.requests++
	if detail.Failed {
		modelAcc.failureCount++
	} else {
		modelAcc.successCount++
	}
	modelAcc.tokens += tokens.TotalTokens
	mergeTokenStats(&modelAcc.tokenBreakdown, tokens)
	addLatencySample(&modelAcc.latency, detail.LatencyMs)
}

func (a *aggregatedUsageWindowAccumulator) addCredentialAggregate(detail RequestDetail, tokens TokenStats) {
	if a == nil {
		return
	}
	source := strings.TrimSpace(detail.Source)
	authIndex := strings.TrimSpace(detail.AuthIndex)
	credKey := source + "\x00" + authIndex
	acc := a.credentials[credKey]
	if acc == nil {
		acc = &aggregatedUsageCredentialAccumulator{
			source:    source,
			authIndex: authIndex,
		}
		a.credentials[credKey] = acc
	}
	acc.totalRequests++
	if detail.Failed {
		acc.failureCount++
	} else {
		acc.successCount++
	}
	acc.totalTokens += tokens.TotalTokens
}

func (a *aggregatedUsageWindowAccumulator) build() AggregatedUsageWindow {
	windowMinutes := int64(30)
	rate := AggregatedUsageRate{
		WindowMinutes: windowMinutes,
		RequestCount:  a.rateRequests,
		TokenCount:    a.rateTokens,
	}
	if windowMinutes > 0 {
		rate.RPM = float64(rate.RequestCount) / float64(windowMinutes)
		rate.TPM = float64(rate.TokenCount) / float64(windowMinutes)
	}

	return AggregatedUsageWindow{
		TotalRequests:        a.totalRequests,
		SuccessCount:         a.successCount,
		FailureCount:         a.failureCount,
		TotalTokens:          a.totalTokens,
		TokenBreakdown:       a.tokenBreakdown,
		Latency:              a.latency,
		Rate30m:              rate,
		Sparklines:           AggregatedUsageSparkline{Timestamps: append([]time.Time(nil), a.sparkTimestamps...), Requests: append([]int64(nil), a.sparkRequests...), Tokens: append([]int64(nil), a.sparkTokens...)},
		Requests:             AggregatedUsageModelSeriesSet{Hour: a.buildBoundedModelSeries(a.requestsHour, a.requestHourStart, time.Hour, a.requestHourCount), Day: a.buildDayModelSeries(a.requestsDay)},
		Tokens:               AggregatedUsageModelSeriesSet{Hour: a.buildBoundedModelSeries(a.tokensHour, a.requestHourStart, time.Hour, a.requestHourCount), Day: a.buildDayModelSeries(a.tokensDay)},
		TokenBreakdownSeries: AggregatedUsageTokenBreakdownSeriesSet{Hour: a.buildBoundedTokenBreakdownSeries(a.tokenBreakdownHour, a.analysisHourStart, a.analysisBucket, a.analysisCount), Day: a.buildDayTokenBreakdownSeries(a.tokenBreakdownDay)},
		LatencySeries:        AggregatedUsageLatencySeriesSet{Hour: a.buildBoundedLatencySeries(a.latencyHour, a.analysisHourStart, a.analysisBucket, a.analysisCount), Day: a.buildDayLatencySeries(a.latencyDay)},
		CostBasis:            AggregatedUsageCostBasisSeriesSet{Hour: a.buildBoundedCostBasisSeries(a.costBasisHour, a.analysisHourStart, a.analysisBucket, a.analysisCount), Day: a.buildDayCostBasisSeries(a.costBasisDay)},
		APIs:                 a.buildAPIStats(),
		Models:               a.buildModelStats(),
		Credentials:          a.buildCredentialStats(),
		ModelNames:           sortedStringKeys(a.modelNames),
	}
}

func (a *aggregatedUsageWindowAccumulator) buildBoundedModelSeries(source map[string]map[time.Time]int64, start time.Time, step time.Duration, count int) AggregatedUsageModelSeries {
	timestamps := buildBoundedTimestamps(start, step, count)
	series := make(map[string][]int64, len(source))
	for modelName, buckets := range source {
		values := make([]int64, len(timestamps))
		for idx, ts := range timestamps {
			values[idx] = buckets[ts]
		}
		series[modelName] = values
	}
	return AggregatedUsageModelSeries{
		Timestamps: timestamps,
		Series:     series,
	}
}

func (a *aggregatedUsageWindowAccumulator) buildDayModelSeries(source map[string]map[time.Time]int64) AggregatedUsageModelSeries {
	timestamps := a.buildDayTimestamps()
	series := make(map[string][]int64, len(source))
	for modelName, buckets := range source {
		values := make([]int64, len(timestamps))
		for idx, ts := range timestamps {
			values[idx] = buckets[ts]
		}
		series[modelName] = values
	}
	return AggregatedUsageModelSeries{
		Timestamps: timestamps,
		Series:     series,
	}
}

func (a *aggregatedUsageWindowAccumulator) buildBoundedTokenBreakdownSeries(source map[time.Time]TokenStats, start time.Time, step time.Duration, count int) AggregatedUsageTokenBreakdownSeries {
	timestamps := buildBoundedTimestamps(start, step, count)
	result := AggregatedUsageTokenBreakdownSeries{
		Timestamps: timestamps,
		Input:      make([]int64, len(timestamps)),
		Output:     make([]int64, len(timestamps)),
		Cached:     make([]int64, len(timestamps)),
		Reasoning:  make([]int64, len(timestamps)),
	}
	for idx, ts := range timestamps {
		tokens := source[ts]
		result.Input[idx] = tokens.InputTokens
		result.Output[idx] = tokens.OutputTokens
		result.Cached[idx] = tokens.CachedTokens
		result.Reasoning[idx] = tokens.ReasoningTokens
	}
	return result
}

func (a *aggregatedUsageWindowAccumulator) buildDayTokenBreakdownSeries(source map[time.Time]TokenStats) AggregatedUsageTokenBreakdownSeries {
	timestamps := a.buildDayTimestamps()
	result := AggregatedUsageTokenBreakdownSeries{
		Timestamps: timestamps,
		Input:      make([]int64, len(timestamps)),
		Output:     make([]int64, len(timestamps)),
		Cached:     make([]int64, len(timestamps)),
		Reasoning:  make([]int64, len(timestamps)),
	}
	for idx, ts := range timestamps {
		tokens := source[ts]
		result.Input[idx] = tokens.InputTokens
		result.Output[idx] = tokens.OutputTokens
		result.Cached[idx] = tokens.CachedTokens
		result.Reasoning[idx] = tokens.ReasoningTokens
	}
	return result
}

func (a *aggregatedUsageWindowAccumulator) buildBoundedLatencySeries(source map[time.Time]LatencyStats, start time.Time, step time.Duration, count int) AggregatedUsageLatencySeries {
	timestamps := buildBoundedTimestamps(start, step, count)
	values := make([]*float64, len(timestamps))
	counts := make([]int64, len(timestamps))
	for idx, ts := range timestamps {
		stats := source[ts]
		if stats.Count <= 0 {
			continue
		}
		avg := float64(stats.TotalMs) / float64(stats.Count)
		values[idx] = &avg
		counts[idx] = stats.Count
	}
	return AggregatedUsageLatencySeries{
		Timestamps: timestamps,
		Values:     values,
		Counts:     counts,
	}
}

func (a *aggregatedUsageWindowAccumulator) buildDayLatencySeries(source map[time.Time]LatencyStats) AggregatedUsageLatencySeries {
	timestamps := a.buildDayTimestamps()
	values := make([]*float64, len(timestamps))
	counts := make([]int64, len(timestamps))
	for idx, ts := range timestamps {
		stats := source[ts]
		if stats.Count <= 0 {
			continue
		}
		avg := float64(stats.TotalMs) / float64(stats.Count)
		values[idx] = &avg
		counts[idx] = stats.Count
	}
	return AggregatedUsageLatencySeries{
		Timestamps: timestamps,
		Values:     values,
		Counts:     counts,
	}
}

func (a *aggregatedUsageWindowAccumulator) buildBoundedCostBasisSeries(source map[string]map[time.Time]TokenStats, start time.Time, step time.Duration, count int) AggregatedUsageCostBasisSeries {
	timestamps := buildBoundedTimestamps(start, step, count)
	models := make(map[string]AggregatedUsageTokenSeries, len(source))
	for modelName, buckets := range source {
		series := AggregatedUsageTokenSeries{
			Input:     make([]int64, len(timestamps)),
			Output:    make([]int64, len(timestamps)),
			Cached:    make([]int64, len(timestamps)),
			Reasoning: make([]int64, len(timestamps)),
			Total:     make([]int64, len(timestamps)),
		}
		for idx, ts := range timestamps {
			tokens := buckets[ts]
			series.Input[idx] = tokens.InputTokens
			series.Output[idx] = tokens.OutputTokens
			series.Cached[idx] = tokens.CachedTokens
			series.Reasoning[idx] = tokens.ReasoningTokens
			series.Total[idx] = tokens.TotalTokens
		}
		models[modelName] = series
	}
	return AggregatedUsageCostBasisSeries{
		Timestamps: timestamps,
		Models:     models,
	}
}

func (a *aggregatedUsageWindowAccumulator) buildDayCostBasisSeries(source map[string]map[time.Time]TokenStats) AggregatedUsageCostBasisSeries {
	timestamps := a.buildDayTimestamps()
	models := make(map[string]AggregatedUsageTokenSeries, len(source))
	for modelName, buckets := range source {
		series := AggregatedUsageTokenSeries{
			Input:     make([]int64, len(timestamps)),
			Output:    make([]int64, len(timestamps)),
			Cached:    make([]int64, len(timestamps)),
			Reasoning: make([]int64, len(timestamps)),
			Total:     make([]int64, len(timestamps)),
		}
		for idx, ts := range timestamps {
			tokens := buckets[ts]
			series.Input[idx] = tokens.InputTokens
			series.Output[idx] = tokens.OutputTokens
			series.Cached[idx] = tokens.CachedTokens
			series.Reasoning[idx] = tokens.ReasoningTokens
			series.Total[idx] = tokens.TotalTokens
		}
		models[modelName] = series
	}
	return AggregatedUsageCostBasisSeries{
		Timestamps: timestamps,
		Models:     models,
	}
}

func (a *aggregatedUsageWindowAccumulator) buildDayTimestamps() []time.Time {
	if a == nil {
		return nil
	}
	if a.dynamicDayRange {
		if !a.hasDayData {
			return nil
		}
	}
	start := a.dayStart
	end := a.dayEnd
	if a.dynamicDayRange && a.hasDayData {
		start = a.minDayBucket
		end = a.maxDayBucket
	}
	if end.Before(start) {
		return nil
	}
	count := int(end.Sub(start)/(24*time.Hour)) + 1
	return buildBoundedTimestamps(start, 24*time.Hour, count)
}

func (a *aggregatedUsageWindowAccumulator) buildAPIStats() []AggregatedUsageAPIStats {
	result := make([]AggregatedUsageAPIStats, 0, len(a.apiStats))
	for _, apiAcc := range a.apiStats {
		if apiAcc == nil {
			continue
		}
		models := make(map[string]AggregatedUsageAPIModelStats, len(apiAcc.models))
		for modelName, modelAcc := range apiAcc.models {
			if modelAcc == nil {
				continue
			}
			models[modelName] = AggregatedUsageAPIModelStats{
				Requests:       modelAcc.requests,
				SuccessCount:   modelAcc.successCount,
				FailureCount:   modelAcc.failureCount,
				Tokens:         modelAcc.tokens,
				TokenBreakdown: modelAcc.tokenBreakdown,
				Latency:        modelAcc.latency,
			}
		}
		result = append(result, AggregatedUsageAPIStats{
			Endpoint:       apiAcc.endpoint,
			TotalRequests:  apiAcc.totalRequests,
			SuccessCount:   apiAcc.successCount,
			FailureCount:   apiAcc.failureCount,
			TotalTokens:    apiAcc.totalTokens,
			TokenBreakdown: apiAcc.tokenBreakdown,
			Latency:        apiAcc.latency,
			Models:         models,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TotalRequests == result[j].TotalRequests {
			return result[i].Endpoint < result[j].Endpoint
		}
		return result[i].TotalRequests > result[j].TotalRequests
	})
	return result
}

func (a *aggregatedUsageWindowAccumulator) buildModelStats() []AggregatedUsageModelStats {
	result := make([]AggregatedUsageModelStats, 0, len(a.modelStats))
	for modelName, modelAcc := range a.modelStats {
		if modelAcc == nil {
			continue
		}
		result = append(result, AggregatedUsageModelStats{
			Model:          modelName,
			Requests:       modelAcc.requests,
			SuccessCount:   modelAcc.successCount,
			FailureCount:   modelAcc.failureCount,
			Tokens:         modelAcc.tokens,
			TokenBreakdown: modelAcc.tokenBreakdown,
			Latency:        modelAcc.latency,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Requests == result[j].Requests {
			return result[i].Model < result[j].Model
		}
		return result[i].Requests > result[j].Requests
	})
	return result
}

func (a *aggregatedUsageWindowAccumulator) buildCredentialStats() []AggregatedUsageCredentialStats {
	result := make([]AggregatedUsageCredentialStats, 0, len(a.credentials))
	for _, credAcc := range a.credentials {
		if credAcc == nil {
			continue
		}
		result = append(result, AggregatedUsageCredentialStats{
			Source:        credAcc.source,
			AuthIndex:     credAcc.authIndex,
			TotalRequests: credAcc.totalRequests,
			SuccessCount:  credAcc.successCount,
			FailureCount:  credAcc.failureCount,
			TotalTokens:   credAcc.totalTokens,
		})
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

func buildBoundedTimestamps(start time.Time, step time.Duration, count int) []time.Time {
	if count <= 0 {
		return nil
	}
	timestamps := make([]time.Time, count)
	for idx := range timestamps {
		timestamps[idx] = start.Add(time.Duration(idx) * step)
	}
	return timestamps
}

func truncateToHourUTC(ts time.Time) time.Time {
	return ts.UTC().Truncate(time.Hour)
}

func truncateToMinuteUTC(ts time.Time) time.Time {
	return ts.UTC().Truncate(time.Minute)
}

func truncateToDayUTC(ts time.Time) time.Time {
	utc := ts.UTC()
	year, month, day := utc.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func truncateToBucketUTC(ts time.Time, bucket time.Duration) time.Time {
	if bucket <= 0 {
		return truncateToHourUTC(ts)
	}
	return ts.UTC().Truncate(bucket)
}

func resolveAnalysisBucket(hourWindowHours int) time.Duration {
	switch {
	case hourWindowHours <= 3:
		return 5 * time.Minute
	case hourWindowHours <= 6:
		return 10 * time.Minute
	case hourWindowHours <= 12:
		return 15 * time.Minute
	case hourWindowHours <= 24:
		return 30 * time.Minute
	default:
		return time.Hour
	}
}

func sortedStringKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
