// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
    "context"
    "fmt"
    "math"
    "sort"
    "sync"
    "sync/atomic"
    "time"

    "github.com/gin-gonic/gin"
    coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

var statisticsEnabled atomic.Bool

func init() {
	statisticsEnabled.Store(true)
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

// LoggerPlugin collects in-memory request statistics for usage analysis.
// It implements coreusage.Plugin to receive usage records emitted by the runtime.
type LoggerPlugin struct {
	stats *RequestStatistics
}

// NewLoggerPlugin constructs a new logger plugin instance.
//
// Returns:
//   - *LoggerPlugin: A new logger plugin instance wired to the shared statistics store.
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{stats: defaultRequestStatistics} }

// HandleUsage implements coreusage.Plugin.
// It updates the in-memory statistics store whenever a usage record is received.
//
// Parameters:
//   - ctx: The context for the usage record
//   - record: The usage record to aggregate
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if !statisticsEnabled.Load() {
		return
	}
	if p == nil || p.stats == nil {
		return
	}
	p.stats.Record(ctx, record)
}

// SetStatisticsEnabled toggles whether in-memory statistics are recorded.
func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

// StatisticsEnabled reports the current recording state.
func StatisticsEnabled() bool { return statisticsEnabled.Load() }

// RequestStatistics maintains aggregated request metrics in memory.
type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64

	apis map[string]*apiStats

	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64
}

// apiStats holds aggregated metrics for a single API key.
type apiStats struct {
	TotalRequests int64
	TotalTokens   int64
	Models        map[string]*modelStats
}

// modelStats holds aggregated metrics for a specific model within an API.
type modelStats struct {
	TotalRequests int64
	TotalTokens   int64
	Details       []RequestDetail
}

// RequestDetail stores the timestamp and token usage for a single request.
type RequestDetail struct {
	Timestamp time.Time  `json:"timestamp"`
	Source    string     `json:"source"`
	Tokens    TokenStats `json:"tokens"`
	Failed    bool       `json:"failed"`
}

// TokenStats captures the token usage breakdown for a request.
type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

