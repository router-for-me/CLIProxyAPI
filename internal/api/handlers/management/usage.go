package management

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}

// GetUsageStatistics returns bounded client-key usage aggregates. Attempts and
// failures are final client-request counters; upstream_* fields are operational
// retry diagnostics. Estimated costs are not an authoritative billing ledger.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	filter, errFilter := parseUsageFilter(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}
	stats := h.usageStatistics()
	if stats == nil {
		c.JSON(http.StatusOK, internalusage.Report{
			Estimated:     true,
			Currency:      "USD",
			Currencies:    []string{},
			RetentionDays: config.DefaultUsageStatisticsRetentionDays,
			Keys:          []internalusage.ClientKeyUsage{},
			Daily:         []internalusage.DailyUsage{},
			Models:        []internalusage.ModelUsage{},
		})
		return
	}
	c.JSON(http.StatusOK, stats.Report(filter))
}

// GetClientKeyUsage is an explicit alias for GetUsageStatistics.
func (h *Handler) GetClientKeyUsage(c *gin.Context) { h.GetUsageStatistics(c) }

// ResetClientKeyUsage clears all retained client-key usage aggregates. It does
// not modify configured client keys or upstream provider quota state.
func (h *Handler) ResetClientKeyUsage(c *gin.Context) {
	stats := h.usageStatistics()
	if stats == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage statistics unavailable"})
		return
	}
	resetCount, errReset := stats.ResetAllClientKeyUsage(c.Request.Context())
	if errReset != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist reset usage statistics"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":      "ok",
		"reset_count": resetCount,
	})
}

// ExportUsageStatistics returns the versioned secret-safe aggregate snapshot.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	filter, errFilter := parseUsageFilter(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}
	stats := h.usageStatistics()
	if stats == nil {
		c.JSON(http.StatusOK, internalusage.Snapshot{Version: internalusage.SnapshotVersion})
		return
	}
	c.Header("Content-Disposition", `attachment; filename="usage-v1.json"`)
	c.JSON(http.StatusOK, stats.ExportSnapshotFiltered(filter))
}

// ExportUsageStatisticsCSV returns one row per retained aggregate bucket.
func (h *Handler) ExportUsageStatisticsCSV(c *gin.Context) {
	filter, errFilter := parseUsageFilter(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}
	stats := h.usageStatistics()
	rows := []internalusage.ExportRow{}
	if stats != nil {
		rows = stats.ExportRows(filter)
	}

	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	header := []string{
		"day", "is_overflow", "key_id", "alias", "masked_key", "provider", "model", "service_tier",
		"first_used_at", "last_used_at", "attempts", "success", "failed", "upstream_attempts", "upstream_failed_attempts",
		"input_tokens", "output_tokens", "reasoning_tokens", "cached_tokens",
		"cache_read_tokens", "cache_creation_tokens", "total_tokens",
		"latency_ms_total", "latency_samples", "ttft_ms_total", "ttft_samples",
		"estimated_cost_micros", "estimated_cost_micros_by_currency",
		"unpriced_tokens", "unpriced_attempts", "overflow_attempts", "pricing_versions",
	}
	if errWrite := writer.Write(header); errWrite != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate csv"})
		return
	}
	for _, row := range rows {
		metrics := row.Metrics
		record := []string{
			row.Day,
			strconv.FormatBool(row.Overflow),
			safeCSVCell(row.ClientKeyID),
			safeCSVCell(row.Alias),
			safeCSVCell(row.MaskedKey),
			safeCSVCell(row.Provider),
			safeCSVCell(row.Model),
			safeCSVCell(row.ServiceTier),
			formatUsageTime(row.FirstUsedAt),
			formatUsageTime(row.LastUsedAt),
			strconv.FormatInt(metrics.Attempts, 10),
			strconv.FormatInt(metrics.Success, 10),
			strconv.FormatInt(metrics.Failed, 10),
			strconv.FormatInt(metrics.UpstreamAttempts, 10),
			strconv.FormatInt(metrics.UpstreamFailedAttempts, 10),
			strconv.FormatInt(metrics.Tokens.InputTokens, 10),
			strconv.FormatInt(metrics.Tokens.OutputTokens, 10),
			strconv.FormatInt(metrics.Tokens.ReasoningTokens, 10),
			strconv.FormatInt(metrics.Tokens.CachedTokens, 10),
			strconv.FormatInt(metrics.Tokens.CacheReadTokens, 10),
			strconv.FormatInt(metrics.Tokens.CacheCreationTokens, 10),
			strconv.FormatInt(metrics.Tokens.TotalTokens, 10),
			strconv.FormatInt(metrics.LatencyMsTotal, 10),
			strconv.FormatInt(metrics.LatencySamples, 10),
			strconv.FormatInt(metrics.TTFTMsTotal, 10),
			strconv.FormatInt(metrics.TTFTSamples, 10),
			strconv.FormatInt(metrics.EstimatedCostMicros, 10),
			safeCSVCell(formatCurrencyCosts(metrics.EstimatedCostMicrosByCurrency)),
			strconv.FormatInt(metrics.UnpricedTokens, 10),
			strconv.FormatInt(metrics.UnpricedAttempts, 10),
			strconv.FormatInt(metrics.OverflowAttempts, 10),
			safeCSVCell(formatPricingVersions(metrics.PricingVersions)),
		}
		if errWrite := writer.Write(record); errWrite != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate csv"})
			return
		}
	}
	writer.Flush()
	if errCSV := writer.Error(); errCSV != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate csv"})
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="usage.csv"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buffer.Bytes())
}

