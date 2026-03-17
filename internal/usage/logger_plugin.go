// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
	"context"
	"fmt"
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

var persistHookMu sync.RWMutex
var persistHook func()

// SetPersistHook registers a hook for triggering persistence after usage updates.
func SetPersistHook(hook func()) {
	persistHookMu.Lock()
	persistHook = hook
	persistHookMu.Unlock()
}

// TriggerPersistHook invokes the registered persistence hook if present.
func TriggerPersistHook() {
	persistHookMu.RLock()
	hook := persistHook
	persistHookMu.RUnlock()
	if hook != nil {
		hook()
	}
}

// RequestStatistics maintains aggregated request metrics in memory.
type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests        int64
	successCount         int64
	failureCount         int64
	totalTokens          int64
	detailTotal          int64
	detailRingSize       int
	detailMaxTotal       int
	persistEveryRequests int64
	persistCounter       int64

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
	Details       *RingBuffer
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

const (
	defaultDetailRingSize = 10000
	defaultDetailMaxTotal = 50000
)

var defaultRequestStatistics = NewRequestStatistics()

// GetRequestStatistics returns the shared statistics store.
func GetRequestStatistics() *RequestStatistics { return defaultRequestStatistics }

// GetSnapshot returns a snapshot from the shared statistics store.
func GetSnapshot() StatisticsSnapshot {
	if defaultRequestStatistics == nil {
		return StatisticsSnapshot{}
	}
	return defaultRequestStatistics.Snapshot()
}

// ApplyDetailLimits updates the detail limits for the shared statistics store.
func ApplyDetailLimits(ringSize, maxTotal int) {
	if defaultRequestStatistics == nil {
		return
	}
	defaultRequestStatistics.SetDetailLimits(ringSize, maxTotal)
}

// SetPersistEveryRequests configures how often to trigger persistence.
func SetPersistEveryRequests(count int) {
	if defaultRequestStatistics == nil {
		return
	}
	defaultRequestStatistics.SetPersistEveryRequests(count)
}

// NewRequestStatistics constructs an empty statistics store.
func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
		detailRingSize: defaultDetailRingSize,
		detailMaxTotal: defaultDetailMaxTotal,
	}
}

