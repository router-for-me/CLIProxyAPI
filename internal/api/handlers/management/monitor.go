package management

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

const (
	monitorDefaultPage     = 1
	monitorDefaultPageSize = 20
	monitorMaxPageSize     = 200
	monitorDefaultTopLimit = 10
	monitorMaxTopLimit     = 100
	monitorRecentLimit     = 12
)

type monitorRecord struct {
	Timestamp       time.Time
	APIKey          string
	Model           string
	Source          string
	AuthIndex       string
	Failed          bool
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
}

type monitorRecordFilter struct {
	APIKey      string
	APIContains string
	Model       string
	Source      string
	Status      string
	Start       *time.Time
	End         *time.Time
}

type monitorRecentRequest struct {
	Failed    bool      `json:"failed"`
	Timestamp time.Time `json:"timestamp"`
}

type monitorTimeRange struct {
	Start *time.Time `json:"start_time,omitempty"`
	End   *time.Time `json:"end_time,omitempty"`
}

type monitorRequestLogItem struct {
	Timestamp       time.Time              `json:"timestamp"`
	APIKey          string                 `json:"api_key"`
	Model           string                 `json:"model"`
	Source          string                 `json:"source"`
	AuthIndex       string                 `json:"auth_index"`
	Failed          bool                   `json:"failed"`
	InputTokens     int64                  `json:"input_tokens"`
	OutputTokens    int64                  `json:"output_tokens"`
	ReasoningTokens int64                  `json:"reasoning_tokens"`
	CachedTokens    int64                  `json:"cached_tokens"`
	TotalTokens     int64                  `json:"total_tokens"`
	RequestCount    int64                  `json:"request_count"`
	SuccessRate     float64                `json:"success_rate"`
	RecentRequests  []monitorRecentRequest `json:"recent_requests"`
}

type monitorFilterOptions struct {
	APIs    []string `json:"apis,omitempty"`
	Models  []string `json:"models,omitempty"`
	Sources []string `json:"sources,omitempty"`
}

type monitorModelStats struct {
	Model         string                 `json:"model"`
	Requests      int64                  `json:"requests"`
	Success       int64                  `json:"success"`
	Failed        int64                  `json:"failed"`
	SuccessRate   float64                `json:"success_rate"`
	LastRequestAt *time.Time             `json:"last_request_at,omitempty"`
	Recent        []monitorRecentRequest `json:"recent_requests"`
}

type monitorChannelStatsItem struct {
	Source          string                 `json:"source"`
	TotalRequests   int64                  `json:"total_requests"`
	SuccessRequests int64                  `json:"success_requests"`
	FailedRequests  int64                  `json:"failed_requests"`
	SuccessRate     float64                `json:"success_rate"`
	LastRequestAt   *time.Time             `json:"last_request_at,omitempty"`
	Recent          []monitorRecentRequest `json:"recent_requests"`
	Models          []monitorModelStats    `json:"models"`
}

type monitorFailureStatsItem struct {
	Source       string              `json:"source"`
	FailedCount  int64               `json:"failed_count"`
	LastFailedAt *time.Time          `json:"last_failed_at,omitempty"`
	Models       []monitorModelStats `json:"models"`
}

type monitorChannelAggregate struct {
	Source          string
	TotalRequests   int64
	SuccessRequests int64
	FailedRequests  int64
	LastRequestAt   time.Time
	Recent          []monitorRecentRequest
	Models          map[string]*monitorModelAggregate
}

type monitorModelAggregate struct {
	Model         string
	Requests      int64
	Success       int64
	Failed        int64
	LastRequestAt time.Time
	Recent        []monitorRecentRequest
}

type monitorRequestGroupStats struct {
	Total   int64
	Success int64
	Recent  []monitorRecentRequest
}

// usageSnapshot returns usage snapshot with database+memory data when available.
func (h *Handler) usageSnapshot() usage.StatisticsSnapshot {
	if dbPlugin := usage.GetDatabasePlugin(); dbPlugin != nil {
		return dbPlugin.GetCombinedSnapshot()
	}
	if h != nil && h.usageStats != nil {
		return h.usageStats.Snapshot()
	}
	return usage.StatisticsSnapshot{}
}

