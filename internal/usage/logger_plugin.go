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
var detailRetentionLimit atomic.Int64

const aggregateRecordRetentionWindow = 7 * 24 * time.Hour

func init() {
	statisticsEnabled.Store(true)
	detailRetentionLimit.Store(0)
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

// SetDetailRetentionLimit configures how many detailed records are retained per model.
// A limit <= 0 keeps all details for backward compatibility.
func SetDetailRetentionLimit(limit int) {
	if limit <= 0 {
		detailRetentionLimit.Store(0)
		return
	}
	detailRetentionLimit.Store(int64(limit))
}

// DetailRetentionLimit reports the configured per-model detailed record limit.
func DetailRetentionLimit() int {
	limit := detailRetentionLimit.Load()
	if limit <= 0 {
		return 0
	}
	return int(limit)
}

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

	aggregateRecords        []usageAggregateRecord
	oldestAggregateRecordAt time.Time
	newestAggregateRecordAt time.Time
	rolledUpAggregated      *AggregatedUsageSnapshot
	importedSummary         *StatisticsSnapshot
	importedAggregated      *AggregatedUsageSnapshot
	importedSummaryHashes   map[string]struct{}
	importedAggregateHashes map[string]struct{}
	importedSummarySources  map[string]StatisticsSnapshot
	importedDetailedSources map[string]StatisticsSnapshot
	importedAggregateSource map[string]AggregatedUsageSnapshot
}

// apiStats holds aggregated metrics for a single API key.
type apiStats struct {
	TotalRequests int64
	TotalTokens   int64
	Models        map[string]*modelStats
}

// modelStats holds aggregated metrics for a specific model within an API.
type modelStats struct {
	TotalRequests  int64
	TotalTokens    int64
	TokenBreakdown TokenStats
	Latency        LatencyStats
	Details        []RequestDetail
}

type usageAggregateRecord struct {
	APIName   string
	ModelName string
	Detail    RequestDetail
}

// RequestDetail stores the timestamp, latency, and token usage for a single request.
type RequestDetail struct {
	Timestamp            time.Time  `json:"timestamp"`
	LatencyMs            int64      `json:"latency_ms"`
	Source               string     `json:"source"`
	AuthIndex            string     `json:"auth_index"`
	ModelReasoningEffort string     `json:"model_reasoning_effort,omitempty"`
	Tokens               TokenStats `json:"tokens"`
	Failed               bool       `json:"failed"`
}

// TokenStats captures the token usage breakdown for a request.
type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

// LatencyStats captures aggregated latency information for a model.
type LatencyStats struct {
	Count   int64 `json:"count"`
	TotalMs int64 `json:"total_ms"`
	MinMs   int64 `json:"min_ms"`
	MaxMs   int64 `json:"max_ms"`
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
	TotalRequests  int64           `json:"total_requests"`
	TotalTokens    int64           `json:"total_tokens"`
	TokenBreakdown TokenStats      `json:"token_breakdown"`
	Latency        LatencyStats    `json:"latency"`
	Details        []RequestDetail `json:"details,omitempty"`
}

var defaultRequestStatistics = NewRequestStatistics()

// GetRequestStatistics returns the shared statistics store.
func GetRequestStatistics() *RequestStatistics { return defaultRequestStatistics }

// NewRequestStatistics constructs an empty statistics store.
func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis:                    make(map[string]*apiStats),
		requestsByDay:           make(map[string]int64),
		requestsByHour:          make(map[int]int64),
		tokensByDay:             make(map[string]int64),
		tokensByHour:            make(map[int]int64),
		importedSummaryHashes:   make(map[string]struct{}),
		importedAggregateHashes: make(map[string]struct{}),
		importedSummarySources:  make(map[string]StatisticsSnapshot),
		importedDetailedSources: make(map[string]StatisticsSnapshot),
		importedAggregateSource: make(map[string]AggregatedUsageSnapshot),
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
	requestDetail := RequestDetail{
		Timestamp:            timestamp,
		LatencyMs:            normaliseLatency(record.Latency),
		Source:               record.Source,
		AuthIndex:            record.AuthIndex,
		ModelReasoningEffort: strings.TrimSpace(record.ModelReasoningEffort),
		Tokens:               detail,
		Failed:               failed,
	}
	s.updateAPIStats(stats, modelName, requestDetail)
	s.appendAggregateRecord(statsKey, modelName, requestDetail)

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func (s *RequestStatistics) updateAPIStats(stats *apiStats, model string, detail RequestDetail) {
	detail.Tokens = normaliseTokenStats(detail.Tokens)

	stats.TotalRequests++
	stats.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue, ok := stats.Models[model]
	if !ok {
		modelStatsValue = &modelStats{}
		stats.Models[model] = modelStatsValue
	}
	modelStatsValue.TotalRequests++
	modelStatsValue.TotalTokens += detail.Tokens.TotalTokens
	mergeTokenStats(&modelStatsValue.TokenBreakdown, detail.Tokens)
	addLatencySample(&modelStatsValue.Latency, detail.LatencyMs)
	modelStatsValue.Details = append(modelStatsValue.Details, detail)
	trimRequestDetails(&modelStatsValue.Details, DetailRetentionLimit())
}

