// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

var statisticsEnabled atomic.Bool
var statisticsRedactDetails atomic.Bool

// statsFileName defines the location of the stats file.
const statsFileName = "usage_stats.json"
const maxRequestDetails = 500

func init() {
	statisticsEnabled.Store(true)
	statisticsRedactDetails.Store(false)
	coreusage.RegisterPlugin(NewLoggerPlugin())

	// Automatically load existing data if the file exists
	if err := defaultRequestStatistics.Load(statsFileName); err != nil {
		if !os.IsNotExist(err) {
			log.Warnf("Failed to load usage stats from %s: %v", statsFileName, err)
		}
	} else {
		log.Infof("Loaded usage stats from %s", statsFileName)
	}

	// Start a background routine to save data every 1 minute
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if err := defaultRequestStatistics.Save(statsFileName); err != nil {
				log.Warnf("Failed to save usage stats to %s: %v", statsFileName, err)
			}
		}
	}()
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

// SetStatisticsRedactDetails toggles whether sensitive request details are redacted.
func SetStatisticsRedactDetails(enabled bool) {
	previous := statisticsRedactDetails.Swap(enabled)
	if enabled && !previous {
		defaultRequestStatistics.RedactSensitiveDetails()
	}
}

// StatisticsRedactDetails reports the current redaction state.
func StatisticsRedactDetails() bool { return statisticsRedactDetails.Load() }

