// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
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

	// cache for dayKey
	cachedDayKey   string
	cachedDayYear  int
	cachedDayMonth time.Month
	cachedDayDay   int
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
	AuthIndex string     `json:"auth_index"`
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

// getDayKey returns the day string formatted as YYYY-MM-DD.
// It caches the most recent calculation to avoid the CPU and allocation overhead 
// of calling time.Format("2006-01-02") on every single request record.
func (s *RequestStatistics) getDayKey(timestamp time.Time) string {
	y, m, d := timestamp.Date()
	if y == s.cachedDayYear && m == s.cachedDayMonth && d == s.cachedDayDay && s.cachedDayKey != "" {
		return s.cachedDayKey
	}
	s.cachedDayYear = y
	s.cachedDayMonth = m
	s.cachedDayDay = d
	s.cachedDayKey = timestamp.Format("2006-01-02")
	return s.cachedDayKey
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
	hourKey := timestamp.Hour()

	s.mu.Lock()
	defer s.mu.Unlock()

	dayKey := s.getDayKey(timestamp)

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
		AuthIndex: record.AuthIndex,
		Tokens:    detail,
		Failed:    failed,
	})

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

// updateAPIStats updates the model specific aggregates with a new request detail.
// It keeps the latest 1000 details per model to prevent memory leaks from unbounded growth
// in long-running processes or high-throughput scenarios, while still providing
// enough history for meaningful observability.
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
	const maxDetails = 1000
	if len(modelStatsValue.Details) > maxDetails {
		// When the slice exceeds its limit, shrink it back to 90% of maxDetails to avoid
		// trimming on every single new record, which would be inefficient.
		const shrinkToSize = maxDetails - (maxDetails / 10) // 900
		numToRemove := len(modelStatsValue.Details) - shrinkToSize

		// Use copy to shift elements to the front, preserving the backing array's capacity.
		// This is more efficient than re-slicing with `modelStatsValue.Details[numToRemove:]`.
		copy(modelStatsValue.Details, modelStatsValue.Details[numToRemove:])
		modelStatsValue.Details = modelStatsValue.Details[:shrinkToSize]
	}
}

// RestoreSnapshot completely overwrites the current memory state with the provided snapshot.
// It performs a direct state replacement rather than merging individual request details,
// which significantly improves performance when loading initial state from disk.
func (s *RequestStatistics) RestoreSnapshot(snapshot StatisticsSnapshot) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests = snapshot.TotalRequests
	s.successCount = snapshot.SuccessCount
	s.failureCount = snapshot.FailureCount
	s.totalTokens = snapshot.TotalTokens

	s.apis = make(map[string]*apiStats)
	for apiName, apiSnapshot := range snapshot.APIs {
		stats := &apiStats{
			TotalRequests: apiSnapshot.TotalRequests,
			TotalTokens:   apiSnapshot.TotalTokens,
			Models:        make(map[string]*modelStats),
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			details := make([]RequestDetail, len(modelSnapshot.Details))
			copy(details, modelSnapshot.Details)
			stats.Models[modelName] = &modelStats{
				TotalRequests: modelSnapshot.TotalRequests,
				TotalTokens:   modelSnapshot.TotalTokens,
				Details:       details,
			}
		}
		s.apis[apiName] = stats
	}

	s.requestsByDay = make(map[string]int64)
	for k, v := range snapshot.RequestsByDay {
		s.requestsByDay[k] = v
	}

	s.requestsByHour = make(map[int]int64)
	for k, v := range snapshot.RequestsByHour {
		// handle string to int conversion for the hour map key
		var hour int
		hour, _ = strconv.Atoi(k)
		s.requestsByHour[hour] = v
	}

	s.tokensByDay = make(map[string]int64)
	for k, v := range snapshot.TokensByDay {
		s.tokensByDay[k] = v
	}

	s.tokensByHour = make(map[int]int64)
	for k, v := range snapshot.TokensByHour {
		var hour int
		hour, _ = strconv.Atoi(k)
		s.tokensByHour[hour] = v
	}
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