func (s *RequestStatistics) appendAggregateRecord(apiName, modelName string, detail RequestDetail) {
	if s == nil {
		return
	}
	apiName = strings.TrimSpace(apiName)
	if apiName == "" {
		apiName = "unknown"
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		modelName = "unknown"
	}
	detail.Tokens = normaliseTokenStats(detail.Tokens)
	recordTime := detail.Timestamp.UTC()
	s.aggregateRecords = append(s.aggregateRecords, usageAggregateRecord{
		APIName:   apiName,
		ModelName: modelName,
		Detail:    detail,
	})
	if !recordTime.IsZero() && (s.oldestAggregateRecordAt.IsZero() || recordTime.Before(s.oldestAggregateRecordAt)) {
		s.oldestAggregateRecordAt = recordTime
	}
	s.pruneAggregateRecordsLocked(recordTime)
}

func (s *RequestStatistics) pruneAggregateRecordsLocked(reference time.Time) {
	if s == nil || aggregateRecordRetentionWindow <= 0 || len(s.aggregateRecords) == 0 {
		return
	}
	reference = reference.UTC()
	if reference.IsZero() {
		reference = time.Now().UTC()
	}
	if s.newestAggregateRecordAt.IsZero() || reference.After(s.newestAggregateRecordAt) {
		s.newestAggregateRecordAt = reference
	}
	cutoff := s.newestAggregateRecordAt.Add(-aggregateRecordRetentionWindow)
	if !s.oldestAggregateRecordAt.IsZero() && !s.oldestAggregateRecordAt.Before(cutoff) {
		return
	}
	retained := s.aggregateRecords[:0]
	oldestRetained := time.Time{}
	var expired []usageAggregateRecord
	for _, record := range s.aggregateRecords {
		recordTime := record.Detail.Timestamp.UTC()
		if recordTime.IsZero() || recordTime.Before(cutoff) {
			expired = append(expired, record)
			continue
		}
		if oldestRetained.IsZero() || recordTime.Before(oldestRetained) {
			oldestRetained = recordTime
		}
		retained = append(retained, record)
	}
	s.oldestAggregateRecordAt = oldestRetained
	if len(expired) == 0 {
		return
	}
	clear(s.aggregateRecords[len(retained):])
	s.aggregateRecords = retained

	rolledUp := aggregateRecordsToAllSnapshot(expired, s.newestAggregateRecordAt)
	if len(rolledUp.Windows) == 0 {
		return
	}
	if s.rolledUpAggregated == nil {
		s.rolledUpAggregated = &rolledUp
		return
	}
	merged := mergeAggregatedUsageSnapshot(*s.rolledUpAggregated, rolledUp)
	s.rolledUpAggregated = &merged
}

func trimRequestDetails(details *[]RequestDetail, limit int) {
	if details == nil || limit <= 0 || len(*details) <= limit {
		return
	}
	excess := len(*details) - limit
	copy((*details)[0:], (*details)[excess:])
	clear((*details)[limit:])
	*details = (*details)[:limit]
}

// Snapshot returns a copy of the aggregated metrics for external consumption.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	return s.snapshotWithDetails(true)
}

// SnapshotSummary returns a copy of the aggregated metrics without per-request details.
func (s *RequestStatistics) SnapshotSummary() StatisticsSnapshot {
	return s.snapshotWithDetails(false)
}