// StatisticsSnapshot represents an immutable view of the aggregated metrics.
type StatisticsSnapshot struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`

	APIs map[string]APISnapshot `json:"apis"`

	RequestsByDay  map[string]int64 `json:"requests_by_day"`
	RequestsByHour map[string]int64 `json:"requests_by_hour"`
	TokensByDay    map[string]int64 `json:"tokens_by_day"`
	TokensByHour   map[string]int64 `json:"tokens_by_hour"`
}

// APISnapshot summarises metrics for a single API key.
type APISnapshot struct {
	TotalRequests int64                    `json:"total_requests"`
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot summarises metrics for a specific model.
type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
}

var defaultRequestStatistics = NewRequestStatistics()

// GetRequestStatistics returns the shared statistics store.
func GetRequestStatistics() *RequestStatistics { return defaultRequestStatistics }

// NewRequestStatistics constructs an empty statistics store.
func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
	}
}

// Record ingests a new usage record and updates the aggregates.
func (s *RequestStatistics) Record(ctx context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	if !statisticsEnabled.Load() {
		return
	}
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	detail := normaliseDetail(record.Detail)
	totalTokens := detail.TotalTokens
	statsKey := record.APIKey
	if statsKey == "" {
		statsKey = resolveAPIIdentifier(ctx, record)
	}
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	success := !failed
	modelName := record.Model
	if modelName == "" {
		modelName = "unknown"
	}
	dayKey := timestamp.Format("2006-01-02")
	hourKey := timestamp.Hour()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests++
	if success {
		s.successCount++
	} else {
		s.failureCount++
	}
	s.totalTokens += totalTokens

	stats, ok := s.apis[statsKey]
	if !ok {
		stats = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[statsKey] = stats
	}
	s.updateAPIStats(stats, modelName, RequestDetail{
		Timestamp: timestamp,
		Source:    record.Source,
		Tokens:    detail,
		Failed:    failed,
	})

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func (s *RequestStatistics) updateAPIStats(stats *apiStats, model string, detail RequestDetail) {
	stats.TotalRequests++
	stats.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue, ok := stats.Models[model]
	if !ok {
		modelStatsValue = &modelStats{}
		stats.Models[model] = modelStatsValue
	}
	modelStatsValue.TotalRequests++
	modelStatsValue.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue.Details = append(modelStatsValue.Details, detail)
}

// Snapshot returns a copy of the aggregated metrics for external consumption.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	result := StatisticsSnapshot{}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result.TotalRequests = s.totalRequests
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	result.TotalTokens = s.totalTokens

	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, stats := range s.apis {
		apiSnapshot := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelStatsValue := range stats.Models {
			requestDetails := make([]RequestDetail, len(modelStatsValue.Details))
			copy(requestDetails, modelStatsValue.Details)
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests: modelStatsValue.TotalRequests,
				TotalTokens:   modelStatsValue.TotalTokens,
				Details:       requestDetails,
			}
		}
		result.APIs[apiName] = apiSnapshot
	}

	result.RequestsByDay = make(map[string]int64, len(s.requestsByDay))
	for k, v := range s.requestsByDay {
		result.RequestsByDay[k] = v
	}

	result.RequestsByHour = make(map[string]int64, len(s.requestsByHour))
	for hour, v := range s.requestsByHour {
		key := formatHour(hour)
		result.RequestsByHour[key] = v
	}

	result.TokensByDay = make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		result.TokensByDay[k] = v
	}

	result.TokensByHour = make(map[string]int64, len(s.tokensByHour))
	for hour, v := range s.tokensByHour {
		key := formatHour(hour)
		result.TokensByHour[key] = v
	}

	return result
}

func resolveAPIIdentifier(ctx context.Context, record coreusage.Record) string {
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
			path := ginCtx.FullPath()
			if path == "" && ginCtx.Request != nil {
				path = ginCtx.Request.URL.Path
			}
			method := ""
			if ginCtx.Request != nil {
				method = ginCtx.Request.Method
			}
			if path != "" {
				if method != "" {
					return method + " " + path
				}
				return path
			}
		}
	}
	if record.Provider != "" {
		return record.Provider
	}
	return "unknown"
}

func resolveSuccess(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return true
	}
	status := ginCtx.Writer.Status()
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

const httpStatusBadRequest = 400

func normaliseDetail(detail coreusage.Detail) TokenStats {
	tokens := TokenStats{
		InputTokens:     detail.InputTokens,
		OutputTokens:    detail.OutputTokens,
		ReasoningTokens: detail.ReasoningTokens,
		CachedTokens:    detail.CachedTokens,
		TotalTokens:     detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens + detail.CachedTokens
	}
	return tokens
}

func formatHour(hour int) string {
    if hour < 0 {
        hour = 0
    }
    hour = hour % 24
    return fmt.Sprintf("%02d", hour)
}

// --- TPS Aggregation (since server start) ---

// serverStart records the approximate module init time, used as start boundary
// for TPS aggregation. This aligns closely with process start.
var serverStart = time.Now()

// TPSAggregateSnapshot presents summary statistics of observed TPS samples.
type TPSAggregateSnapshot struct {
    Since time.Time `json:"since"`
    Completion TPSSummary `json:"completion"`
    Total      TPSSummary `json:"total"`
}

// TPSSummary holds mean/median and count for a metric.
type TPSSummary struct {
    Count  int64   `json:"count"`
    Avg    float64 `json:"avg"`
    Median float64 `json:"median"`
}

type timedValue struct {
    ts time.Time
    v  float64
}

// taggedValue stores a sample value with provider/model attribution
type taggedValue struct {
    ts       time.Time
    v        float64
    provider string
    model    string
}

type tpsAggregator struct {
    mu             sync.RWMutex
    completion     []timedValue
    total          []timedValue
    completionSum  float64
    totalSum       float64

    // Optional tagged series for provider/model filtering
    completionTagged []taggedValue
    totalTagged      []taggedValue
}

var defaultTPSAggregator = &tpsAggregator{}

// Cleanup configuration (can be tuned if needed)
var (
    tpsMaxRetention    = 24 * time.Hour
    tpsCleanupInterval = 1 * time.Minute
    tpsMinSamples      = 10
    tpsCleanEveryN     = 1000
)

func init() {
    // start background cleanup goroutine
    go backgroundTPSCleanup()
}

// RecordTPSSample ingests one observation of completion/total TPS.
// Values can be zero; negative/NaN/Inf are ignored.
func RecordTPSSample(completion, total float64) {
    agg := defaultTPSAggregator
    if agg == nil {
        return
    }
    agg.mu.Lock()
    defer agg.mu.Unlock()
    if !finite(completion) && !finite(total) {
        return
    }
    now := time.Now()
    added := 0
    if finite(completion) {
        agg.completion = append(agg.completion, timedValue{ts: now, v: completion})
        agg.completionSum += completion
        added++
    }
    if finite(total) {
        agg.total = append(agg.total, timedValue{ts: now, v: total})
        agg.totalSum += total
        added++
    }
    if added > 0 {
        recordAndMaybeCleanLocked(agg)
    }
}

// RecordTPSSampleTagged ingests a TPS observation and associates it with provider/model.
// Also updates the untagged series to keep default aggregates intact.
func RecordTPSSampleTagged(provider, model string, completion, total float64) {
    agg := defaultTPSAggregator
    if agg == nil {
        return
    }
    now := time.Now()
    agg.mu.Lock()
    // First, append to untagged series (mirrors RecordTPSSample logic, but inline to avoid double locking)
    if finite(completion) {
        agg.completion = append(agg.completion, timedValue{ts: now, v: completion})
        agg.completionSum += completion
    }
    if finite(total) {
        agg.total = append(agg.total, timedValue{ts: now, v: total})
        agg.totalSum += total
    }
    // Then, append tagged entries for filtering
    if finite(completion) {
        agg.completionTagged = append(agg.completionTagged, taggedValue{ts: now, v: completion, provider: provider, model: model})
    }
    if finite(total) {
        agg.totalTagged = append(agg.totalTagged, taggedValue{ts: now, v: total, provider: provider, model: model})
    }
    recordAndMaybeCleanLocked(agg)
    agg.mu.Unlock()
}

func recordAndMaybeCleanLocked(agg *tpsAggregator) {
    // simple counter via slice lengths; when large enough, trigger a quick cleanup
    totalCount := len(agg.completion) + len(agg.total)
    if totalCount%tpsCleanEveryN == 0 {
        // unlock-lock pattern avoided; we already hold the lock
        cutoff := time.Now().Add(-tpsMaxRetention)
        pruneLocked(agg, cutoff)
    }
}

func backgroundTPSCleanup() {
    ticker := time.NewTicker(tpsCleanupInterval)
    defer ticker.Stop()
    for range ticker.C {
        agg := defaultTPSAggregator
        if agg == nil {
            continue
        }
        cutoff := time.Now().Add(-tpsMaxRetention)
        agg.mu.Lock()
        pruneLocked(agg, cutoff)
        agg.mu.Unlock()
    }
}

func pruneLocked(agg *tpsAggregator, cutoff time.Time) {
    // prune completion
    if n := len(agg.completion); n > tpsMinSamples {
        kept := agg.completion[:0]
        var sum float64
        for i := 0; i < n; i++ {
            tv := agg.completion[i]
            if tv.ts.Before(cutoff) {
                continue
            }
            kept = append(kept, tv)
            sum += tv.v
        }
        if len(kept) < tpsMinSamples && n > 0 {
            // ensure at least latest tpsMinSamples retained
            start := n - tpsMinSamples
            if start < 0 {
                start = 0
            }
            kept = append([]timedValue(nil), agg.completion[start:]...)
            sum = 0
            for i := range kept {
                sum += kept[i].v
            }
        }
        agg.completion = kept
        agg.completionSum = sum
    }
    // prune total
    if n := len(agg.total); n > tpsMinSamples {
        kept := agg.total[:0]
        var sum float64
        for i := 0; i < n; i++ {
            tv := agg.total[i]
            if tv.ts.Before(cutoff) {
                continue
            }
            kept = append(kept, tv)
            sum += tv.v
        }
        if len(kept) < tpsMinSamples && n > 0 {
            start := n - tpsMinSamples
            if start < 0 {
                start = 0
            }
            kept = append([]timedValue(nil), agg.total[start:]...)
            sum = 0
            for i := range kept {
                sum += kept[i].v
            }
        }
        agg.total = kept
        agg.totalSum = sum
    }
}

// GetTPSAggregates returns the current TPS aggregate snapshot.
func GetTPSAggregates() TPSAggregateSnapshot {
    agg := defaultTPSAggregator
    if agg == nil {
        return TPSAggregateSnapshot{Since: serverStart}
    }
    agg.mu.RLock()
    defer agg.mu.RUnlock()

    snap := TPSAggregateSnapshot{Since: serverStart}
    // completion
    if n := len(agg.completion); n > 0 {
        snap.Completion.Count = int64(n)
        snap.Completion.Avg = round2f(agg.completionSum / float64(n))
        snap.Completion.Median = medianOf(extractValues(agg.completion))
    }
    // total
    if n := len(agg.total); n > 0 {
        snap.Total.Count = int64(n)
        snap.Total.Avg = round2f(agg.totalSum / float64(n))
        snap.Total.Median = medianOf(extractValues(agg.total))
    }
    return snap
}

// GetTPSAggregatesWindow returns snapshot limited to the last 'window' duration.
// If window <= 0, this is equivalent to GetTPSAggregates().
func GetTPSAggregatesWindow(window time.Duration) TPSAggregateSnapshot {
    if window <= 0 {
        return GetTPSAggregates()
    }
    agg := defaultTPSAggregator
    snap := TPSAggregateSnapshot{Since: time.Now().Add(-window)}
    if agg == nil {
        return snap
    }
    cutoff := time.Now().Add(-window)
    agg.mu.RLock()
    // completion windowed
    {
        var n int
        var sum float64
        vals := make([]float64, 0)
        for i := range agg.completion {
            tv := agg.completion[i]
            if tv.ts.Before(cutoff) {
                continue
            }
            n++
            sum += tv.v
            vals = append(vals, tv.v)
        }
        if n > 0 {
            snap.Completion.Count = int64(n)
            snap.Completion.Avg = round2f(sum / float64(n))
            snap.Completion.Median = medianOf(vals)
        }
    }
    // total windowed
    {
        var n int
        var sum float64
        vals := make([]float64, 0)
        for i := range agg.total {
            tv := agg.total[i]
            if tv.ts.Before(cutoff) {
                continue
            }
            n++
            sum += tv.v
            vals = append(vals, tv.v)
        }
        if n > 0 {
            snap.Total.Count = int64(n)
            snap.Total.Avg = round2f(sum / float64(n))
            snap.Total.Median = medianOf(vals)
        }
    }
    agg.mu.RUnlock()
    return snap
}

// GetTPSAggregatesWindowFiltered returns windowed aggregates filtered by provider and/or model.
// If both provider and model are empty, it falls back to GetTPSAggregatesWindow.
func GetTPSAggregatesWindowFiltered(window time.Duration, provider, model string) TPSAggregateSnapshot {
    if provider == "" && model == "" {
        return GetTPSAggregatesWindow(window)
    }
    if window <= 0 {
        // treat as unbounded window
        window = time.Since(serverStart)
    }
    agg := defaultTPSAggregator
    snap := TPSAggregateSnapshot{Since: time.Now().Add(-window)}
    if agg == nil {
        return snap
    }
    cutoff := time.Now().Add(-window)
    agg.mu.RLock()
    // completion filtered
    {
        var n int
        var sum float64
        vals := make([]float64, 0)
        for i := range agg.completionTagged {
            tv := agg.completionTagged[i]
            if tv.ts.Before(cutoff) {
                continue
            }
            if provider != "" && tv.provider != provider {
                continue
            }
            if model != "" && tv.model != model {
                continue
            }
            n++
            sum += tv.v
            vals = append(vals, tv.v)
        }
        if n > 0 {
            snap.Completion.Count = int64(n)
            snap.Completion.Avg = round2f(sum / float64(n))
            snap.Completion.Median = medianOf(vals)
        }
    }
    // total filtered
    {
        var n int
        var sum float64
        vals := make([]float64, 0)
        for i := range agg.totalTagged {
            tv := agg.totalTagged[i]
            if tv.ts.Before(cutoff) {
                continue
            }
            if provider != "" && tv.provider != provider {
                continue
            }
            if model != "" && tv.model != model {
                continue
            }
            n++
            sum += tv.v
            vals = append(vals, tv.v)
        }
        if n > 0 {
            snap.Total.Count = int64(n)
            snap.Total.Avg = round2f(sum / float64(n))
            snap.Total.Median = medianOf(vals)
        }
    }
    agg.mu.RUnlock()
    return snap
}

// ServerStartTime exposes the module init time for external use.
func ServerStartTime() time.Time { return serverStart }

// helpers
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func medianOf(values []float64) float64 {
    if len(values) == 0 {
        return 0
    }
    // copy to avoid mutating underlying slice
    cp := make([]float64, len(values))
    copy(cp, values)
    sort.Float64s(cp)
    n := len(cp)
    if n%2 == 1 {
        return round2f(cp[n/2])
    }
    return round2f((cp[n/2-1] + cp[n/2]) / 2)
}

// round2f rounds to 2 decimal places with guards.
func round2f(v float64) float64 {
    if math.IsNaN(v) || math.IsInf(v, 0) {
        return 0
    }
    return math.Round(v*100) / 100
}

func extractValues(tvs []timedValue) []float64 {
    if len(tvs) == 0 {
        return nil
    }
    vals := make([]float64, len(tvs))
    for i := range tvs {
        vals[i] = tvs[i].v
    }
    return vals
}