// GetMonitorRequestLogs returns request logs filtered by time and paginated on server side.
func (h *Handler) GetMonitorRequestLogs(c *gin.Context) {
	start, end, err := parseMonitorTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	status, err := parseStatusFilter(firstQuery(c, "status"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	page, pageSize, err := parsePagination(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	filter := monitorRecordFilter{
		APIKey:      firstQuery(c, "api", "api_key"),
		APIContains: firstQuery(c, "api_filter", "apiFilter", "api_like", "apiLike", "q"),
		Model:       firstQuery(c, "model"),
		Source:      firstQuery(c, "source", "channel"),
		Status:      status,
		Start:       start,
		End:         end,
	}

	if dbPlugin := usage.GetDatabasePlugin(); dbPlugin != nil {
		queryResult, queryErr := dbPlugin.QueryMonitorRequestLogs(c.Request.Context(), toUsageMonitorFilter(filter), page, pageSize, monitorRecentLimit)
		if queryErr == nil {
			items := make([]monitorRequestLogItem, 0, len(queryResult.Items))
			for _, row := range queryResult.Items {
				groupStats := queryResult.GroupStats[usage.MonitorGroupKey(row.Source, row.Model)]
				items = append(items, monitorRequestLogItem{
					Timestamp:       row.Timestamp,
					APIKey:          row.APIKey,
					Model:           row.Model,
					Source:          row.Source,
					AuthIndex:       row.AuthIndex,
					Failed:          row.Failed,
					InputTokens:     row.InputTokens,
					OutputTokens:    row.OutputTokens,
					ReasoningTokens: row.ReasoningTokens,
					CachedTokens:    row.CachedTokens,
					TotalTokens:     row.TotalTokens,
					RequestCount:    groupStats.Total,
					SuccessRate:     calcRate(groupStats.Success, groupStats.Total),
					RecentRequests:  fromUsageRecentRequests(groupStats.Recent),
				})
			}

			total := safeInt64ToInt(queryResult.Total)
			totalPages := calcTotalPages(total, queryResult.PageSize)

			c.JSON(http.StatusOK, gin.H{
				"items":       items,
				"page":        queryResult.Page,
				"page_size":   queryResult.PageSize,
				"total":       total,
				"total_pages": totalPages,
				"has_prev":    queryResult.Page > 1 && totalPages > 0,
				"has_next":    totalPages > 0 && queryResult.Page < totalPages,
				"filters": monitorFilterOptions{
					APIs:    queryResult.Filters.APIs,
					Models:  queryResult.Filters.Models,
					Sources: queryResult.Filters.Sources,
				},
				"time_range": monitorTimeRange{Start: start, End: end},
			})
			return
		}
	}

	logs := make([]monitorRequestLogItem, 0, 128)
	apiSet := make(map[string]struct{})
	modelSet := make(map[string]struct{})
	sourceSet := make(map[string]struct{})

	visitSnapshotRecords(h.usageSnapshot(), func(record monitorRecord) {
		if !filter.matches(record) {
			return
		}
		if record.APIKey != "" {
			apiSet[record.APIKey] = struct{}{}
		}
		if record.Model != "" {
			modelSet[record.Model] = struct{}{}
		}
		if record.Source != "" {
			sourceSet[record.Source] = struct{}{}
		}
		logs = append(logs, monitorRequestLogItem{
			Timestamp:       record.Timestamp,
			APIKey:          record.APIKey,
			Model:           record.Model,
			Source:          record.Source,
			AuthIndex:       record.AuthIndex,
			Failed:          record.Failed,
			InputTokens:     record.InputTokens,
			OutputTokens:    record.OutputTokens,
			ReasoningTokens: record.ReasoningTokens,
			CachedTokens:    record.CachedTokens,
			TotalTokens:     record.TotalTokens,
		})
	})

	sort.Slice(logs, func(i, j int) bool {
		return logs[i].Timestamp.After(logs[j].Timestamp)
	})
	requestStats := buildRequestGroupStats(logs)
	for i := range logs {
		stats := requestStats[requestGroupKey(logs[i].Source, logs[i].Model)]
		logs[i].RequestCount = stats.Total
		logs[i].SuccessRate = calcRate(stats.Success, stats.Total)
		logs[i].RecentRequests = copyRecentRequests(stats.Recent)
	}

	total := len(logs)
	totalPages := calcTotalPages(total, pageSize)
	if totalPages > 0 && page > totalPages {
		page = totalPages
	}
	items := paginate(logs, page, pageSize)

	c.JSON(http.StatusOK, gin.H{
		"items":       items,
		"page":        page,
		"page_size":   pageSize,
		"total":       total,
		"total_pages": totalPages,
		"has_prev":    page > 1 && totalPages > 0,
		"has_next":    totalPages > 0 && page < totalPages,
		"filters": monitorFilterOptions{
			APIs:    setToSortedSlice(apiSet),
			Models:  setToSortedSlice(modelSet),
			Sources: setToSortedSlice(sourceSet),
		},
		"time_range": monitorTimeRange{Start: start, End: end},
	})
}

// GetMonitorChannelStats returns aggregated channel statistics from usage records.
func (h *Handler) GetMonitorChannelStats(c *gin.Context) {
	start, end, err := parseMonitorTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	status, err := parseStatusFilter(firstQuery(c, "status"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	limit, err := parseTopLimit(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	filter := monitorRecordFilter{
		APIKey:      firstQuery(c, "api", "api_key"),
		APIContains: firstQuery(c, "api_filter", "apiFilter", "api_like", "apiLike", "q"),
		Source:      firstQuery(c, "source", "channel"),
		Start:       start,
		End:         end,
	}
	modelFilter := firstQuery(c, "model")

	if dbPlugin := usage.GetDatabasePlugin(); dbPlugin != nil {
		usageFilter := toUsageMonitorFilter(filter)
		usageFilter.Model = strings.TrimSpace(modelFilter)
		usageFilter.Status = strings.TrimSpace(status)

		queryResult, queryErr := dbPlugin.QueryMonitorChannelStats(c.Request.Context(), usageFilter, limit, monitorRecentLimit)
		if queryErr == nil {
			items := make([]monitorChannelStatsItem, 0, len(queryResult.Items))
			for _, channel := range queryResult.Items {
				models := make([]monitorModelStats, 0, len(channel.Models))
				for _, model := range channel.Models {
					models = append(models, monitorModelStats{
						Model:         model.Model,
						Requests:      model.Requests,
						Success:       model.Success,
						Failed:        model.Failed,
						SuccessRate:   calcRate(model.Success, model.Requests),
						LastRequestAt: cloneTimePointer(model.LastRequestAt),
						Recent:        fromUsageRecentRequests(model.Recent),
					})
				}

				items = append(items, monitorChannelStatsItem{
					Source:          channel.Source,
					TotalRequests:   channel.TotalRequests,
					SuccessRequests: channel.SuccessRequests,
					FailedRequests:  channel.FailedRequests,
					SuccessRate:     calcRate(channel.SuccessRequests, channel.TotalRequests),
					LastRequestAt:   cloneTimePointer(channel.LastRequestAt),
					Recent:          fromUsageRecentRequests(channel.Recent),
					Models:          models,
				})
			}

			c.JSON(http.StatusOK, gin.H{
				"items":   items,
				"total":   len(items),
				"limit":   limit,
				"filters": monitorFilterOptions{Models: queryResult.Filters.Models, Sources: queryResult.Filters.Sources},
				"time_range": monitorTimeRange{
					Start: start,
					End:   end,
				},
			})
			return
		}
	}

	channelMap := make(map[string]*monitorChannelAggregate)
	modelSet := make(map[string]struct{})
	sourceSet := make(map[string]struct{})

	visitSnapshotRecords(h.usageSnapshot(), func(record monitorRecord) {
		if !filter.matches(record) {
			return
		}
		source := record.Source
		if source == "" {
			source = "unknown"
		}
		agg, ok := channelMap[source]
		if !ok {
			agg = &monitorChannelAggregate{
				Source: source,
				Models: make(map[string]*monitorModelAggregate),
			}
			channelMap[source] = agg
		}

		agg.TotalRequests++
		if record.Failed {
			agg.FailedRequests++
		} else {
			agg.SuccessRequests++
		}
		if record.Timestamp.After(agg.LastRequestAt) {
			agg.LastRequestAt = record.Timestamp
		}
		agg.Recent = append(agg.Recent, monitorRecentRequest{Failed: record.Failed, Timestamp: record.Timestamp})

		modelAgg, ok := agg.Models[record.Model]
		if !ok {
			modelAgg = &monitorModelAggregate{Model: record.Model}
			agg.Models[record.Model] = modelAgg
		}
		modelAgg.Requests++
		if record.Failed {
			modelAgg.Failed++
		} else {
			modelAgg.Success++
		}
		if record.Timestamp.After(modelAgg.LastRequestAt) {
			modelAgg.LastRequestAt = record.Timestamp
		}
		modelAgg.Recent = append(modelAgg.Recent, monitorRecentRequest{Failed: record.Failed, Timestamp: record.Timestamp})

		sourceSet[source] = struct{}{}
		if record.Model != "" {
			modelSet[record.Model] = struct{}{}
		}
	})

	items := make([]monitorChannelStatsItem, 0, len(channelMap))
	for _, agg := range channelMap {
		if modelFilter != "" {
			if _, ok := agg.Models[modelFilter]; !ok {
				continue
			}
		}
		if status == "success" && agg.FailedRequests > 0 {
			continue
		}
		if status == "failed" && agg.FailedRequests == 0 {
			continue
		}

		models := make([]monitorModelStats, 0, len(agg.Models))
		for _, modelAgg := range agg.Models {
			models = append(models, monitorModelStats{
				Model:         modelAgg.Model,
				Requests:      modelAgg.Requests,
				Success:       modelAgg.Success,
				Failed:        modelAgg.Failed,
				SuccessRate:   calcRate(modelAgg.Success, modelAgg.Requests),
				LastRequestAt: timePointer(modelAgg.LastRequestAt),
				Recent:        normalizeRecentRequests(modelAgg.Recent),
			})
		}
		sort.Slice(models, func(i, j int) bool {
			if models[i].Requests == models[j].Requests {
				return models[i].Model < models[j].Model
			}
			return models[i].Requests > models[j].Requests
		})

		items = append(items, monitorChannelStatsItem{
			Source:          agg.Source,
			TotalRequests:   agg.TotalRequests,
			SuccessRequests: agg.SuccessRequests,
			FailedRequests:  agg.FailedRequests,
			SuccessRate:     calcRate(agg.SuccessRequests, agg.TotalRequests),
			LastRequestAt:   timePointer(agg.LastRequestAt),
			Recent:          normalizeRecentRequests(agg.Recent),
			Models:          models,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].TotalRequests == items[j].TotalRequests {
			return items[i].Source < items[j].Source
		}
		return items[i].TotalRequests > items[j].TotalRequests
	})
	if len(items) > limit {
		items = items[:limit]
	}

	c.JSON(http.StatusOK, gin.H{
		"items":      items,
		"total":      len(items),
		"limit":      limit,
		"filters":    monitorFilterOptions{Models: setToSortedSlice(modelSet), Sources: setToSortedSlice(sourceSet)},
		"time_range": monitorTimeRange{Start: start, End: end},
	})
}

// GetMonitorFailureAnalysis returns per-channel failure statistics aggregated on server side.
func (h *Handler) GetMonitorFailureAnalysis(c *gin.Context) {
	start, end, err := parseMonitorTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	limit, err := parseTopLimit(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	filter := monitorRecordFilter{
		APIKey:      firstQuery(c, "api", "api_key"),
		APIContains: firstQuery(c, "api_filter", "apiFilter", "api_like", "apiLike", "q"),
		Source:      firstQuery(c, "source", "channel"),
		Start:       start,
		End:         end,
	}
	modelFilter := firstQuery(c, "model")

	if dbPlugin := usage.GetDatabasePlugin(); dbPlugin != nil {
		usageFilter := toUsageMonitorFilter(filter)
		usageFilter.Model = strings.TrimSpace(modelFilter)

		queryResult, queryErr := dbPlugin.QueryMonitorFailureStats(c.Request.Context(), usageFilter, limit, monitorRecentLimit)
		if queryErr == nil {
			items := make([]monitorFailureStatsItem, 0, len(queryResult.Items))
			for _, channel := range queryResult.Items {
				models := make([]monitorModelStats, 0, len(channel.Models))
				for _, model := range channel.Models {
					models = append(models, monitorModelStats{
						Model:         model.Model,
						Requests:      model.Requests,
						Success:       model.Success,
						Failed:        model.Failed,
						SuccessRate:   calcRate(model.Success, model.Requests),
						LastRequestAt: cloneTimePointer(model.LastRequestAt),
						Recent:        fromUsageRecentRequests(model.Recent),
					})
				}

				items = append(items, monitorFailureStatsItem{
					Source:       channel.Source,
					FailedCount:  channel.FailedCount,
					LastFailedAt: cloneTimePointer(channel.LastFailedAt),
					Models:       models,
				})
			}

			c.JSON(http.StatusOK, gin.H{
				"items":   items,
				"total":   len(items),
				"limit":   limit,
				"filters": monitorFilterOptions{Models: queryResult.Filters.Models, Sources: queryResult.Filters.Sources},
				"time_range": monitorTimeRange{
					Start: start,
					End:   end,
				},
			})
			return
		}
	}

	filtered := make([]monitorRecord, 0, 128)
	failedSources := make(map[string]struct{})
	visitSnapshotRecords(h.usageSnapshot(), func(record monitorRecord) {
		if !filter.matches(record) {
			return
		}
		if record.Source == "" {
			record.Source = "unknown"
		}
		filtered = append(filtered, record)
		if record.Failed {
			failedSources[record.Source] = struct{}{}
		}
	})

	channelMap := make(map[string]*monitorChannelAggregate)
	modelSet := make(map[string]struct{})
	sourceSet := make(map[string]struct{})

	for _, record := range filtered {
		if _, ok := failedSources[record.Source]; !ok {
			continue
		}
		agg, ok := channelMap[record.Source]
		if !ok {
			agg = &monitorChannelAggregate{
				Source: record.Source,
				Models: make(map[string]*monitorModelAggregate),
			}
			channelMap[record.Source] = agg
		}

		agg.TotalRequests++
		if record.Failed {
			agg.FailedRequests++
		} else {
			agg.SuccessRequests++
		}
		if record.Failed && record.Timestamp.After(agg.LastRequestAt) {
			agg.LastRequestAt = record.Timestamp
		}

		modelAgg, ok := agg.Models[record.Model]
		if !ok {
			modelAgg = &monitorModelAggregate{Model: record.Model}
			agg.Models[record.Model] = modelAgg
		}
		modelAgg.Requests++
		if record.Failed {
			modelAgg.Failed++
		} else {
			modelAgg.Success++
		}
		if record.Timestamp.After(modelAgg.LastRequestAt) {
			modelAgg.LastRequestAt = record.Timestamp
		}
		modelAgg.Recent = append(modelAgg.Recent, monitorRecentRequest{Failed: record.Failed, Timestamp: record.Timestamp})

		sourceSet[record.Source] = struct{}{}
		if record.Model != "" {
			modelSet[record.Model] = struct{}{}
		}
	}

	items := make([]monitorFailureStatsItem, 0, len(channelMap))
	for _, agg := range channelMap {
		if modelFilter != "" {
			if _, ok := agg.Models[modelFilter]; !ok {
				continue
			}
		}

		models := make([]monitorModelStats, 0, len(agg.Models))
		for _, modelAgg := range agg.Models {
			models = append(models, monitorModelStats{
				Model:         modelAgg.Model,
				Requests:      modelAgg.Requests,
				Success:       modelAgg.Success,
				Failed:        modelAgg.Failed,
				SuccessRate:   calcRate(modelAgg.Success, modelAgg.Requests),
				LastRequestAt: timePointer(modelAgg.LastRequestAt),
				Recent:        normalizeRecentRequests(modelAgg.Recent),
			})
		}
		sort.Slice(models, func(i, j int) bool {
			if models[i].Failed == models[j].Failed {
				if models[i].Requests == models[j].Requests {
					return models[i].Model < models[j].Model
				}
				return models[i].Requests > models[j].Requests
			}
			return models[i].Failed > models[j].Failed
		})

		items = append(items, monitorFailureStatsItem{
			Source:       agg.Source,
			FailedCount:  agg.FailedRequests,
			LastFailedAt: timePointer(agg.LastRequestAt),
			Models:       models,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].FailedCount == items[j].FailedCount {
			return items[i].Source < items[j].Source
		}
		return items[i].FailedCount > items[j].FailedCount
	})
	if len(items) > limit {
		items = items[:limit]
	}

	c.JSON(http.StatusOK, gin.H{
		"items":      items,
		"total":      len(items),
		"limit":      limit,
		"filters":    monitorFilterOptions{Models: setToSortedSlice(modelSet), Sources: setToSortedSlice(sourceSet)},
		"time_range": monitorTimeRange{Start: start, End: end},
	})
}

func visitSnapshotRecords(snapshot usage.StatisticsSnapshot, visit func(record monitorRecord)) {
	for apiKey, api := range snapshot.APIs {
		for model, modelSnapshot := range api.Models {
			for _, detail := range modelSnapshot.Details {
				source := detail.Source
				if source == "" {
					source = "unknown"
				}
				visit(monitorRecord{
					Timestamp:       detail.Timestamp,
					APIKey:          apiKey,
					Model:           model,
					Source:          source,
					AuthIndex:       detail.AuthIndex,
					Failed:          detail.Failed,
					InputTokens:     detail.Tokens.InputTokens,
					OutputTokens:    detail.Tokens.OutputTokens,
					ReasoningTokens: detail.Tokens.ReasoningTokens,
					CachedTokens:    detail.Tokens.CachedTokens,
					TotalTokens:     detail.Tokens.TotalTokens,
				})
			}
		}
	}
}

func (f monitorRecordFilter) matches(record monitorRecord) bool {
	if f.APIKey != "" && record.APIKey != f.APIKey {
		return false
	}
	if f.APIContains != "" && !strings.Contains(strings.ToLower(record.APIKey), strings.ToLower(f.APIContains)) {
		return false
	}
	if f.Model != "" && record.Model != f.Model {
		return false
	}
	if f.Source != "" && record.Source != f.Source {
		return false
	}
	if f.Status == "success" && record.Failed {
		return false
	}
	if f.Status == "failed" && !record.Failed {
		return false
	}
	if f.Start != nil && record.Timestamp.Before(*f.Start) {
		return false
	}
	if f.End != nil && record.Timestamp.After(*f.End) {
		return false
	}
	return true
}

func buildRequestGroupStats(items []monitorRequestLogItem) map[string]monitorRequestGroupStats {
	statsMap := make(map[string]*monitorRequestGroupStats)
	for _, item := range items {
		key := requestGroupKey(item.Source, item.Model)
		stats, ok := statsMap[key]
		if !ok {
			stats = &monitorRequestGroupStats{}
			statsMap[key] = stats
		}
		stats.Total++
		if !item.Failed {
			stats.Success++
		}
		stats.Recent = append(stats.Recent, monitorRecentRequest{Failed: item.Failed, Timestamp: item.Timestamp})
	}

	result := make(map[string]monitorRequestGroupStats, len(statsMap))
	for key, stats := range statsMap {
		result[key] = monitorRequestGroupStats{
			Total:   stats.Total,
			Success: stats.Success,
			Recent:  normalizeRecentRequests(stats.Recent),
		}
	}

	return result
}

func copyRecentRequests(items []monitorRecentRequest) []monitorRecentRequest {
	if len(items) == 0 {
		return []monitorRecentRequest{}
	}
	cloned := make([]monitorRecentRequest, len(items))
	copy(cloned, items)
	return cloned
}

func requestGroupKey(source, model string) string {
	return source + "|||" + model
}

func toUsageMonitorFilter(filter monitorRecordFilter) usage.MonitorQueryFilter {
	return usage.MonitorQueryFilter{
		APIKey:      strings.TrimSpace(filter.APIKey),
		APIContains: strings.TrimSpace(filter.APIContains),
		Model:       strings.TrimSpace(filter.Model),
		Source:      strings.TrimSpace(filter.Source),
		Status:      strings.TrimSpace(filter.Status),
		Start:       filter.Start,
		End:         filter.End,
	}
}

func fromUsageRecentRequests(items []usage.MonitorRecentRequest) []monitorRecentRequest {
	if len(items) == 0 {
		return []monitorRecentRequest{}
	}
	out := make([]monitorRecentRequest, 0, len(items))
	for _, item := range items {
		out = append(out, monitorRecentRequest{Failed: item.Failed, Timestamp: item.Timestamp})
	}
	return out
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func safeInt64ToInt(value int64) int {
	if value <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if value > int64(maxInt) {
		return maxInt
	}
	return int(value)
}

func parseMonitorTimeRange(c *gin.Context) (*time.Time, *time.Time, error) {
	startRaw := firstQuery(c, "start_time", "start", "from", "startDate", "start_date")
	endRaw := firstQuery(c, "end_time", "end", "to", "endDate", "end_date")
	timeRange := strings.ToLower(firstQuery(c, "time_range", "timeRange", "range"))

	if startRaw == "" && endRaw == "" {
		if timeRange == "" || timeRange == "all" {
			return nil, nil, nil
		}
		return parsePresetTimeRange(timeRange, time.Now())
	}

	var (
		start *time.Time
		end   *time.Time
	)
	if startRaw != "" {
		parsed, parseErr := parseFlexibleTimestamp(startRaw, false)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		start = &parsed
	}
	if endRaw != "" {
		parsed, parseErr := parseFlexibleTimestamp(endRaw, true)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		end = &parsed
	}

	if start != nil && end != nil && start.After(*end) {
		return nil, nil, errInvalidTimeRange()
	}
	if start != nil && end == nil {
		now := time.Now()
		end = &now
	}
	return start, end, nil
}

func parsePresetTimeRange(value string, now time.Time) (*time.Time, *time.Time, error) {
	startOfDay := func(ts time.Time) time.Time {
		return time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, ts.Location())
	}
	endOfDay := func(ts time.Time) time.Time {
		return time.Date(ts.Year(), ts.Month(), ts.Day(), 23, 59, 59, int(time.Second-time.Nanosecond), ts.Location())
	}

	switch value {
	case "today", "1":
		start := startOfDay(now)
		end := now
		return &start, &end, nil
	case "yesterday":
		yesterday := now.AddDate(0, 0, -1)
		start := startOfDay(yesterday)
		end := endOfDay(yesterday)
		return &start, &end, nil
	case "daybeforeyesterday", "day_before_yesterday", "day-before-yesterday", "daybefore":
		d := now.AddDate(0, 0, -2)
		start := startOfDay(d)
		end := endOfDay(d)
		return &start, &end, nil
	default:
		if strings.HasSuffix(value, "d") {
			value = strings.TrimSuffix(value, "d")
		}
		days, err := strconv.Atoi(value)
		if err != nil || days <= 0 {
			return nil, nil, errInvalidTimeRange()
		}
		start := startOfDay(now.AddDate(0, 0, -(days - 1)))
		end := now
		return &start, &end, nil
	}
}

func parseFlexibleTimestamp(raw string, isEnd bool) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, errInvalidTimestamp()
	}

	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		if n > 1e12 {
			return time.UnixMilli(n), nil
		}
		return time.Unix(n, 0), nil
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	for _, layout := range layouts {
		if ts, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return ts, nil
		}
	}

	if day, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		if isEnd {
			day = day.Add(24*time.Hour - time.Nanosecond)
		}
		return day, nil
	}

	return time.Time{}, errInvalidTimestamp()
}

func parseStatusFilter(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return "", nil
	case "success":
		return "success", nil
	case "failed", "failure", "error":
		return "failed", nil
	default:
		return "", errInvalidStatus()
	}
}

func parsePagination(c *gin.Context) (int, int, error) {
	page, err := parseBoundedInt(firstQuery(c, "page"), monitorDefaultPage, 1, 1_000_000)
	if err != nil {
		return 0, 0, err
	}
	pageSize, err := parseBoundedInt(firstQuery(c, "page_size", "pageSize", "size", "limit"), monitorDefaultPageSize, 1, monitorMaxPageSize)
	if err != nil {
		return 0, 0, err
	}
	return page, pageSize, nil
}

func parseTopLimit(c *gin.Context) (int, error) {
	return parseBoundedInt(firstQuery(c, "limit"), monitorDefaultTopLimit, 1, monitorMaxTopLimit)
}

func parseBoundedInt(raw string, fallback, minValue, maxValue int) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, errInvalidInteger()
	}
	if n < minValue {
		return 0, errInvalidInteger()
	}
	if n > maxValue {
		n = maxValue
	}
	return n, nil
}