// RequestStatistics maintains aggregated request metrics in memory.
type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64

	apis map[string]*apiStats

	requestsByDay    map[string]int64
	requestsByHour   map[int]int64
	requestsByMinute map[string]int64
	tokensByDay      map[string]int64
	tokensByHour     map[int]int64
	tokensByMinute   map[string]int64
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

	RequestsByDay    map[string]int64 `json:"requests_by_day"`
	RequestsByHour   map[string]int64 `json:"requests_by_hour"`
	RequestsByMinute map[string]int64 `json:"requests_by_minute"`
	TokensByDay      map[string]int64 `json:"tokens_by_day"`
	TokensByHour     map[string]int64 `json:"tokens_by_hour"`
	TokensByMinute   map[string]int64 `json:"tokens_by_minute"`
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
		apis:             make(map[string]*apiStats),
		requestsByDay:    make(map[string]int64),
		requestsByHour:   make(map[int]int64),
		requestsByMinute: make(map[string]int64),
		tokensByDay:      make(map[string]int64),
		tokensByHour:     make(map[int]int64),
		tokensByMinute:   make(map[string]int64),
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
	statsKey := resolveStatisticsKey(ctx, record)
	if statisticsRedactDetails.Load() {
		statsKey = redactAPIIdentifier(statsKey)
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
	minuteKey := timestamp.Format("2006-01-02 15:04")

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
		Timestamp: timestamp,
		Source:    record.Source,
		AuthIndex: record.AuthIndex,
		Tokens:    detail,
		Failed:    failed,
	}
	if statisticsRedactDetails.Load() {
		requestDetail.Source = ""
		requestDetail.AuthIndex = ""
	}
	s.updateAPIStats(stats, modelName, requestDetail)

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.requestsByMinute[minuteKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
	s.tokensByMinute[minuteKey] += totalTokens
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
	modelStatsValue.Details = trimRequestDetails(modelStatsValue.Details)
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
	redact := statisticsRedactDetails.Load()
	for apiName, stats := range s.apis {
		if redact {
			apiName = redactAPIIdentifier(apiName)
		}
		apiSnapshot := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelStatsValue := range stats.Models {
			requestDetails := make([]RequestDetail, len(modelStatsValue.Details))
			copy(requestDetails, modelStatsValue.Details)
			if redact {
				for i := range requestDetails {
					requestDetails[i].Source = ""
					requestDetails[i].AuthIndex = ""
				}
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

	result.RequestsByMinute = make(map[string]int64, len(s.requestsByMinute))
	for minute, v := range s.requestsByMinute {
		result.RequestsByMinute[minute] = v
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

	result.TokensByMinute = make(map[string]int64, len(s.tokensByMinute))
	for minute, v := range s.tokensByMinute {
		result.TokensByMinute[minute] = v
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
	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()
	minuteKey := detail.Timestamp.Format("2006-01-02 15:04")

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.requestsByMinute[minuteKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
	s.tokensByMinute[minuteKey] += totalTokens
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

// Save writes the current statistics to a JSON file.
func (s *RequestStatistics) Save(filename string) error {
	snapshot := s.Snapshot()

	// OPTIMIZATION: Trim the 'Details' slice from the snapshot before saving.
	// This keeps the JSON file bounded while preserving recent request context.
	for apiKey, apiSnap := range snapshot.APIs {
		for modelName, modelSnap := range apiSnap.Models {
			modelSnap.Details = trimRequestDetails(modelSnap.Details)
			apiSnap.Models[modelName] = modelSnap
		}
		snapshot.APIs[apiKey] = apiSnap
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0600)
}

// Load reads statistics from a JSON file and restores the state.
func (s *RequestStatistics) Load(filename string) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	s.Restore(snapshot)
	return nil
}

// Restore populates the RequestStatistics from a snapshot.
func (s *RequestStatistics) Restore(snapshot StatisticsSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests = snapshot.TotalRequests
	s.successCount = snapshot.SuccessCount
	s.failureCount = snapshot.FailureCount
	s.totalTokens = snapshot.TotalTokens

	// Restore APIs
	for apiName, apiSnap := range snapshot.APIs {
		stats := &apiStats{
			TotalRequests: apiSnap.TotalRequests,
			TotalTokens:   apiSnap.TotalTokens,
			Models:        make(map[string]*modelStats),
		}
		for modelName, modelSnap := range apiSnap.Models {
			// Details from the file are a trimmed list of recent requests due to optimization.
			// Initialize a new slice for details to ensure the loaded stats are independent.
			details := make([]RequestDetail, 0)
			if len(modelSnap.Details) > 0 {
				details = make([]RequestDetail, len(modelSnap.Details))
				copy(details, modelSnap.Details)
			}
			stats.Models[modelName] = &modelStats{
				TotalRequests: modelSnap.TotalRequests,
				TotalTokens:   modelSnap.TotalTokens,
				Details:       details,
			}
		}
		s.apis[apiName] = stats
	}

	// Restore Trends Maps
	for k, v := range snapshot.RequestsByDay {
		s.requestsByDay[k] = v
	}
	for k, v := range snapshot.TokensByDay {
		s.tokensByDay[k] = v
	}
	for k, v := range snapshot.RequestsByHour {
		if h, err := strconv.Atoi(k); err == nil {
			s.requestsByHour[h] = v
		}
	}
	for k, v := range snapshot.RequestsByMinute {
		s.requestsByMinute[k] = v
	}
	for k, v := range snapshot.TokensByHour {
		if h, err := strconv.Atoi(k); err == nil {
			s.tokensByHour[h] = v
		}
	}
	for k, v := range snapshot.TokensByMinute {
		s.tokensByMinute[k] = v
	}
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

func resolveStatisticsKey(ctx context.Context, record coreusage.Record) string {
	if record.APIKey != "" {
		return record.APIKey
	}
	return resolveAPIIdentifier(ctx, record)
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

func trimRequestDetails(details []RequestDetail) []RequestDetail {
	if len(details) <= maxRequestDetails {
		return details
	}
	sort.Slice(details, func(i, j int) bool {
		return details[i].Timestamp.Before(details[j].Timestamp)
	})
	start := len(details) - maxRequestDetails
	trimmed := make([]RequestDetail, maxRequestDetails)
	copy(trimmed, details[start:])
	return trimmed
}

func redactAPIIdentifier(identifier string) string {
	if strings.HasPrefix(identifier, "redacted-") {
		return identifier
	}
	sum := sha256.Sum256([]byte(identifier))
	return "redacted-" + hex.EncodeToString(sum[:])
}

// RedactSensitiveDetails removes sensitive identifiers from stored usage statistics.
func (s *RequestStatistics) RedactSensitiveDetails() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	redactedAPIs := make(map[string]*apiStats, len(s.apis))
	for apiName, stats := range s.apis {
		redactedKey := redactAPIIdentifier(apiName)
		if stats == nil {
			continue
		}
		for _, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			for i := range modelStatsValue.Details {
				modelStatsValue.Details[i].Source = ""
				modelStatsValue.Details[i].AuthIndex = ""
			}
		}
		existing, ok := redactedAPIs[redactedKey]
		if !ok {
			redactedAPIs[redactedKey] = stats
			continue
		}
		existing.TotalRequests += stats.TotalRequests
		existing.TotalTokens += stats.TotalTokens
		if existing.Models == nil {
			existing.Models = make(map[string]*modelStats)
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			existingModel, ok := existing.Models[modelName]
			if !ok {
				existing.Models[modelName] = modelStatsValue
				continue
			}
			existingModel.TotalRequests += modelStatsValue.TotalRequests
			existingModel.TotalTokens += modelStatsValue.TotalTokens
			existingModel.Details = append(existingModel.Details, modelStatsValue.Details...)
			existingModel.Details = trimRequestDetails(existingModel.Details)
		}
	}
	s.apis = redactedAPIs
}

func formatHour(hour int) string {
	if hour < 0 {
		hour = 0
	}
	hour = hour % 24
	return fmt.Sprintf("%02d", hour)
}