func (s *RequestStatistics) snapshotWithDetails(includeDetails bool) StatisticsSnapshot {
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
			modelSnapshot := ModelSnapshot{
				TotalRequests:  modelStatsValue.TotalRequests,
				TotalTokens:    modelStatsValue.TotalTokens,
				TokenBreakdown: modelStatsValue.TokenBreakdown,
				Latency:        modelStatsValue.Latency,
			}
			if includeDetails && len(modelStatsValue.Details) > 0 {
				requestDetails := make([]RequestDetail, len(modelStatsValue.Details))
				copy(requestDetails, modelStatsValue.Details)
				modelSnapshot.Details = requestDetails
			}
			apiSnapshot.Models[modelName] = modelSnapshot
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

	if s.importedSummary != nil {
		result = mergeStatisticsSnapshots(result, *s.importedSummary)
	}
	for _, imported := range s.importedSummarySources {
		result = mergeStatisticsSnapshots(result, imported)
	}
	for _, imported := range s.importedDetailedSources {
		if includeDetails {
			result = mergeStatisticsSnapshots(result, imported)
			continue
		}
		result = mergeStatisticsSnapshots(result, canonicalSummarySnapshotForImport(imported))
	}

	return result
}

type MergeResult struct {
	Added    int64 `json:"added"`
	Skipped  int64 `json:"skipped"`
	Replaced int64 `json:"replaced,omitempty"`
}

// MergeSnapshot merges an exported statistics snapshot into the current store.
// Existing data is preserved and duplicate request details are skipped.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}
	if !snapshotContainsDetails(snapshot) {
		return s.mergeSummarySnapshot(snapshot)
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
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}
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

func (s *RequestStatistics) MergeImportedAggregatedSnapshot(snapshot AggregatedUsageSnapshot) {
	if s == nil || len(snapshot.Windows) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.importedAggregateHashes == nil {
		s.importedAggregateHashes = make(map[string]struct{})
	}

	fingerprint := fingerprintImportedAggregatedSnapshot(snapshot)
	if fingerprint != "" {
		if _, exists := s.importedAggregateHashes[fingerprint]; exists {
			return
		}
	}

	cloned := filterImportedAggregatedUsageSnapshot(snapshot)
	if s.importedAggregated == nil {
		s.importedAggregated = &cloned
		if fingerprint != "" {
			s.importedAggregateHashes[fingerprint] = struct{}{}
		}
		return
	}

	merged := mergeAggregatedUsageSnapshot(*s.importedAggregated, cloned)
	s.importedAggregated = &merged
	if fingerprint != "" {
		s.importedAggregateHashes[fingerprint] = struct{}{}
	}
}

func (s *RequestStatistics) UpsertImportedSummarySnapshot(sourceID string, snapshot StatisticsSnapshot) MergeResult {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return s.mergeSummarySnapshot(snapshot)
	}

	result := MergeResult{}
	if s == nil {
		return result
	}

	canonical := canonicalSummarySnapshotForImport(snapshot)
	importedTotalRequests := canonical.TotalRequests
	if importedTotalRequests == 0 {
		for _, apiSnapshot := range canonical.APIs {
			importedTotalRequests += apiSnapshot.TotalRequests
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.importedSummarySources == nil {
		s.importedSummarySources = make(map[string]StatisticsSnapshot)
	}

	fingerprint := fingerprintSummarySnapshot(canonical)
	if previous, exists := s.importedSummarySources[sourceID]; exists {
		if fingerprint != "" && fingerprintSummarySnapshot(previous) == fingerprint {
			result.Skipped = importedTotalRequests
			return result
		}
		result.Replaced = previous.TotalRequests
	}

	s.importedSummarySources[sourceID] = canonical
	result.Added = importedTotalRequests
	return result
}

func (s *RequestStatistics) UpsertImportedDetailedSnapshot(sourceID string, snapshot StatisticsSnapshot) MergeResult {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return s.MergeSnapshot(snapshot)
	}

	result := MergeResult{}
	if s == nil {
		return result
	}

	canonical := canonicalDetailedSnapshotForImport(snapshot)
	importedTotalRequests := canonical.TotalRequests
	if importedTotalRequests == 0 {
		for _, apiSnapshot := range canonical.APIs {
			importedTotalRequests += apiSnapshot.TotalRequests
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.importedDetailedSources == nil {
		s.importedDetailedSources = make(map[string]StatisticsSnapshot)
	}

	fingerprint := fingerprintDetailedSnapshot(canonical)
	if previous, exists := s.importedDetailedSources[sourceID]; exists {
		if fingerprint != "" && fingerprintDetailedSnapshot(previous) == fingerprint {
			result.Skipped = importedTotalRequests
			return result
		}
		result.Replaced = previous.TotalRequests
	}

	s.importedDetailedSources[sourceID] = canonical
	result.Added = importedTotalRequests
	return result
}

func (s *RequestStatistics) UpsertImportedAggregatedSnapshot(sourceID string, snapshot AggregatedUsageSnapshot) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		s.MergeImportedAggregatedSnapshot(snapshot)
		return
	}
	if s == nil || len(snapshot.Windows) == 0 {
		return
	}

	filtered := filterImportedAggregatedUsageSnapshot(snapshot)
	if len(filtered.Windows) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.importedAggregateSource == nil {
		s.importedAggregateSource = make(map[string]AggregatedUsageSnapshot)
	}

	fingerprint := fingerprintImportedAggregatedSnapshot(filtered)
	if previous, exists := s.importedAggregateSource[sourceID]; exists {
		if fingerprint != "" && fingerprintImportedAggregatedSnapshot(previous) == fingerprint {
			return
		}
	}
	s.importedAggregateSource[sourceID] = filtered
}