func firstQuery(c *gin.Context, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(c.Query(key)); value != "" {
			return value
		}
	}
	return ""
}

func paginate[T any](items []T, page, pageSize int) []T {
	if len(items) == 0 {
		return []T{}
	}
	if page < 1 {
		page = 1
	}
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []T{}
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func calcTotalPages(total, pageSize int) int {
	if total <= 0 {
		return 0
	}
	return (total + pageSize - 1) / pageSize
}

func normalizeRecentRequests(requests []monitorRecentRequest) []monitorRecentRequest {
	if len(requests) == 0 {
		return []monitorRecentRequest{}
	}
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].Timestamp.Before(requests[j].Timestamp)
	})
	if len(requests) > monitorRecentLimit {
		requests = requests[len(requests)-monitorRecentLimit:]
	}
	result := make([]monitorRecentRequest, len(requests))
	copy(result, requests)
	return result
}

func setToSortedSlice(items map[string]struct{}) []string {
	if len(items) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(items))
	for value := range items {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func calcRate(success, total int64) float64 {
	if total <= 0 {
		return 0
	}
	raw := float64(success) * 100 / float64(total)
	return math.Round(raw*10) / 10
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copyValue := value
	return &copyValue
}

func errInvalidInteger() error {
	return &monitorValidationError{msg: "invalid integer parameter"}
}

func errInvalidStatus() error {
	return &monitorValidationError{msg: "invalid status parameter"}
}

func errInvalidTimestamp() error {
	return &monitorValidationError{msg: "invalid timestamp parameter"}
}

func errInvalidTimeRange() error {
	return &monitorValidationError{msg: "invalid time range parameter"}
}

// --- Aggregation API types ---

type monitorKpiResponse struct {
	TotalRequests   int64            `json:"total_requests"`
	SuccessRequests int64            `json:"success_requests"`
	FailedRequests  int64            `json:"failed_requests"`
	SuccessRate     float64          `json:"success_rate"`
	TotalTokens     int64            `json:"total_tokens"`
	InputTokens     int64            `json:"input_tokens"`
	OutputTokens    int64            `json:"output_tokens"`
	ReasoningTokens int64            `json:"reasoning_tokens"`
	CachedTokens    int64            `json:"cached_tokens"`
	AvgTpm          float64          `json:"avg_tpm"`
	AvgRpm          float64          `json:"avg_rpm"`
	AvgRpd          float64          `json:"avg_rpd"`
	TimeRange       monitorTimeRange `json:"time_range"`
}

type monitorModelDistributionItem struct {
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
}

type monitorDailyTrendItem struct {
	Date            string `json:"date"`
	Requests        int64  `json:"requests"`
	SuccessRequests int64  `json:"success_requests"`
	FailedRequests  int64  `json:"failed_requests"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
	CachedTokens    int64  `json:"cached_tokens"`
}

type monitorHourlyModelsResponse struct {
	Hours        []string           `json:"hours"`
	Models       []string           `json:"models"`
	ModelData    map[string][]int64 `json:"model_data"`
	SuccessRates []float64          `json:"success_rates"`
	TimeRange    monitorTimeRange   `json:"time_range"`
}

type monitorHourlyTokensResponse struct {
	Hours           []string         `json:"hours"`
	TotalTokens     []int64          `json:"total_tokens"`
	InputTokens     []int64          `json:"input_tokens"`
	OutputTokens    []int64          `json:"output_tokens"`
	ReasoningTokens []int64          `json:"reasoning_tokens"`
	CachedTokens    []int64          `json:"cached_tokens"`
	TimeRange       monitorTimeRange `json:"time_range"`
}

// GetMonitorKpi returns aggregated KPI metrics from usage records.
func (h *Handler) GetMonitorKpi(c *gin.Context) {
	start, end, err := parseMonitorTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	status, err := parseStatusFilter(firstQuery(c, "status"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	filter := monitorRecordFilter{
		APIKey:      firstQuery(c, "api", "api_key"),
		APIContains: firstQuery(c, "api_filter", "apiFilter", "api_like", "apiLike", "q"),
		Model:       firstQuery(c, "model"),
		Source:      firstQuery(c, "source", "channel"),
		Status:      status,
		Start:       start,
		End:         end,
	}

	var resp monitorKpiResponse
	var minTs, maxTs time.Time

	visitSnapshotRecords(h.usageSnapshot(), func(record monitorRecord) {
		if !filter.matches(record) {
			return
		}
		resp.TotalRequests++
		if record.Failed {
			resp.FailedRequests++
		} else {
			resp.SuccessRequests++
			resp.TotalTokens += record.TotalTokens
			resp.InputTokens += record.InputTokens
			resp.OutputTokens += record.OutputTokens
			resp.ReasoningTokens += record.ReasoningTokens
			resp.CachedTokens += record.CachedTokens
		}
		if minTs.IsZero() || record.Timestamp.Before(minTs) {
			minTs = record.Timestamp
		}
		if maxTs.IsZero() || record.Timestamp.After(maxTs) {
			maxTs = record.Timestamp
		}
	})

	resp.SuccessRate = calcRate(resp.SuccessRequests, resp.TotalRequests)

	if resp.TotalRequests > 0 && !minTs.IsZero() && !maxTs.IsZero() {
		spanMinutes := maxTs.Sub(minTs).Minutes()
		if spanMinutes < 1 {
			spanMinutes = 1
		}
		spanDays := spanMinutes / (60 * 24)
		if spanDays < 1 {
			spanDays = 1
		}
		resp.AvgTpm = math.Round(float64(resp.TotalTokens)/spanMinutes*10) / 10
		resp.AvgRpm = math.Round(float64(resp.TotalRequests)/spanMinutes*10) / 10
		resp.AvgRpd = math.Round(float64(resp.TotalRequests)/spanDays*10) / 10
	}

	resp.TimeRange = monitorTimeRange{Start: start, End: end}
	c.JSON(http.StatusOK, resp)
}

// GetMonitorModelDistribution returns per-model request and token counts.
func (h *Handler) GetMonitorModelDistribution(c *gin.Context) {
	start, end, err := parseMonitorTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	limit, err := parseTopLimit(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	filter := monitorRecordFilter{
		APIKey:      firstQuery(c, "api", "api_key"),
		APIContains: firstQuery(c, "api_filter", "apiFilter", "api_like", "apiLike", "q"),
		Model:       firstQuery(c, "model"),
		Source:      firstQuery(c, "source", "channel"),
		Start:       start,
		End:         end,
	}

	type modelAcc struct {
		Requests int64
		Tokens   int64
	}
	modelMap := make(map[string]*modelAcc)

	visitSnapshotRecords(h.usageSnapshot(), func(record monitorRecord) {
		if !filter.matches(record) {
			return
		}
		acc, ok := modelMap[record.Model]
		if !ok {
			acc = &modelAcc{}
			modelMap[record.Model] = acc
		}
		acc.Requests++
		acc.Tokens += record.TotalTokens
	})

	items := make([]monitorModelDistributionItem, 0, len(modelMap))
	for model, acc := range modelMap {
		items = append(items, monitorModelDistributionItem{
			Model:    model,
			Requests: acc.Requests,
			Tokens:   acc.Tokens,
		})
	}

	sortField := strings.ToLower(firstQuery(c, "sort"))
	if sortField == "tokens" {
		sort.Slice(items, func(i, j int) bool {
			if items[i].Tokens == items[j].Tokens {
				return items[i].Model < items[j].Model
			}
			return items[i].Tokens > items[j].Tokens
		})
	} else {
		sort.Slice(items, func(i, j int) bool {
			if items[i].Requests == items[j].Requests {
				return items[i].Model < items[j].Model
			}
			return items[i].Requests > items[j].Requests
		})
	}

	if len(items) > limit {
		items = items[:limit]
	}

	c.JSON(http.StatusOK, gin.H{
		"items":      items,
		"time_range": monitorTimeRange{Start: start, End: end},
	})
}

// GetMonitorDailyTrend returns per-day request and token aggregates.
func (h *Handler) GetMonitorDailyTrend(c *gin.Context) {
	start, end, err := parseMonitorTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	filter := monitorRecordFilter{
		APIKey:      firstQuery(c, "api", "api_key"),
		APIContains: firstQuery(c, "api_filter", "apiFilter", "api_like", "apiLike", "q"),
		Model:       firstQuery(c, "model"),
		Source:      firstQuery(c, "source", "channel"),
		Start:       start,
		End:         end,
	}

	type dayAcc struct {
		Requests        int64
		SuccessRequests int64
		FailedRequests  int64
		InputTokens     int64
		OutputTokens    int64
		ReasoningTokens int64
		CachedTokens    int64
	}
	dayMap := make(map[string]*dayAcc)

	visitSnapshotRecords(h.usageSnapshot(), func(record monitorRecord) {
		if !filter.matches(record) {
			return
		}
		dateKey := record.Timestamp.Local().Format("2006-01-02")
		acc, ok := dayMap[dateKey]
		if !ok {
			acc = &dayAcc{}
			dayMap[dateKey] = acc
		}
		acc.Requests++
		if record.Failed {
			acc.FailedRequests++
		} else {
			acc.SuccessRequests++
			acc.InputTokens += record.InputTokens
			acc.OutputTokens += record.OutputTokens
			acc.ReasoningTokens += record.ReasoningTokens
			acc.CachedTokens += record.CachedTokens
		}
	})

	items := make([]monitorDailyTrendItem, 0, len(dayMap))
	for date, acc := range dayMap {
		items = append(items, monitorDailyTrendItem{
			Date:            date,
			Requests:        acc.Requests,
			SuccessRequests: acc.SuccessRequests,
			FailedRequests:  acc.FailedRequests,
			InputTokens:     acc.InputTokens,
			OutputTokens:    acc.OutputTokens,
			ReasoningTokens: acc.ReasoningTokens,
			CachedTokens:    acc.CachedTokens,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Date < items[j].Date
	})

	c.JSON(http.StatusOK, gin.H{
		"items":      items,
		"time_range": monitorTimeRange{Start: start, End: end},
	})
}

// GetMonitorHourlyModels returns per-hour per-model request counts for the top N models.
func (h *Handler) GetMonitorHourlyModels(c *gin.Context) {
	start, end, err := parseMonitorTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hours, err := parseHoursParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	limit, err := parseBoundedInt(firstQuery(c, "limit"), 6, 1, 20)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	filter := monitorRecordFilter{
		APIKey:      firstQuery(c, "api", "api_key"),
		APIContains: firstQuery(c, "api_filter", "apiFilter", "api_like", "apiLike", "q"),
		Model:       firstQuery(c, "model"),
		Source:      firstQuery(c, "source", "channel"),
		Start:       start,
		End:         end,
	}

	now := time.Now()
	cutoff := now.Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour)

	// Generate hour slots
	hourSlots := make([]string, 0, hours)
	hourIndex := make(map[string]int, hours)
	for t := cutoff; !t.After(now.Truncate(time.Hour)); t = t.Add(time.Hour) {
		key := t.Local().Format("2006-01-02T15")
		hourIndex[key] = len(hourSlots)
		hourSlots = append(hourSlots, key)
	}
	slotCount := len(hourSlots)

	// Per-hour per-model counts and per-hour success tracking
	type hourModelKey struct {
		hour  string
		model string
	}
	hmCounts := make(map[hourModelKey]int64)
	modelTotals := make(map[string]int64)
	hourSuccess := make([]int64, slotCount)
	hourTotal := make([]int64, slotCount)

	visitSnapshotRecords(h.usageSnapshot(), func(record monitorRecord) {
		if record.Timestamp.Before(cutoff) {
			return
		}
		if !filter.matches(record) {
			return
		}
		hourKey := record.Timestamp.Local().Truncate(time.Hour).Format("2006-01-02T15")
		idx, ok := hourIndex[hourKey]
		if !ok {
			return
		}
		hmCounts[hourModelKey{hour: hourKey, model: record.Model}]++
		modelTotals[record.Model]++
		hourTotal[idx]++
		if !record.Failed {
			hourSuccess[idx]++
		}
	})

	// Find top N models
	type modelCount struct {
		model string
		count int64
	}
	mc := make([]modelCount, 0, len(modelTotals))
	for m, cnt := range modelTotals {
		mc = append(mc, modelCount{model: m, count: cnt})
	}
	sort.Slice(mc, func(i, j int) bool {
		if mc[i].count == mc[j].count {
			return mc[i].model < mc[j].model
		}
		return mc[i].count > mc[j].count
	})
	if len(mc) > limit {
		mc = mc[:limit]
	}

	topModels := make([]string, len(mc))
	modelData := make(map[string][]int64, len(mc))
	for i, m := range mc {
		topModels[i] = m.model
		data := make([]int64, slotCount)
		for si, slot := range hourSlots {
			data[si] = hmCounts[hourModelKey{hour: slot, model: m.model}]
		}
		modelData[m.model] = data
	}

	successRates := make([]float64, slotCount)
	for i := range hourSlots {
		successRates[i] = calcRate(hourSuccess[i], hourTotal[i])
	}

	c.JSON(http.StatusOK, monitorHourlyModelsResponse{
		Hours:        hourSlots,
		Models:       topModels,
		ModelData:    modelData,
		SuccessRates: successRates,
		TimeRange:    monitorTimeRange{Start: start, End: end},
	})
}

// GetMonitorHourlyTokens returns per-hour token breakdowns.
func (h *Handler) GetMonitorHourlyTokens(c *gin.Context) {
	start, end, err := parseMonitorTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hours, err := parseHoursParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	filter := monitorRecordFilter{
		APIKey:      firstQuery(c, "api", "api_key"),
		APIContains: firstQuery(c, "api_filter", "apiFilter", "api_like", "apiLike", "q"),
		Model:       firstQuery(c, "model"),
		Source:      firstQuery(c, "source", "channel"),
		Start:       start,
		End:         end,
	}

	now := time.Now()
	cutoff := now.Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour)

	hourSlots := make([]string, 0, hours)
	hourIndex := make(map[string]int, hours)
	for t := cutoff; !t.After(now.Truncate(time.Hour)); t = t.Add(time.Hour) {
		key := t.Local().Format("2006-01-02T15")
		hourIndex[key] = len(hourSlots)
		hourSlots = append(hourSlots, key)
	}
	slotCount := len(hourSlots)

	totalTokens := make([]int64, slotCount)
	inputTokens := make([]int64, slotCount)
	outputTokens := make([]int64, slotCount)
	reasoningTokens := make([]int64, slotCount)
	cachedTokens := make([]int64, slotCount)

	visitSnapshotRecords(h.usageSnapshot(), func(record monitorRecord) {
		if record.Timestamp.Before(cutoff) {
			return
		}
		if !filter.matches(record) {
			return
		}
		if record.Failed {
			return
		}
		hourKey := record.Timestamp.Local().Truncate(time.Hour).Format("2006-01-02T15")
		idx, ok := hourIndex[hourKey]
		if !ok {
			return
		}
		totalTokens[idx] += record.TotalTokens
		inputTokens[idx] += record.InputTokens
		outputTokens[idx] += record.OutputTokens
		reasoningTokens[idx] += record.ReasoningTokens
		cachedTokens[idx] += record.CachedTokens
	})

	c.JSON(http.StatusOK, monitorHourlyTokensResponse{
		Hours:           hourSlots,
		TotalTokens:     totalTokens,
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		ReasoningTokens: reasoningTokens,
		CachedTokens:    cachedTokens,
		TimeRange:       monitorTimeRange{Start: start, End: end},
	})
}

func parseHoursParam(c *gin.Context) (int, error) {
	return parseBoundedInt(firstQuery(c, "hours"), 24, 1, 168)
}

type monitorValidationError struct {
	msg string
}

func (e *monitorValidationError) Error() string {
	if e == nil || e.msg == "" {
		return "invalid monitor request"
	}
	return e.msg
}

// GetMonitorServiceHealth returns a 7-day health grid with 15-minute granularity.
func (h *Handler) GetMonitorServiceHealth(c *gin.Context) {
	const (
		rows            = 7
		cols            = 96
		totalBlocks     = rows * cols // 672
		blockDurationMs = 900000      // 15 minutes
		blockDuration   = 15 * time.Minute
		windowDuration  = 7 * 24 * time.Hour
	)

	now := time.Now()
	windowStart := now.Add(-windowDuration)

	type healthBlock struct {
		Success int64 `json:"success"`
		Failure int64 `json:"failure"`
	}

	blocks := make([]healthBlock, totalBlocks)
	var totalSuccess, totalFailure int64

	visitSnapshotRecords(h.usageSnapshot(), func(record monitorRecord) {
		if record.Timestamp.Before(windowStart) || record.Timestamp.After(now) {
			return
		}
		idx := int(record.Timestamp.Sub(windowStart) / blockDuration)
		if idx >= totalBlocks {
			idx = totalBlocks - 1
		}
		if record.Failed {
			blocks[idx].Failure++
			totalFailure++
		} else {
			blocks[idx].Success++
			totalSuccess++
		}
	})

	total := totalSuccess + totalFailure
	c.JSON(http.StatusOK, gin.H{
		"rows":              rows,
		"cols":              cols,
		"block_duration_ms": blockDurationMs,
		"blocks":            blocks,
		"total_success":     totalSuccess,
		"total_failure":     totalFailure,
		"success_rate":      calcRate(totalSuccess, total),
	})
}

// GetMonitorKeyStats returns per-source and per-auth-index success/failure stats
// with 10-minute time blocks over a 200-minute sliding window.
func (h *Handler) GetMonitorKeyStats(c *gin.Context) {
	const (
		blockCount      = 20
		blockDurationMs = 600000 // 10 minutes
		blockDuration   = 10 * time.Minute
		windowDuration  = blockCount * blockDuration // 200 minutes
	)

	now := time.Now()
	windowStart := now.Add(-windowDuration)

	type blockStats struct {
		Success int64 `json:"success"`
		Failure int64 `json:"failure"`
	}

	type keyStats struct {
		Success int64        `json:"success"`
		Failure int64        `json:"failure"`
		Blocks  []blockStats `json:"blocks"`
	}

	bySource := make(map[string]*keyStats)
	byAuthIndex := make(map[string]*keyStats)

	ensureEntry := func(m map[string]*keyStats, key string) *keyStats {
		if s, ok := m[key]; ok {
			return s
		}
		s := &keyStats{Blocks: make([]blockStats, blockCount)}
		m[key] = s
		return s
	}

	visitSnapshotRecords(h.usageSnapshot(), func(record monitorRecord) {
		if record.Timestamp.Before(windowStart) || record.Timestamp.After(now) {
			return
		}
		idx := int(record.Timestamp.Sub(windowStart) / blockDuration)
		if idx >= blockCount {
			idx = blockCount - 1
		}

		source := record.Source
		if source == "" {
			source = "unknown"
		}
		srcStats := ensureEntry(bySource, source)

		authIdx := record.AuthIndex
		if authIdx == "" {
			authIdx = "unknown"
		}
		aiStats := ensureEntry(byAuthIndex, authIdx)

		if record.Failed {
			srcStats.Failure++
			srcStats.Blocks[idx].Failure++
			aiStats.Failure++
			aiStats.Blocks[idx].Failure++
		} else {
			srcStats.Success++
			srcStats.Blocks[idx].Success++
			aiStats.Success++
			aiStats.Blocks[idx].Success++
		}
	})

	sourceResp := make(map[string]keyStats, len(bySource))
	for k, v := range bySource {
		sourceResp[k] = *v
	}
	authResp := make(map[string]keyStats, len(byAuthIndex))
	for k, v := range byAuthIndex {
		authResp[k] = *v
	}

	c.JSON(http.StatusOK, gin.H{
		"by_source":     sourceResp,
		"by_auth_index": authResp,
		"block_config": gin.H{
			"count":           blockCount,
			"duration_ms":     blockDurationMs,
			"window_start_ms": windowStart.UnixMilli(),
		},
	})
}