// ExportUsageCSV is a concise route-handler alias.
func (h *Handler) ExportUsageCSV(c *gin.Context) { h.ExportUsageStatisticsCSV(c) }

func (h *Handler) usageStatistics() *internalusage.Collector {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	stats := h.usageCollector
	h.mu.Unlock()
	return stats
}

func parseUsageFilter(c *gin.Context) (internalusage.Filter, error) {
	filter := internalusage.Filter{}
	if c == nil {
		return filter, nil
	}
	var errDate error
	if value := strings.TrimSpace(c.Query("from")); value != "" {
		filter.From, errDate = parseUsageDate(value)
		if errDate != nil {
			return filter, errors.New("from must be YYYY-MM-DD or RFC3339")
		}
	}
	if value := strings.TrimSpace(c.Query("to")); value != "" {
		filter.To, errDate = parseUsageDate(value)
		if errDate != nil {
			return filter, errors.New("to must be YYYY-MM-DD or RFC3339")
		}
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To) {
		return filter, errors.New("from must not be after to")
	}
	filter.ClientKeyID = strings.TrimSpace(c.Query("key_id"))
	if filter.ClientKeyID == "" {
		filter.ClientKeyID = strings.TrimSpace(c.Query("client_key_id"))
	}
	filter.Provider = strings.TrimSpace(c.Query("provider"))
	filter.Model = strings.TrimSpace(c.Query("model"))
	filter.ServiceTier = strings.TrimSpace(c.Query("service_tier"))
	return filter, nil
}

func parseUsageDate(value string) (time.Time, error) {
	if parsed, errParse := time.Parse(time.DateOnly, value); errParse == nil {
		return parsed.UTC(), nil
	}
	parsed, errParse := time.Parse(time.RFC3339, value)
	if errParse != nil {
		return time.Time{}, errParse
	}
	return time.Date(parsed.UTC().Year(), parsed.UTC().Month(), parsed.UTC().Day(), 0, 0, 0, 0, time.UTC), nil
}

func formatUsageTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func formatPricingVersions(versions map[string]int64) string {
	keys := make([]string, 0, len(versions))
	for version := range versions {
		keys = append(keys, version)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, version := range keys {
		parts = append(parts, version+":"+strconv.FormatInt(versions[version], 10))
	}
	return strings.Join(parts, ";")
}

func formatCurrencyCosts(costs map[string]int64) string {
	keys := make([]string, 0, len(costs))
	for currency := range costs {
		keys = append(keys, currency)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, currency := range keys {
		parts = append(parts, currency+":"+strconv.FormatInt(costs[currency], 10))
	}
	return strings.Join(parts, ";")
}

func safeCSVCell(value string) string {
	if value == "" {
		return ""
	}
	switch value[0] {
	case '=', '+', '-', '@':
		return "'" + value
	default:
		return value
	}
}
