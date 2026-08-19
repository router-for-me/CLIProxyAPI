// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

var statisticsEnabled atomic.Bool

const (
	maxTrackedAPIs            = 1024
	maxModelsPerAPI           = 256
	maxRequestDetailsPerModel = 1000
	maxUsageIdentifierRunes   = 160
	overflowUsageBucket       = "other"
	unknownUsageBucket        = "unknown"
)

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
	FailureCount  int64
	Models        map[string]*modelStats
}

// modelStats holds aggregated metrics for a specific model within an API.
type modelStats struct {
	TotalRequests int64
	TotalTokens   int64
	FailureCount  int64
	Details       []RequestDetail
}

// RequestDetail stores the timestamp, latency, and token usage for a single request.
type RequestDetail struct {
	Timestamp time.Time  `json:"timestamp"`
	LatencyMs int64      `json:"latency_ms"`
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
	FailureCount  int64                    `json:"failure_count"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot summarises metrics for a specific model.
type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	FailureCount  int64           `json:"failure_count"`
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
	statsKey := secretUsageBucket(record.APIKey)
	if statsKey == "" {
		statsKey = resolveAPIIdentifier(ctx, record)
	}
	statsKey = normaliseUsageIdentifier(statsKey)
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	success := !failed
	modelName := record.Model
	if modelName == "" {
		modelName = unknownUsageBucket
	}
	modelName = normaliseUsageIdentifier(modelName)
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

	stats := s.apiStatsForKey(statsKey)
	s.updateAPIStats(stats, modelName, RequestDetail{
		Timestamp: timestamp,
		LatencyMs: normaliseLatency(record.Latency),
		Source:    sanitizeUsageDetailSource(record.Source),
		AuthIndex: sanitizeUsageDetailAuthIndex(record.AuthIndex),
		Tokens:    detail,
		Failed:    failed,
	})

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func (s *RequestStatistics) apiStatsForKey(key string) *apiStats {
	key = normaliseUsageIdentifier(key)
	if stats, ok := s.apis[key]; ok && stats != nil {
		if stats.Models == nil {
			stats.Models = make(map[string]*modelStats)
		}
		return stats
	}
	if key != overflowUsageBucket && len(s.apis) >= maxTrackedAPIs-1 {
		key = overflowUsageBucket
		if stats, ok := s.apis[key]; ok && stats != nil {
			if stats.Models == nil {
				stats.Models = make(map[string]*modelStats)
			}
			return stats
		}
	}
	stats := &apiStats{Models: make(map[string]*modelStats)}
	s.apis[key] = stats
	return stats
}

func (s *RequestStatistics) updateAPIStats(stats *apiStats, model string, detail RequestDetail) {
	if stats == nil {
		return
	}
	if stats.Models == nil {
		stats.Models = make(map[string]*modelStats)
	}
	stats.TotalRequests++
	stats.TotalTokens += detail.Tokens.TotalTokens
	if detail.Failed {
		stats.FailureCount++
	}
	model = modelBucketName(stats, model)
	modelStatsValue, ok := stats.Models[model]
	if !ok {
		modelStatsValue = &modelStats{}
		stats.Models[model] = modelStatsValue
	}
	modelStatsValue.TotalRequests++
	modelStatsValue.TotalTokens += detail.Tokens.TotalTokens
	if detail.Failed {
		modelStatsValue.FailureCount++
	}
	modelStatsValue.Details = append(modelStatsValue.Details, detail)
	if len(modelStatsValue.Details) > maxRequestDetailsPerModel {
		modelStatsValue.Details = modelStatsValue.Details[len(modelStatsValue.Details)-maxRequestDetailsPerModel:]
	}
}

func modelBucketName(stats *apiStats, model string) string {
	model = normaliseUsageIdentifier(model)
	if stats == nil || stats.Models == nil {
		return model
	}
	if _, ok := stats.Models[model]; ok {
		return model
	}
	if model != overflowUsageBucket && len(stats.Models) >= maxModelsPerAPI-1 {
		return overflowUsageBucket
	}
	return model
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
		apiName = sanitizeUsageAPIIdentifier(apiName)
		apiSnapshot := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			FailureCount:  stats.FailureCount,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelStatsValue := range stats.Models {
			requestDetails := make([]RequestDetail, len(modelStatsValue.Details))
			copy(requestDetails, modelStatsValue.Details)
			for i := range requestDetails {
				requestDetails[i] = sanitizeRequestDetail(requestDetails[i])
			}
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests: modelStatsValue.TotalRequests,
				TotalTokens:   modelStatsValue.TotalTokens,
				FailureCount:  modelStatsValue.FailureCount,
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

// RestoreSnapshot replaces the current store with an exported statistics snapshot.
func (s *RequestStatistics) RestoreSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	next := NewRequestStatistics()
	next.loadSnapshot(snapshot)
	restored := next.Snapshot()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests = restored.TotalRequests
	s.successCount = restored.SuccessCount
	s.failureCount = restored.FailureCount
	s.totalTokens = restored.TotalTokens
	s.apis = next.apis
	s.requestsByDay = next.requestsByDay
	s.requestsByHour = next.requestsByHour
	s.tokensByDay = next.tokensByDay
	s.tokensByHour = next.tokensByHour
	result.Added = restored.TotalRequests
	return result
}

func (s *RequestStatistics) loadSnapshot(snapshot StatisticsSnapshot) {
	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = sanitizeUsageAPIIdentifier(apiName)
		stats := s.apiStatsForKey(apiName)
		var apiRequests int64
		var apiTokens int64
		var apiFailures int64

		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = modelBucketName(stats, modelName)
			modelStatsValue, ok := stats.Models[modelName]
			if !ok || modelStatsValue == nil {
				modelStatsValue = &modelStats{}
				stats.Models[modelName] = modelStatsValue
			}

			details, detailTotals := normaliseRequestDetails(modelSnapshot.Details)
			modelRequests := maxInt64(nonNegativeInt64(modelSnapshot.TotalRequests), detailTotals.requests)
			modelTokens := maxInt64(nonNegativeInt64(modelSnapshot.TotalTokens), detailTotals.tokens)
			modelFailures := maxInt64(nonNegativeInt64(modelSnapshot.FailureCount), detailTotals.failures)

			modelStatsValue.TotalRequests += modelRequests
			modelStatsValue.TotalTokens += modelTokens
			modelStatsValue.FailureCount += modelFailures
			modelStatsValue.Details = append(modelStatsValue.Details, details...)
			if len(modelStatsValue.Details) > maxRequestDetailsPerModel {
				modelStatsValue.Details = modelStatsValue.Details[len(modelStatsValue.Details)-maxRequestDetailsPerModel:]
			}

			apiRequests += modelRequests
			apiTokens += modelTokens
			apiFailures += modelFailures
		}
		stats.TotalRequests += maxInt64(nonNegativeInt64(apiSnapshot.TotalRequests), apiRequests)
		stats.TotalTokens += maxInt64(nonNegativeInt64(apiSnapshot.TotalTokens), apiTokens)
		stats.FailureCount += maxInt64(nonNegativeInt64(apiSnapshot.FailureCount), apiFailures)
	}

	s.copySnapshotTimeMaps(snapshot)
	s.rebuildRootTotals(snapshot)
}

type detailAggregate struct {
	requests int64
	tokens   int64
	failures int64
}

func normaliseRequestDetails(details []RequestDetail) ([]RequestDetail, detailAggregate) {
	if len(details) > maxRequestDetailsPerModel {
		details = details[len(details)-maxRequestDetailsPerModel:]
	}
	now := time.Now()
	out := make([]RequestDetail, 0, len(details))
	var totals detailAggregate
	for _, detail := range details {
		detail.Tokens = normaliseTokenStats(detail.Tokens)
		if detail.Tokens.TotalTokens < 0 {
			detail.Tokens.TotalTokens = 0
		}
		if detail.LatencyMs < 0 {
			detail.LatencyMs = 0
		}
		if detail.Timestamp.IsZero() {
			detail.Timestamp = now
		}
		detail = sanitizeRequestDetail(detail)
		out = append(out, detail)
		totals.requests++
		totals.tokens += detail.Tokens.TotalTokens
		if detail.Failed {
			totals.failures++
		}
	}
	return out, totals
}

func (s *RequestStatistics) copySnapshotTimeMaps(snapshot StatisticsSnapshot) {
	s.requestsByDay = copyStringInt64Map(snapshot.RequestsByDay)
	s.tokensByDay = copyStringInt64Map(snapshot.TokensByDay)
	s.requestsByHour = copyHourInt64Map(snapshot.RequestsByHour)
	s.tokensByHour = copyHourInt64Map(snapshot.TokensByHour)
}

func (s *RequestStatistics) rebuildRootTotals(snapshot StatisticsSnapshot) {
	var requests int64
	var tokens int64
	var failures int64
	for _, stats := range s.apis {
		if stats == nil {
			continue
		}
		requests += stats.TotalRequests
		tokens += stats.TotalTokens
		failures += stats.FailureCount
	}
	s.totalRequests = maxInt64(nonNegativeInt64(snapshot.TotalRequests), requests)
	s.failureCount = maxInt64(nonNegativeInt64(snapshot.FailureCount), failures)
	s.totalTokens = maxInt64(nonNegativeInt64(snapshot.TotalTokens), tokens)
	s.successCount = maxInt64(nonNegativeInt64(snapshot.SuccessCount), s.totalRequests-s.failureCount)
	if s.successCount+s.failureCount > s.totalRequests {
		s.totalRequests = s.successCount + s.failureCount
	}
}

func resolveAPIIdentifier(ctx context.Context, record coreusage.Record) string {
	if endpoint := strings.TrimSpace(internallogging.GetEndpoint(ctx)); endpoint != "" {
		return endpoint
	}
	if provider := strings.TrimSpace(record.Provider); provider != "" {
		return provider
	}
	return unknownUsageBucket
}

func resolveSuccess(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
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

func normaliseUsageIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return unknownUsageBucket
	}
	return trimRunes(value, maxUsageIdentifierRunes)
}

func sanitizeRequestDetail(detail RequestDetail) RequestDetail {
	detail.Source = sanitizeUsageDetailSource(detail.Source)
	detail.AuthIndex = sanitizeUsageDetailAuthIndex(detail.AuthIndex)
	return detail
}

func sanitizeUsageAPIIdentifier(value string) string {
	raw := strings.TrimSpace(value)
	normalized := normaliseUsageIdentifier(raw)
	if isSafeUsageIdentifier(normalized) {
		return normalized
	}
	return "api-key:" + shortUsageHash(raw)
}

func sanitizeUsageDetailSource(value string) string {
	raw := strings.TrimSpace(value)
	normalized := normaliseUsageIdentifier(raw)
	if normalized == unknownUsageBucket || isSafeUsageIdentifier(normalized) {
		return normalized
	}
	return "source:" + shortUsageHash(raw)
}

func sanitizeUsageDetailAuthIndex(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	normalized := trimRunes(value, maxUsageIdentifierRunes)
	lower := strings.ToLower(normalized)
	if strings.HasPrefix(lower, "auth:") {
		hash := strings.TrimPrefix(lower, "auth:")
		if isHexIdentifier(hash, 12) || isHexIdentifier(hash, 16) || isHexIdentifier(hash, 64) {
			return normalized
		}
		return "auth:" + shortUsageHash(value)
	}
	if isHexIdentifier(lower, 16) || isHexIdentifier(lower, 64) {
		return normalized
	}
	return "auth:" + shortUsageHash(value)
}

func isSafeUsageIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	lower := strings.ToLower(value)
	if lower == unknownUsageBucket || lower == overflowUsageBucket {
		return true
	}
	if strings.HasPrefix(lower, "api-key:") {
		hash := strings.TrimPrefix(lower, "api-key:")
		return isHexIdentifier(hash, 8) || isHexIdentifier(hash, 12) || isHexIdentifier(hash, 64)
	}
	if strings.HasPrefix(lower, "source:") {
		hash := strings.TrimPrefix(lower, "source:")
		return isHexIdentifier(hash, 12) || isHexIdentifier(hash, 64)
	}
	if isHTTPRoute(value) || isSafePathIdentifier(value) {
		return true
	}
	switch lower {
	case "gemini", "gemini-cli", "aistudio", "vertex", "claude", "codex", "openai",
		"openai-compatibility", "openai-compatible", "antigravity", "github-copilot",
		"gitlab", "cursor", "kiro", "kilo", "kimi", "iflow", "codebuddy", "local":
		return true
	default:
		return false
	}
}

func isSafePathIdentifier(value string) bool {
	if !strings.HasPrefix(value, "/") {
		return false
	}
	lower := strings.ToLower(value)
	return !strings.Contains(value, "?") &&
		!strings.Contains(lower, "key") &&
		!strings.Contains(lower, "token") &&
		!strings.Contains(lower, "auth")
}

func isHTTPRoute(value string) bool {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.HasPrefix(parts[1], "/") {
		return false
	}
	switch strings.ToUpper(parts[0]) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

func isHexIdentifier(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func secretUsageBucket(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return fmt.Sprintf("api-key:%s", shortUsageHash(value)[:8])
}

func shortUsageHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func trimRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func maxInt64(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func copyStringInt64Map(source map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(source))
	for key, value := range source {
		out[key] = nonNegativeInt64(value)
	}
	return out
}

func copyHourInt64Map(source map[string]int64) map[int]int64 {
	out := make(map[int]int64, len(source))
	for key, value := range source {
		hour, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil {
			continue
		}
		hour = hour % 24
		if hour < 0 {
			hour += 24
		}
		out[hour] = nonNegativeInt64(value)
	}
	return out
}