func snapshotContainsDetails(snapshot StatisticsSnapshot) bool {
	for _, apiSnapshot := range snapshot.APIs {
		for _, modelSnapshot := range apiSnapshot.Models {
			if len(modelSnapshot.Details) > 0 {
				return true
			}
		}
	}
	return false
}

func (s *RequestStatistics) mergeSummarySnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	s.mu.Lock()
	defer s.mu.Unlock()

	canonical := canonicalSummarySnapshotForImport(snapshot)
	importedTotalRequests := canonical.TotalRequests
	if importedTotalRequests == 0 {
		for _, apiSnapshot := range canonical.APIs {
			importedTotalRequests += apiSnapshot.TotalRequests
		}
	}

	if s.importedSummaryHashes == nil {
		s.importedSummaryHashes = make(map[string]struct{})
	}

	fingerprint := fingerprintSummarySnapshot(canonical)
	if fingerprint != "" {
		if _, exists := s.importedSummaryHashes[fingerprint]; exists {
			result.Skipped = importedTotalRequests
			return result
		}
	}

	if s.importedSummary == nil {
		cloned := cloneStatisticsSnapshot(canonical)
		s.importedSummary = &cloned
	} else {
		merged := mergeStatisticsSnapshots(*s.importedSummary, canonical)
		s.importedSummary = &merged
	}

	result.Added = importedTotalRequests

	if fingerprint != "" {
		s.importedSummaryHashes[fingerprint] = struct{}{}
	}

	return result
}

func parseSnapshotHour(hour string) (int, bool) {
	parsed, err := strconv.Atoi(strings.TrimSpace(hour))
	if err != nil {
		return 0, false
	}
	if parsed < 0 {
		parsed = 0
	}
	return parsed % 24, true
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
	s.appendAggregateRecord(apiName, modelName, detail)

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
		"%s|%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d",
		apiName,
		modelName,
		timestamp,
		detail.Source,
		detail.AuthIndex,
		strings.TrimSpace(detail.ModelReasoningEffort),
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

func mergeTokenStats(dst *TokenStats, src TokenStats) {
	if dst == nil {
		return
	}
	src = normaliseTokenStats(src)
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.ReasoningTokens += src.ReasoningTokens
	dst.CachedTokens += src.CachedTokens
	dst.TotalTokens += src.TotalTokens
}

func addLatencySample(dst *LatencyStats, latencyMs int64) {
	if dst == nil || latencyMs <= 0 {
		return
	}
	if dst.Count == 0 {
		dst.MinMs = latencyMs
		dst.MaxMs = latencyMs
	} else {
		if dst.MinMs == 0 || latencyMs < dst.MinMs {
			dst.MinMs = latencyMs
		}
		if latencyMs > dst.MaxMs {
			dst.MaxMs = latencyMs
		}
	}
	dst.Count++
	dst.TotalMs += latencyMs
}

func mergeLatencyStats(dst *LatencyStats, src LatencyStats) {
	if dst == nil || src.Count <= 0 {
		return
	}
	if dst.Count == 0 {
		dst.MinMs = src.MinMs
		dst.MaxMs = src.MaxMs
	} else {
		if dst.MinMs == 0 || (src.MinMs > 0 && src.MinMs < dst.MinMs) {
			dst.MinMs = src.MinMs
		}
		if src.MaxMs > dst.MaxMs {
			dst.MaxMs = src.MaxMs
		}
	}
	dst.Count += src.Count
	dst.TotalMs += src.TotalMs
}

func normaliseLatency(latency time.Duration) int64 {
	if latency <= 0 {
		return 0
	}
	return latency.Milliseconds()
}

func formatHour(hour int) string {
	if hour < 0 {
		hour = 0
	}
	hour = hour % 24
	return fmt.Sprintf("%02d", hour)
}