// WriteJSON locks the statistics and streams them directly to the provided encoder,
// avoiding the memory allocations of a full Snapshot deep-copy.
func (s *RequestStatistics) WriteJSON(enc *json.Encoder) error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	// We construct a shallow StatisticsSnapshot instead of a deep copy since we hold the read lock.
	// This ensures we format the keys properly (e.g. padding hour strings) while avoiding 
	// unnecessary array allocations before JSON encoding.

	shallow := StatisticsSnapshot{
		TotalRequests: s.totalRequests,
		SuccessCount:  s.successCount,
		FailureCount:  s.failureCount,
		TotalTokens:   s.totalTokens,
		APIs:          make(map[string]APISnapshot, len(s.apis)),
		RequestsByDay: s.requestsByDay, // Maps are safe to read concurrently if we hold RLock
		TokensByDay:   s.tokensByDay,
	}

	for apiName, stats := range s.apis {
		apiShallow := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelStatsValue := range stats.Models {
			// SHALLOW COPY: Just pass the slice reference!
			apiShallow.Models[modelName] = ModelSnapshot{
				TotalRequests: modelStatsValue.TotalRequests,
				TotalTokens:   modelStatsValue.TotalTokens,
				Details:       modelStatsValue.Details,
			}
		}
		shallow.APIs[apiName] = apiShallow
	}

	shallow.RequestsByHour = make(map[string]int64, len(s.requestsByHour))
	for hour, v := range s.requestsByHour {
		shallow.RequestsByHour[formatHour(hour)] = v
	}

	shallow.TokensByHour = make(map[string]int64, len(s.tokensByHour))
	for hour, v := range s.tokensByHour {
		shallow.TokensByHour[formatHour(hour)] = v
	}

	return enc.Encode(shallow)
}

// GetTotalRequests returns the total number of requests without locking the whole struct if we just want to check.
func (s *RequestStatistics) GetTotalRequests() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totalRequests
}

type MergeResult struct {
	Added   int64 `json:"added"`
	Skipped int64 `json:"skipped"`
}

// MergeSnapshot merges an exported statistics snapshot into the current store.
// Existing data is preserved and duplicate request details are skipped.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]struct{})
	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			for _, detail := range modelStatsValue.Details {
				seen[dedupKey(apiName, modelName, detail)] = struct{}{}
			}
		}
	}

	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			continue
		}
		stats, ok := s.apis[apiName]
		if !ok || stats == nil {
			stats = &apiStats{Models: make(map[string]*modelStats)}
			s.apis[apiName] = stats
		} else if stats.Models == nil {
			stats.Models = make(map[string]*modelStats)
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = strings.TrimSpace(modelName)
			if modelName == "" {
				modelName = "unknown"
			}
			for _, detail := range modelSnapshot.Details {
				detail.Tokens = normaliseTokenStats(detail.Tokens)
				if detail.Timestamp.IsZero() {
					detail.Timestamp = time.Now()
				}
				key := dedupKey(apiName, modelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}
				s.recordImported(apiName, modelName, stats, detail)
				result.Added++
			}
		}
	}

	return result
}

func (s *RequestStatistics) recordImported(apiName, modelName string, stats *apiStats, detail RequestDetail) {
	totalTokens := detail.Tokens.TotalTokens
	if totalTokens < 0 {
		totalTokens = 0
	}

	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totalTokens

	s.updateAPIStats(stats, modelName, detail)

	dayKey := s.getDayKey(detail.Timestamp)
	hourKey := detail.Timestamp.Hour()

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

// dedupKey generates a unique string identifier for a request detail to prevent 
// processing duplicates during snapshot merges.
// It uses strings.Builder instead of fmt.Sprintf to significantly reduce memory 
// allocations and CPU overhead during high-volume usage tracking.
func dedupKey(apiName, modelName string, detail RequestDetail) string {
	timestamp := detail.Timestamp.UTC().Format(time.RFC3339Nano)
	tokens := normaliseTokenStats(detail.Tokens)

	var sb strings.Builder
	// Rough pre-allocation: lengths of strings + approx max int lengths
	sb.Grow(len(apiName) + len(modelName) + len(timestamp) + len(detail.Source) + len(detail.AuthIndex) + 100)

	sb.WriteString(apiName)
	sb.WriteByte('|')
	sb.WriteString(modelName)
	sb.WriteByte('|')
	sb.WriteString(timestamp)
	sb.WriteByte('|')
	sb.WriteString(detail.Source)
	sb.WriteByte('|')
	sb.WriteString(detail.AuthIndex)
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatBool(detail.Failed))
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatInt(tokens.InputTokens, 10))
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatInt(tokens.OutputTokens, 10))
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatInt(tokens.ReasoningTokens, 10))
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatInt(tokens.CachedTokens, 10))
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatInt(tokens.TotalTokens, 10))

	return sb.String()
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

func normaliseTokenStats(tokens TokenStats) TokenStats {
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}
	return tokens
}

var hourStrings = []string{
	"00", "01", "02", "03", "04", "05", "06", "07", "08", "09",
	"10", "11", "12", "13", "14", "15", "16", "17", "18", "19",
	"20", "21", "22", "23",
}

// formatHour returns the zero-padded string representation of an hour (0-23).
// It uses a pre-allocated array of strings to avoid the high allocation 
// cost of using fmt.Sprintf("%02d", hour) on the hot path of metrics aggregation.
func formatHour(hour int) string {
	if hour < 0 {
		hour = 0
	}
	hour = hour % 24
	return hourStrings[hour]
}