// SetDetailLimits updates detail ring buffer limits and trims existing data as needed.
func (s *RequestStatistics) SetDetailLimits(ringSize, maxTotal int) {
	if s == nil {
		return
	}
	if ringSize <= 0 {
		ringSize = defaultDetailRingSize
	}
	if maxTotal <= 0 {
		maxTotal = defaultDetailMaxTotal
	}
	if maxTotal < ringSize {
		maxTotal = ringSize
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.detailRingSize = ringSize
	s.detailMaxTotal = maxTotal

	var total int64
	for _, api := range s.apis {
		if api == nil {
			continue
		}
		if api.Models == nil {
			api.Models = make(map[string]*modelStats)
		}
		for _, model := range api.Models {
			if model == nil {
				continue
			}
			if model.Details == nil {
				model.Details = NewRingBuffer(ringSize)
			} else {
				model.Details.Resize(ringSize)
			}
			total += int64(model.Details.Len())
		}
	}
	s.detailTotal = total
	if s.detailMaxTotal > 0 && s.detailTotal > int64(s.detailMaxTotal) {
		s.trimOldestDetails(int(s.detailTotal - int64(s.detailMaxTotal)))
	}
}

// SetPersistEveryRequests sets how often persistence is triggered by request count.
func (s *RequestStatistics) SetPersistEveryRequests(count int) {
	if s == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	s.mu.Lock()
	s.persistEveryRequests = int64(count)
	s.persistCounter = 0
	s.mu.Unlock()
}

// RestoreSnapshot replaces the current statistics with the provided snapshot.
// It restores totals directly rather than recalculating from request details.
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
	s.persistCounter = 0

	s.apis = make(map[string]*apiStats, len(snapshot.APIs))
	s.detailTotal = 0
	for apiName, apiSnapshot := range snapshot.APIs {
		stats := &apiStats{
			TotalRequests: apiSnapshot.TotalRequests,
			TotalTokens:   apiSnapshot.TotalTokens,
			Models:        make(map[string]*modelStats, len(apiSnapshot.Models)),
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelDetails := NewRingBuffer(s.detailRingSize)
			modelDetails.Load(modelSnapshot.Details)
			s.detailTotal += int64(modelDetails.Len())
			stats.Models[modelName] = &modelStats{
				TotalRequests: modelSnapshot.TotalRequests,
				TotalTokens:   modelSnapshot.TotalTokens,
				Details:       modelDetails,
			}
		}
		s.apis[apiName] = stats
	}

	s.requestsByDay = make(map[string]int64, len(snapshot.RequestsByDay))
	for k, v := range snapshot.RequestsByDay {
		s.requestsByDay[k] = v
	}
	s.requestsByHour = make(map[int]int64, len(snapshot.RequestsByHour))
	for k, v := range snapshot.RequestsByHour {
		if parsed, ok := parseHourKey(k); ok {
			s.requestsByHour[parsed] = v
		}
	}
	s.tokensByDay = make(map[string]int64, len(snapshot.TokensByDay))
	for k, v := range snapshot.TokensByDay {
		s.tokensByDay[k] = v
	}
	s.tokensByHour = make(map[int]int64, len(snapshot.TokensByHour))
	for k, v := range snapshot.TokensByHour {
		if parsed, ok := parseHourKey(k); ok {
			s.tokensByHour[parsed] = v
		}
	}

	if s.detailMaxTotal > 0 && s.detailTotal > int64(s.detailMaxTotal) {
		s.trimOldestDetails(int(s.detailTotal - int64(s.detailMaxTotal)))
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
	triggerPersist := false
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

	if s.persistEveryRequests > 0 {
		s.persistCounter++
		if s.persistCounter >= s.persistEveryRequests {
			s.persistCounter = 0
			triggerPersist = true
		}
	}

	s.mu.Unlock()
	if triggerPersist {
		TriggerPersistHook()
	}
}

func (s *RequestStatistics) updateAPIStats(stats *apiStats, model string, detail RequestDetail) {
	stats.TotalRequests++
	stats.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue, ok := stats.Models[model]
	if !ok {
		modelStatsValue = &modelStats{Details: NewRingBuffer(s.detailRingSize)}
		stats.Models[model] = modelStatsValue
	}
	if modelStatsValue.Details == nil {
		modelStatsValue.Details = NewRingBuffer(s.detailRingSize)
	}
	modelStatsValue.TotalRequests++
	modelStatsValue.TotalTokens += detail.Tokens.TotalTokens
	before := modelStatsValue.Details.Len()
	modelStatsValue.Details.Push(detail)
	after := modelStatsValue.Details.Len()
	if before != after {
		s.detailTotal += int64(after - before)
	}
	if s.detailMaxTotal > 0 && s.detailTotal > int64(s.detailMaxTotal) {
		s.trimOldestDetails(int(s.detailTotal - int64(s.detailMaxTotal)))
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
			var requestDetails []RequestDetail
			if modelStatsValue.Details != nil {
				requestDetails = modelStatsValue.Details.Snapshot()
			}
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
			if modelStatsValue.Details == nil {
				continue
			}
			for _, detail := range modelStatsValue.Details.Snapshot() {
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

	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func dedupKey(apiName, modelName string, detail RequestDetail) string {
	timestamp := detail.Timestamp.UTC().Format(time.RFC3339Nano)
	tokens := normaliseTokenStats(detail.Tokens)
	return fmt.Sprintf(
		"%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d",
		apiName,
		modelName,
		timestamp,
		detail.Source,
		detail.AuthIndex,
		detail.Failed,
		tokens.InputTokens,
		tokens.OutputTokens,
		tokens.ReasoningTokens,
		tokens.CachedTokens,
		tokens.TotalTokens,
	)
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

func (s *RequestStatistics) trimOldestDetails(count int) {
	if s == nil || count <= 0 {
		return
	}
	for count > 0 {
		var (
			oldestDetail RequestDetail
			oldestModel  *modelStats
			found        bool
		)
		for _, api := range s.apis {
			if api == nil {
				continue
			}
			for _, model := range api.Models {
				if model == nil || model.Details == nil || model.Details.Len() == 0 {
					continue
				}
				detail, ok := model.Details.Oldest()
				if !ok {
					continue
				}
				if !found || detail.Timestamp.Before(oldestDetail.Timestamp) {
					oldestDetail = detail
					oldestModel = model
					found = true
				}
			}
		}
		if !found || oldestModel == nil || oldestModel.Details == nil {
			return
		}
		if _, ok := oldestModel.Details.PopOldest(); ok {
			s.detailTotal--
			count--
			continue
		}
		return
	}
}

func formatHour(hour int) string {
	if hour < 0 {
		hour = 0
	}
	hour = hour % 24
	return fmt.Sprintf("%02d", hour)
}

func parseHourKey(key string) (int, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, false
	}
	value, err := strconv.Atoi(key)
	if err != nil {
		return 0, false
	}
	if value < 0 {
		value = 0
	}
	return value % 24, true
}
