package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	monitorDefaultRecentLimit = 12
)

var (
	// ErrMonitorQueryUnsupported means current persistence backend does not support monitor SQL queries.
	ErrMonitorQueryUnsupported = errors.New("usage: monitor queries are unsupported by current store")
)

// MonitorQueryFilter describes shared query filters for monitor APIs.
type MonitorQueryFilter struct {
	APIKey      string
	APIContains string
	Model       string
	Source      string
	Status      string
	Start       *time.Time
	End         *time.Time
}

// MonitorRecentRequest stores the request status and time for trend bars.
type MonitorRecentRequest struct {
	Failed    bool
	Timestamp time.Time
}

// MonitorFilterOptions contains dynamic filter candidates from current query range.
type MonitorFilterOptions struct {
	APIs    []string
	Models  []string
	Sources []string
}

// MonitorRequestLog represents a single request log row.
type MonitorRequestLog struct {
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

// MonitorRequestGroupStats represents per-channel+model counters for request logs.
type MonitorRequestGroupStats struct {
	Total   int64
	Success int64
	Recent  []MonitorRecentRequest
}

// MonitorRequestLogsResult is the SQL-backed result for monitor request logs.
type MonitorRequestLogsResult struct {
	Items      []MonitorRequestLog
	Total      int64
	Page       int
	PageSize   int
	Filters    MonitorFilterOptions
	GroupStats map[string]MonitorRequestGroupStats
}

// MonitorModelStats is the model-level aggregate used by channel/failure analysis.
type MonitorModelStats struct {
	Model         string
	Requests      int64
	Success       int64
	Failed        int64
	LastRequestAt *time.Time
	Recent        []MonitorRecentRequest
}

// MonitorChannelStats is the source-level aggregate used by channel stats endpoint.
type MonitorChannelStats struct {
	Source          string
	TotalRequests   int64
	SuccessRequests int64
	FailedRequests  int64
	LastRequestAt   *time.Time
	Recent          []MonitorRecentRequest
	Models          []MonitorModelStats
}

// MonitorChannelStatsResult is the SQL-backed result for channel stats endpoint.
type MonitorChannelStatsResult struct {
	Items   []MonitorChannelStats
	Filters MonitorFilterOptions
}

// MonitorFailureStats is the source-level aggregate used by failure analysis endpoint.
type MonitorFailureStats struct {
	Source       string
	FailedCount  int64
	LastFailedAt *time.Time
	Models       []MonitorModelStats
}

// MonitorFailureStatsResult is the SQL-backed result for failure analysis endpoint.
type MonitorFailureStatsResult struct {
	Items   []MonitorFailureStats
	Filters MonitorFilterOptions
}

type monitorQueryableStore interface {
	QueryMonitorRequestLogs(ctx context.Context, filter MonitorQueryFilter, page, pageSize, recentLimit int) (MonitorRequestLogsResult, error)
	QueryMonitorChannelStats(ctx context.Context, filter MonitorQueryFilter, limit, recentLimit int) (MonitorChannelStatsResult, error)
	QueryMonitorFailureStats(ctx context.Context, filter MonitorQueryFilter, limit, recentLimit int) (MonitorFailureStatsResult, error)
	QueryMonitorRequestDetails(ctx context.Context, center *time.Time, windowSec int, method, path string, limit int) ([]MonitorRequestDetail, error)
	QueryMonitorKpi(ctx context.Context, filter MonitorQueryFilter) (MonitorKpiResult, error)
	QueryMonitorModelDistribution(ctx context.Context, filter MonitorQueryFilter, limit int, sortByTokens bool) ([]MonitorModelDistItem, error)
	QueryMonitorDailyTrend(ctx context.Context, filter MonitorQueryFilter) ([]MonitorDailyTrendItem, error)
	QueryMonitorHourlySlots(ctx context.Context, filter MonitorQueryFilter, cutoffUnix, nowUnix int64, slotSeconds int) ([]MonitorHourlySlot, error)
	QueryMonitorHourlyTokenSlots(ctx context.Context, filter MonitorQueryFilter, cutoffUnix, nowUnix int64, slotSeconds int) ([]MonitorHourlyTokenSlot, error)
	QueryMonitorHealthBlocks(ctx context.Context, windowStartUnix, windowEndUnix int64, blockSeconds int) ([]MonitorHealthBlock, error)
	QueryMonitorKeyStatsBlocks(ctx context.Context, windowStartUnix, windowEndUnix int64, blockSeconds int) ([]MonitorKeyStatsRow, error)
}

// MonitorRequestDetail represents a single request detail row for the request-details endpoint.
type MonitorRequestDetail struct {
	Timestamp time.Time
	Method    string
	Path      string
	Model     string
	Source    string
	AuthIndex string
	Failed    bool
}

// MonitorKpiResult is the SQL-backed result for KPI endpoint.
type MonitorKpiResult struct {
	TotalRequests   int64
	SuccessRequests int64
	FailedRequests  int64
	TotalTokens     int64
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	MinTimestamp    *time.Time
	MaxTimestamp    *time.Time
}

// MonitorModelDistItem represents a single model in the distribution.
type MonitorModelDistItem struct {
	Model    string
	Requests int64
	Tokens   int64
}

// MonitorDailyTrendItem represents a single day in the daily trend.
type MonitorDailyTrendItem struct {
	Date            string
	Requests        int64
	SuccessRequests int64
	FailedRequests  int64
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
}

// MonitorHourlySlot represents per-slot per-model counts for hourly-models.
type MonitorHourlySlot struct {
	SlotIndex int
	Model     string
	Total     int64
	Success   int64
}

// MonitorHourlyTokenSlot represents per-slot token breakdowns for hourly-tokens.
type MonitorHourlyTokenSlot struct {
	SlotIndex       int
	TotalTokens     int64
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
}

// MonitorHealthBlock represents a single time block for service health.
type MonitorHealthBlock struct {
	BlockIndex int
	Success    int64
	Failure    int64
}

// MonitorKeyStatsRow represents a time block + source/auth aggregation for key stats.
type MonitorKeyStatsRow struct {
	BlockIndex int
	Source     string
	AuthIndex  string
	Success    int64
	Failure    int64
}

// MonitorGroupKey returns the stable grouping key used by request log aggregates.
func MonitorGroupKey(source, model string) string {
	return normalizeMonitorSource(source) + "|||" + model
}

// QueryMonitorRequestLogs queries request logs directly from persistence layer.
func (p *DatabasePlugin) QueryMonitorRequestLogs(ctx context.Context, filter MonitorQueryFilter, page, pageSize, recentLimit int) (MonitorRequestLogsResult, error) {
	queryable, err := p.monitorQueryableStore()
	if err != nil {
		return MonitorRequestLogsResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return queryable.QueryMonitorRequestLogs(ctx, normalizeMonitorFilter(filter), page, pageSize, recentLimit)
}

// QueryMonitorChannelStats queries channel aggregates directly from persistence layer.
func (p *DatabasePlugin) QueryMonitorChannelStats(ctx context.Context, filter MonitorQueryFilter, limit, recentLimit int) (MonitorChannelStatsResult, error) {
	queryable, err := p.monitorQueryableStore()
	if err != nil {
		return MonitorChannelStatsResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return queryable.QueryMonitorChannelStats(ctx, normalizeMonitorFilter(filter), limit, recentLimit)
}

// QueryMonitorFailureStats queries failure aggregates directly from persistence layer.
func (p *DatabasePlugin) QueryMonitorFailureStats(ctx context.Context, filter MonitorQueryFilter, limit, recentLimit int) (MonitorFailureStatsResult, error) {
	queryable, err := p.monitorQueryableStore()
	if err != nil {
		return MonitorFailureStatsResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return queryable.QueryMonitorFailureStats(ctx, normalizeMonitorFilter(filter), limit, recentLimit)
}

// QueryMonitorRequestDetails queries request details directly from persistence layer.
func (p *DatabasePlugin) QueryMonitorRequestDetails(ctx context.Context, center *time.Time, windowSec int, method, path string, limit int) ([]MonitorRequestDetail, error) {
	queryable, err := p.monitorQueryableStore()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return queryable.QueryMonitorRequestDetails(ctx, center, windowSec, method, path, limit)
}

func (p *DatabasePlugin) monitorQueryableStore() (monitorQueryableStore, error) {
	if p == nil || p.store == nil {
		return nil, ErrMonitorQueryUnsupported
	}
	queryable, ok := p.store.(monitorQueryableStore)
	if !ok {
		return nil, ErrMonitorQueryUnsupported
	}
	return queryable, nil
}

// QueryMonitorKpi queries KPI aggregates directly from persistence layer.
func (p *DatabasePlugin) QueryMonitorKpi(ctx context.Context, filter MonitorQueryFilter) (MonitorKpiResult, error) {
	queryable, err := p.monitorQueryableStore()
	if err != nil {
		return MonitorKpiResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return queryable.QueryMonitorKpi(ctx, normalizeMonitorFilter(filter))
}

// QueryMonitorModelDistribution queries per-model aggregates directly from persistence layer.
func (p *DatabasePlugin) QueryMonitorModelDistribution(ctx context.Context, filter MonitorQueryFilter, limit int, sortByTokens bool) ([]MonitorModelDistItem, error) {
	queryable, err := p.monitorQueryableStore()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return queryable.QueryMonitorModelDistribution(ctx, normalizeMonitorFilter(filter), limit, sortByTokens)
}

// QueryMonitorDailyTrend queries per-day aggregates directly from persistence layer.
func (p *DatabasePlugin) QueryMonitorDailyTrend(ctx context.Context, filter MonitorQueryFilter) ([]MonitorDailyTrendItem, error) {
	queryable, err := p.monitorQueryableStore()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return queryable.QueryMonitorDailyTrend(ctx, normalizeMonitorFilter(filter))
}

// QueryMonitorHourlySlots queries per-slot per-model counts directly from persistence layer.
func (p *DatabasePlugin) QueryMonitorHourlySlots(ctx context.Context, filter MonitorQueryFilter, cutoffUnix, nowUnix int64, slotSeconds int) ([]MonitorHourlySlot, error) {
	queryable, err := p.monitorQueryableStore()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return queryable.QueryMonitorHourlySlots(ctx, normalizeMonitorFilter(filter), cutoffUnix, nowUnix, slotSeconds)
}

// QueryMonitorHourlyTokenSlots queries per-slot token breakdowns directly from persistence layer.
func (p *DatabasePlugin) QueryMonitorHourlyTokenSlots(ctx context.Context, filter MonitorQueryFilter, cutoffUnix, nowUnix int64, slotSeconds int) ([]MonitorHourlyTokenSlot, error) {
	queryable, err := p.monitorQueryableStore()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return queryable.QueryMonitorHourlyTokenSlots(ctx, normalizeMonitorFilter(filter), cutoffUnix, nowUnix, slotSeconds)
}

// QueryMonitorHealthBlocks queries time-block health aggregates directly from persistence layer.
func (p *DatabasePlugin) QueryMonitorHealthBlocks(ctx context.Context, windowStartUnix, windowEndUnix int64, blockSeconds int) ([]MonitorHealthBlock, error) {
	queryable, err := p.monitorQueryableStore()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return queryable.QueryMonitorHealthBlocks(ctx, windowStartUnix, windowEndUnix, blockSeconds)
}

// QueryMonitorKeyStatsBlocks queries per-source/auth time-block aggregates directly from persistence layer.
func (p *DatabasePlugin) QueryMonitorKeyStatsBlocks(ctx context.Context, windowStartUnix, windowEndUnix int64, blockSeconds int) ([]MonitorKeyStatsRow, error) {
	queryable, err := p.monitorQueryableStore()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return queryable.QueryMonitorKeyStatsBlocks(ctx, windowStartUnix, windowEndUnix, blockSeconds)
}

func (s *mirrorUsageStore) QueryMonitorRequestLogs(ctx context.Context, filter MonitorQueryFilter, page, pageSize, recentLimit int) (MonitorRequestLogsResult, error) {
	if s == nil || s.local == nil {
		return MonitorRequestLogsResult{}, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.QueryMonitorRequestLogs(ctx, filter, page, pageSize, recentLimit)
}

func (s *mirrorUsageStore) QueryMonitorChannelStats(ctx context.Context, filter MonitorQueryFilter, limit, recentLimit int) (MonitorChannelStatsResult, error) {
	if s == nil || s.local == nil {
		return MonitorChannelStatsResult{}, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.QueryMonitorChannelStats(ctx, filter, limit, recentLimit)
}

func (s *mirrorUsageStore) QueryMonitorFailureStats(ctx context.Context, filter MonitorQueryFilter, limit, recentLimit int) (MonitorFailureStatsResult, error) {
	if s == nil || s.local == nil {
		return MonitorFailureStatsResult{}, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.QueryMonitorFailureStats(ctx, filter, limit, recentLimit)
}

func (s *mirrorUsageStore) QueryMonitorRequestDetails(ctx context.Context, center *time.Time, windowSec int, method, path string, limit int) ([]MonitorRequestDetail, error) {
	if s == nil || s.local == nil {
		return nil, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.QueryMonitorRequestDetails(ctx, center, windowSec, method, path, limit)
}

func (s *mirrorUsageStore) QueryMonitorKpi(ctx context.Context, filter MonitorQueryFilter) (MonitorKpiResult, error) {
	if s == nil || s.local == nil {
		return MonitorKpiResult{}, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.QueryMonitorKpi(ctx, filter)
}

func (s *mirrorUsageStore) QueryMonitorModelDistribution(ctx context.Context, filter MonitorQueryFilter, limit int, sortByTokens bool) ([]MonitorModelDistItem, error) {
	if s == nil || s.local == nil {
		return nil, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.QueryMonitorModelDistribution(ctx, filter, limit, sortByTokens)
}

func (s *mirrorUsageStore) QueryMonitorDailyTrend(ctx context.Context, filter MonitorQueryFilter) ([]MonitorDailyTrendItem, error) {
	if s == nil || s.local == nil {
		return nil, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.QueryMonitorDailyTrend(ctx, filter)
}

func (s *mirrorUsageStore) QueryMonitorHourlySlots(ctx context.Context, filter MonitorQueryFilter, cutoffUnix, nowUnix int64, slotSeconds int) ([]MonitorHourlySlot, error) {
	if s == nil || s.local == nil {
		return nil, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.QueryMonitorHourlySlots(ctx, filter, cutoffUnix, nowUnix, slotSeconds)
}

func (s *mirrorUsageStore) QueryMonitorHourlyTokenSlots(ctx context.Context, filter MonitorQueryFilter, cutoffUnix, nowUnix int64, slotSeconds int) ([]MonitorHourlyTokenSlot, error) {
	if s == nil || s.local == nil {
		return nil, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.QueryMonitorHourlyTokenSlots(ctx, filter, cutoffUnix, nowUnix, slotSeconds)
}

func (s *mirrorUsageStore) QueryMonitorHealthBlocks(ctx context.Context, windowStartUnix, windowEndUnix int64, blockSeconds int) ([]MonitorHealthBlock, error) {
	if s == nil || s.local == nil {
		return nil, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.QueryMonitorHealthBlocks(ctx, windowStartUnix, windowEndUnix, blockSeconds)
}

func (s *mirrorUsageStore) QueryMonitorKeyStatsBlocks(ctx context.Context, windowStartUnix, windowEndUnix int64, blockSeconds int) ([]MonitorKeyStatsRow, error) {
	if s == nil || s.local == nil {
		return nil, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.QueryMonitorKeyStatsBlocks(ctx, windowStartUnix, windowEndUnix, blockSeconds)
}

func (s *sqliteUsageStore) QueryMonitorRequestLogs(ctx context.Context, filter MonitorQueryFilter, page, pageSize, recentLimit int) (MonitorRequestLogsResult, error) {
	if s == nil || s.db == nil {
		return MonitorRequestLogsResult{}, fmt.Errorf("usage store: sqlite store not initialized")
	}
	page = clampInt(page, 1, 1_000_000, 1)
	pageSize = clampInt(pageSize, 1, 200, 20)
	recentLimit = clampInt(recentLimit, 1, 100, monitorDefaultRecentLimit)

	whereClause, args := buildSQLiteMonitorWhere(filter, true)
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM usage_records WHERE %s", whereClause)

	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return MonitorRequestLogsResult{}, fmt.Errorf("usage store: query monitor logs total: %w", err)
	}

	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if totalPages > 0 && page > totalPages {
		page = totalPages
	}
	offset := (page - 1) * pageSize

	query := fmt.Sprintf(`
		SELECT api_key, model, COALESCE(NULLIF(source, ''), 'unknown'), auth_index,
			failed, requested_at, input_tokens, output_tokens, reasoning_tokens,
			cached_tokens, total_tokens
		FROM usage_records
		WHERE %s
		ORDER BY requested_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, whereClause)
	queryArgs := append(copyArgs(args), pageSize, offset)

	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return MonitorRequestLogsResult{}, fmt.Errorf("usage store: query monitor request logs: %w", err)
	}
	defer rows.Close()

	items := make([]MonitorRequestLog, 0, pageSize)
	groups := make(map[string]monitorGroupEntry)
	for rows.Next() {
		var (
			item      MonitorRequestLog
			failed    int
			unixTime  int64
			rawSource string
		)
		if err = rows.Scan(
			&item.APIKey,
			&item.Model,
			&rawSource,
			&item.AuthIndex,
			&failed,
			&unixTime,
			&item.InputTokens,
			&item.OutputTokens,
			&item.ReasoningTokens,
			&item.CachedTokens,
			&item.TotalTokens,
		); err != nil {
			return MonitorRequestLogsResult{}, fmt.Errorf("usage store: scan monitor request logs: %w", err)
		}
		item.Failed = failed != 0
		item.Timestamp = time.Unix(unixTime, 0)
		item.Source = normalizeMonitorSource(rawSource)
		items = append(items, item)

		key := MonitorGroupKey(item.Source, item.Model)
		if _, exists := groups[key]; !exists {
			groups[key] = monitorGroupEntry{Source: item.Source, Model: item.Model}
		}
	}
	if err = rows.Err(); err != nil {
		return MonitorRequestLogsResult{}, fmt.Errorf("usage store: iterate monitor request logs: %w", err)
	}

	groupStats := make(map[string]MonitorRequestGroupStats, len(groups))
	if len(groups) > 0 {
		groupWhereClause, groupWhereArgs := buildGroupWhereClause(groups)
		if groupWhereClause == "" {
			return MonitorRequestLogsResult{}, fmt.Errorf("usage store: build group where clause: empty")
		}

		// Batch query: aggregate counts per (source, model) group
		batchCountQuery := fmt.Sprintf(`
			SELECT COALESCE(NULLIF(source, ''), 'unknown'), model,
				COUNT(*), COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END), 0)
			FROM usage_records
			WHERE %s AND (%s)
			GROUP BY COALESCE(NULLIF(source, ''), 'unknown'), model
		`, whereClause, groupWhereClause)
		countArgs := append(copyArgs(args), groupWhereArgs...)
		countRows, countErr := s.db.QueryContext(ctx, batchCountQuery, countArgs...)
		if countErr != nil {
			return MonitorRequestLogsResult{}, fmt.Errorf("usage store: batch group stats count: %w", countErr)
		}
		defer countRows.Close()
		for countRows.Next() {
			var src, mdl string
			var total2, success int64
			if countErr = countRows.Scan(&src, &mdl, &total2, &success); countErr != nil {
				return MonitorRequestLogsResult{}, fmt.Errorf("usage store: scan batch group stats: %w", countErr)
			}
			key := MonitorGroupKey(normalizeMonitorSource(src), mdl)
			if _, exists := groups[key]; exists {
				groupStats[key] = MonitorRequestGroupStats{Total: total2, Success: success}
			}
		}
		if countErr = countRows.Err(); countErr != nil {
			return MonitorRequestLogsResult{}, fmt.Errorf("usage store: iterate batch group stats: %w", countErr)
		}

		// Batch query: recent requests per (source, model) group using ROW_NUMBER
		batchRecentQuery := fmt.Sprintf(`
			SELECT source_key, model, failed, requested_at FROM (
				SELECT COALESCE(NULLIF(source, ''), 'unknown') AS source_key, model, failed, requested_at,
					ROW_NUMBER() OVER(PARTITION BY COALESCE(NULLIF(source, ''), 'unknown'), model ORDER BY requested_at DESC, id DESC) AS rn
				FROM usage_records
				WHERE %s AND (%s)
			) WHERE rn <= ?
		`, whereClause, groupWhereClause)
		recentArgs := append(copyArgs(args), groupWhereArgs...)
		recentArgs = append(recentArgs, recentLimit)
		recentRows, recentErr := s.db.QueryContext(ctx, batchRecentQuery, recentArgs...)
		if recentErr != nil {
			return MonitorRequestLogsResult{}, fmt.Errorf("usage store: batch group recent: %w", recentErr)
		}
		defer recentRows.Close()
		recentMap := make(map[string][]MonitorRecentRequest, len(groups))
		for recentRows.Next() {
			var src, mdl string
			var failed int
			var ts int64
			if recentErr = recentRows.Scan(&src, &mdl, &failed, &ts); recentErr != nil {
				return MonitorRequestLogsResult{}, fmt.Errorf("usage store: scan batch group recent: %w", recentErr)
			}
			key := MonitorGroupKey(normalizeMonitorSource(src), mdl)
			if _, exists := groups[key]; exists {
				recentMap[key] = append(recentMap[key], MonitorRecentRequest{
					Failed: failed != 0, Timestamp: time.Unix(ts, 0),
				})
			}
		}
		if recentErr = recentRows.Err(); recentErr != nil {
			return MonitorRequestLogsResult{}, fmt.Errorf("usage store: iterate batch group recent: %w", recentErr)
		}
		for key, recent := range recentMap {
			reverseRecentRequests(recent)
			if gs, ok := groupStats[key]; ok {
				gs.Recent = recent
				groupStats[key] = gs
			}
		}
	}

	filters, err := s.queryMonitorFilterOptions(ctx, filter, true, true)
	if err != nil {
		return MonitorRequestLogsResult{}, err
	}

	return MonitorRequestLogsResult{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		Filters:    filters,
		GroupStats: groupStats,
	}, nil
}

func (s *sqliteUsageStore) QueryMonitorChannelStats(ctx context.Context, filter MonitorQueryFilter, limit, recentLimit int) (MonitorChannelStatsResult, error) {
	if s == nil || s.db == nil {
		return MonitorChannelStatsResult{}, fmt.Errorf("usage store: sqlite store not initialized")
	}
	limit = clampInt(limit, 1, 100, 10)
	recentLimit = clampInt(recentLimit, 1, 100, monitorDefaultRecentLimit)

	baseFilter := filter
	baseFilter.Model = ""
	baseFilter.Status = ""

	whereClause, args := buildSQLiteMonitorWhere(baseFilter, false)

	filters, err := s.queryMonitorFilterOptions(ctx, baseFilter, false, false)
	if err != nil {
		return MonitorChannelStatsResult{}, err
	}

	channelMap, err := s.queryChannelAggregates(ctx, whereClause, args)
	if err != nil {
		return MonitorChannelStatsResult{}, err
	}
	if len(channelMap) == 0 {
		return MonitorChannelStatsResult{Items: []MonitorChannelStats{}, Filters: filters}, nil
	}

	if err = s.attachChannelModels(ctx, whereClause, args, channelMap); err != nil {
		return MonitorChannelStatsResult{}, err
	}

	items := make([]MonitorChannelStats, 0, len(channelMap))
	modelFilter := strings.TrimSpace(filter.Model)
	statusFilter := strings.TrimSpace(filter.Status)

	for _, item := range channelMap {
		if modelFilter != "" && !containsModel(item.Models, modelFilter) {
			continue
		}
		switch statusFilter {
		case "success":
			if item.FailedRequests > 0 {
				continue
			}
		case "failed":
			if item.FailedRequests == 0 {
				continue
			}
		}

		sort.Slice(item.Models, func(i, j int) bool {
			if item.Models[i].Requests == item.Models[j].Requests {
				return item.Models[i].Model < item.Models[j].Model
			}
			return item.Models[i].Requests > item.Models[j].Requests
		})
		items = append(items, *item)
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

	// Batch: channel-level recent requests using ROW_NUMBER
	if len(items) > 0 {
		selectedSources := make([]string, 0, len(items))
		for _, item := range items {
			selectedSources = append(selectedSources, item.Source)
		}
		sourceWhereClause, sourceWhereArgs := buildSourceWhereClause(selectedSources)
		if sourceWhereClause == "" {
			return MonitorChannelStatsResult{}, fmt.Errorf("usage store: build channel source where clause: empty")
		}

		channelRecentQuery := fmt.Sprintf(`
			SELECT source_key, failed, requested_at FROM (
				SELECT COALESCE(NULLIF(source, ''), 'unknown') AS source_key, failed, requested_at,
					ROW_NUMBER() OVER(PARTITION BY COALESCE(NULLIF(source, ''), 'unknown') ORDER BY requested_at DESC, id DESC) AS rn
				FROM usage_records
				WHERE %s AND (%s)
			) WHERE rn <= ?
		`, whereClause, sourceWhereClause)
		channelRecentArgs := append(copyArgs(args), sourceWhereArgs...)
		channelRecentArgs = append(channelRecentArgs, recentLimit)
		crRows, crErr := s.db.QueryContext(ctx, channelRecentQuery, channelRecentArgs...)
		if crErr != nil {
			return MonitorChannelStatsResult{}, fmt.Errorf("usage store: batch channel recent: %w", crErr)
		}
		defer crRows.Close()
		channelRecentMap := make(map[string][]MonitorRecentRequest)
		for crRows.Next() {
			var src string
			var failed int
			var ts int64
			if crErr = crRows.Scan(&src, &failed, &ts); crErr != nil {
				return MonitorChannelStatsResult{}, fmt.Errorf("usage store: scan batch channel recent: %w", crErr)
			}
			normalized := normalizeMonitorSource(src)
			channelRecentMap[normalized] = append(channelRecentMap[normalized], MonitorRecentRequest{
				Failed: failed != 0, Timestamp: time.Unix(ts, 0),
			})
		}
		if crErr = crRows.Err(); crErr != nil {
			return MonitorChannelStatsResult{}, fmt.Errorf("usage store: iterate batch channel recent: %w", crErr)
		}
		for i := range items {
			if recent, ok := channelRecentMap[items[i].Source]; ok {
				reverseRecentRequests(recent)
				items[i].Recent = recent
			}
		}

		// Batch: model-level recent requests using ROW_NUMBER
		modelRecentQuery := fmt.Sprintf(`
			SELECT source_key, model, failed, requested_at FROM (
				SELECT COALESCE(NULLIF(source, ''), 'unknown') AS source_key, model, failed, requested_at,
					ROW_NUMBER() OVER(PARTITION BY COALESCE(NULLIF(source, ''), 'unknown'), model ORDER BY requested_at DESC, id DESC) AS rn
				FROM usage_records
				WHERE %s AND (%s)
			) WHERE rn <= ?
		`, whereClause, sourceWhereClause)
		modelRecentArgs := append(copyArgs(args), sourceWhereArgs...)
		modelRecentArgs = append(modelRecentArgs, recentLimit)
		mrRows, mrErr := s.db.QueryContext(ctx, modelRecentQuery, modelRecentArgs...)
		if mrErr != nil {
			return MonitorChannelStatsResult{}, fmt.Errorf("usage store: batch model recent: %w", mrErr)
		}
		defer mrRows.Close()
		type sourceModelKey struct{ source, model string }
		modelRecentMap := make(map[sourceModelKey][]MonitorRecentRequest)
		for mrRows.Next() {
			var src, mdl string
			var failed int
			var ts int64
			if mrErr = mrRows.Scan(&src, &mdl, &failed, &ts); mrErr != nil {
				return MonitorChannelStatsResult{}, fmt.Errorf("usage store: scan batch model recent: %w", mrErr)
			}
			key := sourceModelKey{source: normalizeMonitorSource(src), model: mdl}
			modelRecentMap[key] = append(modelRecentMap[key], MonitorRecentRequest{
				Failed: failed != 0, Timestamp: time.Unix(ts, 0),
			})
		}
		if mrErr = mrRows.Err(); mrErr != nil {
			return MonitorChannelStatsResult{}, fmt.Errorf("usage store: iterate batch model recent: %w", mrErr)
		}
		for i := range items {
			for j := range items[i].Models {
				key := sourceModelKey{source: items[i].Source, model: items[i].Models[j].Model}
				if recent, ok := modelRecentMap[key]; ok {
					reverseRecentRequests(recent)
					items[i].Models[j].Recent = recent
				}
			}
		}
	}

	return MonitorChannelStatsResult{Items: items, Filters: filters}, nil
}

func (s *sqliteUsageStore) QueryMonitorFailureStats(ctx context.Context, filter MonitorQueryFilter, limit, recentLimit int) (MonitorFailureStatsResult, error) {
	if s == nil || s.db == nil {
		return MonitorFailureStatsResult{}, fmt.Errorf("usage store: sqlite store not initialized")
	}
	limit = clampInt(limit, 1, 100, 10)
	recentLimit = clampInt(recentLimit, 1, 100, monitorDefaultRecentLimit)

	baseFilter := filter
	baseFilter.Model = ""
	baseFilter.Status = ""
	whereClause, args := buildSQLiteMonitorWhere(baseFilter, false)

	failedSources, err := s.queryFailedSources(ctx, whereClause, args)
	if err != nil {
		return MonitorFailureStatsResult{}, err
	}
	if len(failedSources) == 0 {
		return MonitorFailureStatsResult{
			Items:   []MonitorFailureStats{},
			Filters: MonitorFilterOptions{Models: []string{}, Sources: []string{}},
		}, nil
	}

	inClause, inArgs := toInClause(failedSources)
	failedWhere := whereClause + " AND COALESCE(NULLIF(source, ''), 'unknown') IN (" + inClause + ")"
	failedArgs := append(copyArgs(args), inArgs...)

	itemsMap, err := s.queryFailureAggregates(ctx, failedWhere, failedArgs)
	if err != nil {
		return MonitorFailureStatsResult{}, err
	}
	if err = s.attachFailureModels(ctx, failedWhere, failedArgs, itemsMap); err != nil {
		return MonitorFailureStatsResult{}, err
	}

	modelFilter := strings.TrimSpace(filter.Model)
	items := make([]MonitorFailureStats, 0, len(itemsMap))
	modelSet := make(map[string]struct{})
	sourceSet := make(map[string]struct{})

	for _, item := range itemsMap {
		sourceSet[item.Source] = struct{}{}
		for _, model := range item.Models {
			if strings.TrimSpace(model.Model) != "" {
				modelSet[model.Model] = struct{}{}
			}
		}

		if modelFilter != "" && !containsModel(item.Models, modelFilter) {
			continue
		}

		sort.Slice(item.Models, func(i, j int) bool {
			if item.Models[i].Failed == item.Models[j].Failed {
				if item.Models[i].Requests == item.Models[j].Requests {
					return item.Models[i].Model < item.Models[j].Model
				}
				return item.Models[i].Requests > item.Models[j].Requests
			}
			return item.Models[i].Failed > item.Models[j].Failed
		})

		items = append(items, *item)
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

	if len(items) > 0 {
		selectedSources := make([]string, 0, len(items))
		for _, item := range items {
			selectedSources = append(selectedSources, item.Source)
		}
		sourceWhereClause, sourceWhereArgs := buildSourceWhereClause(selectedSources)
		if sourceWhereClause == "" {
			return MonitorFailureStatsResult{}, fmt.Errorf("usage store: build failure source where clause: empty")
		}

		failureRecentQuery := fmt.Sprintf(`
			SELECT source_key, model, failed, requested_at FROM (
				SELECT COALESCE(NULLIF(source, ''), 'unknown') AS source_key, model, failed, requested_at,
					ROW_NUMBER() OVER(PARTITION BY COALESCE(NULLIF(source, ''), 'unknown'), model ORDER BY requested_at DESC, id DESC) AS rn
				FROM usage_records
				WHERE %s AND (%s)
			) WHERE rn <= ?
		`, failedWhere, sourceWhereClause)
		frArgs := append(copyArgs(failedArgs), sourceWhereArgs...)
		frArgs = append(frArgs, recentLimit)
		frRows, frErr := s.db.QueryContext(ctx, failureRecentQuery, frArgs...)
		if frErr != nil {
			return MonitorFailureStatsResult{}, fmt.Errorf("usage store: batch failure recent: %w", frErr)
		}
		defer frRows.Close()

		type sourceModelKey struct{ source, model string }
		failureRecentMap := make(map[sourceModelKey][]MonitorRecentRequest)
		for frRows.Next() {
			var src, mdl string
			var failed int
			var ts int64
			if frErr = frRows.Scan(&src, &mdl, &failed, &ts); frErr != nil {
				return MonitorFailureStatsResult{}, fmt.Errorf("usage store: scan batch failure recent: %w", frErr)
			}
			key := sourceModelKey{source: normalizeMonitorSource(src), model: mdl}
			failureRecentMap[key] = append(failureRecentMap[key], MonitorRecentRequest{
				Failed: failed != 0, Timestamp: time.Unix(ts, 0),
			})
		}
		if frErr = frRows.Err(); frErr != nil {
			return MonitorFailureStatsResult{}, fmt.Errorf("usage store: iterate batch failure recent: %w", frErr)
		}
		for key, recent := range failureRecentMap {
			reverseRecentRequests(recent)
			failureRecentMap[key] = recent
		}
		for i := range items {
			for j := range items[i].Models {
				key := sourceModelKey{source: items[i].Source, model: items[i].Models[j].Model}
				if recent, ok := failureRecentMap[key]; ok {
					items[i].Models[j].Recent = recent
				}
			}
		}
	}

	return MonitorFailureStatsResult{
		Items: items,
		Filters: MonitorFilterOptions{
			Models:  sortedSet(modelSet),
			Sources: sortedSet(sourceSet),
		},
	}, nil
}

type monitorGroupEntry struct {
	Source string
	Model  string
}

func buildGroupWhereClause(groups map[string]monitorGroupEntry) (string, []any) {
	if len(groups) == 0 {
		return "", nil
	}
	clauses := make([]string, 0, len(groups))
	args := make([]any, 0, len(groups)*2)
	for _, group := range groups {
		source := normalizeMonitorSource(group.Source)
		if source == "unknown" {
			clauses = append(clauses, "((source IS NULL OR source = '') AND model = ?)")
			args = append(args, group.Model)
			continue
		}
		clauses = append(clauses, "(source = ? AND model = ?)")
		args = append(args, source, group.Model)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return strings.Join(clauses, " OR "), args
}

func buildSourceWhereClause(sources []string) (string, []any) {
	if len(sources) == 0 {
		return "", nil
	}
	sourceSet := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		normalized := normalizeMonitorSource(source)
		sourceSet[normalized] = struct{}{}
	}
	clauses := make([]string, 0, len(sourceSet))
	args := make([]any, 0, len(sourceSet))
	for source := range sourceSet {
		if source == "unknown" {
			clauses = append(clauses, "(source IS NULL OR source = '')")
			continue
		}
		clauses = append(clauses, "source = ?")
		args = append(args, source)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return strings.Join(clauses, " OR "), args
}

func (s *sqliteUsageStore) queryChannelAggregates(ctx context.Context, whereClause string, args []any) (map[string]*MonitorChannelStats, error) {
	query := fmt.Sprintf(`
		SELECT COALESCE(NULLIF(source, ''), 'unknown') AS source_key,
			COUNT(*),
			COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END), 0),
			MAX(requested_at)
		FROM usage_records
		WHERE %s
		GROUP BY source_key
	`, whereClause)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usage store: query monitor channel aggregates: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*MonitorChannelStats)
	for rows.Next() {
		var (
			source   string
			item     MonitorChannelStats
			lastUnix sql.NullInt64
		)
		if err = rows.Scan(&source, &item.TotalRequests, &item.SuccessRequests, &item.FailedRequests, &lastUnix); err != nil {
			return nil, fmt.Errorf("usage store: scan monitor channel aggregate: %w", err)
		}
		item.Source = normalizeMonitorSource(source)
		item.LastRequestAt = nullUnixPointer(lastUnix)
		item.Models = []MonitorModelStats{}
		item.Recent = []MonitorRecentRequest{}
		result[item.Source] = &item
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usage store: iterate monitor channel aggregates: %w", err)
	}
	return result, nil
}

func (s *sqliteUsageStore) attachChannelModels(ctx context.Context, whereClause string, args []any, channelMap map[string]*MonitorChannelStats) error {
	query := fmt.Sprintf(`
		SELECT COALESCE(NULLIF(source, ''), 'unknown') AS source_key,
			model,
			COUNT(*),
			COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END), 0),
			MAX(requested_at)
		FROM usage_records
		WHERE %s
		GROUP BY source_key, model
	`, whereClause)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("usage store: query monitor channel models: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			source   string
			model    MonitorModelStats
			lastUnix sql.NullInt64
		)
		if err = rows.Scan(&source, &model.Model, &model.Requests, &model.Success, &model.Failed, &lastUnix); err != nil {
			return fmt.Errorf("usage store: scan monitor channel model: %w", err)
		}
		model.LastRequestAt = nullUnixPointer(lastUnix)
		model.Recent = []MonitorRecentRequest{}

		normalizedSource := normalizeMonitorSource(source)
		if channel, ok := channelMap[normalizedSource]; ok {
			channel.Models = append(channel.Models, model)
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("usage store: iterate monitor channel models: %w", err)
	}
	return nil
}

func (s *sqliteUsageStore) queryFailedSources(ctx context.Context, whereClause string, args []any) ([]string, error) {
	query := fmt.Sprintf(`
		SELECT DISTINCT COALESCE(NULLIF(source, ''), 'unknown')
		FROM usage_records
		WHERE %s AND failed = 1
		ORDER BY 1
	`, whereClause)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usage store: query failed sources: %w", err)
	}
	defer rows.Close()

	sources := make([]string, 0)
	for rows.Next() {
		var source string
		if err = rows.Scan(&source); err != nil {
			return nil, fmt.Errorf("usage store: scan failed source: %w", err)
		}
		sources = append(sources, normalizeMonitorSource(source))
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usage store: iterate failed sources: %w", err)
	}
	return sources, nil
}

func (s *sqliteUsageStore) queryFailureAggregates(ctx context.Context, failedWhere string, args []any) (map[string]*MonitorFailureStats, error) {
	query := fmt.Sprintf(`
		SELECT COALESCE(NULLIF(source, ''), 'unknown') AS source_key,
			COALESCE(SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END), 0),
			MAX(CASE WHEN failed=1 THEN requested_at ELSE NULL END)
		FROM usage_records
		WHERE %s
		GROUP BY source_key
	`, failedWhere)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usage store: query failure aggregates: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*MonitorFailureStats)
	for rows.Next() {
		var (
			source   string
			item     MonitorFailureStats
			lastUnix sql.NullInt64
		)
		if err = rows.Scan(&source, &item.FailedCount, &lastUnix); err != nil {
			return nil, fmt.Errorf("usage store: scan failure aggregate: %w", err)
		}
		item.Source = normalizeMonitorSource(source)
		item.LastFailedAt = nullUnixPointer(lastUnix)
		item.Models = []MonitorModelStats{}
		result[item.Source] = &item
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usage store: iterate failure aggregates: %w", err)
	}
	return result, nil
}

func (s *sqliteUsageStore) attachFailureModels(ctx context.Context, failedWhere string, args []any, items map[string]*MonitorFailureStats) error {
	query := fmt.Sprintf(`
		SELECT COALESCE(NULLIF(source, ''), 'unknown') AS source_key,
			model,
			COUNT(*),
			COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END), 0),
			MAX(requested_at)
		FROM usage_records
		WHERE %s
		GROUP BY source_key, model
	`, failedWhere)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("usage store: query failure models: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			source   string
			model    MonitorModelStats
			lastUnix sql.NullInt64
		)
		if err = rows.Scan(&source, &model.Model, &model.Requests, &model.Success, &model.Failed, &lastUnix); err != nil {
			return fmt.Errorf("usage store: scan failure model: %w", err)
		}
		model.LastRequestAt = nullUnixPointer(lastUnix)
		model.Recent = []MonitorRecentRequest{}

		normalizedSource := normalizeMonitorSource(source)
		if item, ok := items[normalizedSource]; ok {
			item.Models = append(item.Models, model)
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("usage store: iterate failure models: %w", err)
	}
	return nil
}

func (s *sqliteUsageStore) queryMonitorFilterOptions(ctx context.Context, filter MonitorQueryFilter, includeStatus bool, includeAPIs bool) (MonitorFilterOptions, error) {
	whereClause, args := buildSQLiteMonitorWhere(filter, includeStatus)
	options := MonitorFilterOptions{APIs: []string{}, Models: []string{}, Sources: []string{}}

	var err error
	if includeAPIs {
		options.APIs, err = s.queryDistinctValues(ctx, "api_key", whereClause, args)
		if err != nil {
			return MonitorFilterOptions{}, err
		}
	}

	options.Models, err = s.queryDistinctValues(ctx, "model", whereClause, args)
	if err != nil {
		return MonitorFilterOptions{}, err
	}

	options.Sources, err = s.queryDistinctValues(ctx, "COALESCE(NULLIF(source, ''), 'unknown')", whereClause, args)
	if err != nil {
		return MonitorFilterOptions{}, err
	}

	return options, nil
}

func (s *sqliteUsageStore) queryDistinctValues(ctx context.Context, expression, whereClause string, args []any) ([]string, error) {
	query := fmt.Sprintf(`
		SELECT DISTINCT %s AS value
		FROM usage_records
		WHERE %s
		ORDER BY value ASC
	`, expression, whereClause)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usage store: query distinct values: %w", err)
	}
	defer rows.Close()

	values := make([]string, 0)
	for rows.Next() {
		var value sql.NullString
		if err = rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("usage store: scan distinct value: %w", err)
		}
		if !value.Valid {
			continue
		}
		trimmed := strings.TrimSpace(value.String)
		if trimmed == "" {
			continue
		}
		values = append(values, trimmed)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usage store: iterate distinct values: %w", err)
	}
	return values, nil
}

func buildSQLiteMonitorWhere(filter MonitorQueryFilter, includeStatus bool) (string, []any) {
	clauses := make([]string, 0, 8)
	args := make([]any, 0, 8)

	if filter.APIKey != "" {
		clauses = append(clauses, "api_key = ?")
		args = append(args, filter.APIKey)
	}
	if filter.APIContains != "" {
		clauses = append(clauses, "LOWER(api_key) LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeSQLiteLike(strings.ToLower(filter.APIContains))+"%")
	}
	if filter.Model != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, filter.Model)
	}
	if filter.Source != "" {
		normalized := normalizeMonitorSource(filter.Source)
		if normalized == "unknown" {
			clauses = append(clauses, "(source IS NULL OR source = '')")
		} else {
			clauses = append(clauses, "source = ?")
			args = append(args, normalized)
		}
	}
	if includeStatus {
		switch filter.Status {
		case "success":
			clauses = append(clauses, "failed = 0")
		case "failed":
			clauses = append(clauses, "failed = 1")
		}
	}
	if filter.Start != nil {
		clauses = append(clauses, "requested_at >= ?")
		args = append(args, filter.Start.Unix())
	}
	if filter.End != nil {
		clauses = append(clauses, "requested_at <= ?")
		args = append(args, filter.End.Unix())
	}

	if len(clauses) == 0 {
		return "1=1", args
	}
	return strings.Join(clauses, " AND "), args
}

func normalizeMonitorFilter(filter MonitorQueryFilter) MonitorQueryFilter {
	filter.APIKey = strings.TrimSpace(filter.APIKey)
	filter.APIContains = strings.TrimSpace(filter.APIContains)
	filter.Model = strings.TrimSpace(filter.Model)

	source := strings.TrimSpace(filter.Source)
	if source == "" {
		filter.Source = ""
	} else {
		filter.Source = normalizeMonitorSource(source)
	}

	filter.Status = strings.TrimSpace(strings.ToLower(filter.Status))
	if filter.Status != "success" && filter.Status != "failed" {
		filter.Status = ""
	}
	return filter
}

func normalizeMonitorSource(source string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func escapeSQLiteLike(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(value)
}

func clampInt(value, minValue, maxValue, fallback int) int {
	if value == 0 {
		value = fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func reverseRecentRequests(items []MonitorRecentRequest) {
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}

func nullUnixPointer(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	ts := time.Unix(value.Int64, 0)
	return &ts
}

func copyArgs(args []any) []any {
	if len(args) == 0 {
		return nil
	}
	copied := make([]any, len(args))
	copy(copied, args)
	return copied
}

func toInClause(values []string) (string, []any) {
	if len(values) == 0 {
		return "", nil
	}
	placeholders := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for _, value := range values {
		placeholders = append(placeholders, "?")
		args = append(args, value)
	}
	return strings.Join(placeholders, ","), args
}

func sortedSet(items map[string]struct{}) []string {
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

func (s *sqliteUsageStore) QueryMonitorRequestDetails(ctx context.Context, center *time.Time, windowSec int, method, path string, limit int) ([]MonitorRequestDetail, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usage store: sqlite store not initialized")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	clauses := []string{}
	args := []any{}

	if center != nil && windowSec > 0 {
		half := int64(windowSec) / 2
		start := center.Unix() - half
		end := center.Unix() + half
		clauses = append(clauses, "requested_at >= ?", "requested_at <= ?")
		args = append(args, start, end)
	}
	if method != "" {
		clauses = append(clauses, "method = ?")
		args = append(args, method)
	}
	if path != "" {
		clauses = append(clauses, "path LIKE ? ESCAPE '\\'")
		args = append(args, escapeSQLiteLike(path)+"%")
	}

	whereClause := "1=1"
	if len(clauses) > 0 {
		whereClause = strings.Join(clauses, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT requested_at, method, path, model,
			COALESCE(NULLIF(source, ''), 'unknown'), auth_index, failed
		FROM usage_records
		WHERE %s
		ORDER BY requested_at DESC
		LIMIT ?
	`, whereClause)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usage store: query monitor request details: %w", err)
	}
	defer rows.Close()

	items := make([]MonitorRequestDetail, 0, limit)
	for rows.Next() {
		var (
			item     MonitorRequestDetail
			unixTime int64
			failed   int
		)
		if err = rows.Scan(&unixTime, &item.Method, &item.Path, &item.Model, &item.Source, &item.AuthIndex, &failed); err != nil {
			return nil, fmt.Errorf("usage store: scan monitor request detail: %w", err)
		}
		item.Timestamp = time.Unix(unixTime, 0)
		item.Failed = failed != 0
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usage store: iterate monitor request details: %w", err)
	}
	return items, nil
}

func containsModel(models []MonitorModelStats, target string) bool {
	for _, model := range models {
		if model.Model == target {
			return true
		}
	}
	return false
}

func (s *sqliteUsageStore) QueryMonitorKpi(ctx context.Context, filter MonitorQueryFilter) (MonitorKpiResult, error) {
	if s == nil || s.db == nil {
		return MonitorKpiResult{}, fmt.Errorf("usage store: sqlite store not initialized")
	}

	whereClause, args := buildSQLiteMonitorWhere(filter, true)
	query := fmt.Sprintf(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=0 THEN total_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=0 THEN input_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=0 THEN output_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=0 THEN reasoning_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=0 THEN cached_tokens ELSE 0 END), 0),
			MIN(requested_at),
			MAX(requested_at)
		FROM usage_records
		WHERE %s
	`, whereClause)

	var result MonitorKpiResult
	var minTs, maxTs sql.NullInt64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&result.TotalRequests,
		&result.SuccessRequests,
		&result.FailedRequests,
		&result.TotalTokens,
		&result.InputTokens,
		&result.OutputTokens,
		&result.ReasoningTokens,
		&result.CachedTokens,
		&minTs,
		&maxTs,
	); err != nil {
		return MonitorKpiResult{}, fmt.Errorf("usage store: query monitor kpi: %w", err)
	}

	result.MinTimestamp = nullUnixPointer(minTs)
	result.MaxTimestamp = nullUnixPointer(maxTs)
	return result, nil
}

func (s *sqliteUsageStore) QueryMonitorModelDistribution(ctx context.Context, filter MonitorQueryFilter, limit int, sortByTokens bool) ([]MonitorModelDistItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usage store: sqlite store not initialized")
	}
	limit = clampInt(limit, 1, 100, 10)

	whereClause, args := buildSQLiteMonitorWhere(filter, false)
	orderBy := "cnt DESC, model ASC"
	if sortByTokens {
		orderBy = "tokens DESC, model ASC"
	}
	query := fmt.Sprintf(`
		SELECT model, COUNT(*) AS cnt, COALESCE(SUM(total_tokens), 0) AS tokens
		FROM usage_records
		WHERE %s
		GROUP BY model
		ORDER BY %s
		LIMIT ?
	`, whereClause, orderBy)
	queryArgs := append(copyArgs(args), limit)

	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("usage store: query monitor model distribution: %w", err)
	}
	defer rows.Close()

	items := make([]MonitorModelDistItem, 0)
	for rows.Next() {
		var item MonitorModelDistItem
		if err = rows.Scan(&item.Model, &item.Requests, &item.Tokens); err != nil {
			return nil, fmt.Errorf("usage store: scan monitor model distribution: %w", err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usage store: iterate monitor model distribution: %w", err)
	}
	return items, nil
}

func (s *sqliteUsageStore) QueryMonitorDailyTrend(ctx context.Context, filter MonitorQueryFilter) ([]MonitorDailyTrendItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usage store: sqlite store not initialized")
	}

	whereClause, args := buildSQLiteMonitorWhere(filter, false)
	query := fmt.Sprintf(`
		SELECT DATE(requested_at, 'unixepoch', 'localtime') AS date_key,
			COUNT(*),
			COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=0 THEN input_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=0 THEN output_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=0 THEN reasoning_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=0 THEN cached_tokens ELSE 0 END), 0)
		FROM usage_records
		WHERE %s
		GROUP BY date_key
		ORDER BY date_key ASC
	`, whereClause)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usage store: query monitor daily trend: %w", err)
	}
	defer rows.Close()

	items := make([]MonitorDailyTrendItem, 0)
	for rows.Next() {
		var item MonitorDailyTrendItem
		if err = rows.Scan(
			&item.Date,
			&item.Requests,
			&item.SuccessRequests,
			&item.FailedRequests,
			&item.InputTokens,
			&item.OutputTokens,
			&item.ReasoningTokens,
			&item.CachedTokens,
		); err != nil {
			return nil, fmt.Errorf("usage store: scan monitor daily trend: %w", err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usage store: iterate monitor daily trend: %w", err)
	}
	return items, nil
}

func (s *sqliteUsageStore) QueryMonitorHourlySlots(ctx context.Context, filter MonitorQueryFilter, cutoffUnix, nowUnix int64, slotSeconds int) ([]MonitorHourlySlot, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usage store: sqlite store not initialized")
	}
	if slotSeconds <= 0 {
		slotSeconds = 3600
	}

	whereClause, args := buildSQLiteMonitorWhere(filter, false)
	query := fmt.Sprintf(`
		SELECT (requested_at - ?) / ? AS slot_idx,
			model,
			COUNT(*),
			COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END), 0)
		FROM usage_records
		WHERE %s AND requested_at >= ? AND requested_at <= ?
		GROUP BY slot_idx, model
	`, whereClause)

	queryArgs := make([]any, 0, len(args)+4)
	queryArgs = append(queryArgs, cutoffUnix, slotSeconds)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, cutoffUnix, nowUnix)

	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("usage store: query monitor hourly slots: %w", err)
	}
	defer rows.Close()

	items := make([]MonitorHourlySlot, 0)
	for rows.Next() {
		var item MonitorHourlySlot
		if err = rows.Scan(&item.SlotIndex, &item.Model, &item.Total, &item.Success); err != nil {
			return nil, fmt.Errorf("usage store: scan monitor hourly slot: %w", err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usage store: iterate monitor hourly slots: %w", err)
	}
	return items, nil
}

func (s *sqliteUsageStore) QueryMonitorHourlyTokenSlots(ctx context.Context, filter MonitorQueryFilter, cutoffUnix, nowUnix int64, slotSeconds int) ([]MonitorHourlyTokenSlot, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usage store: sqlite store not initialized")
	}
	if slotSeconds <= 0 {
		slotSeconds = 3600
	}

	whereClause, args := buildSQLiteMonitorWhere(filter, false)
	query := fmt.Sprintf(`
		SELECT (requested_at - ?) / ? AS slot_idx,
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0),
			COALESCE(SUM(cached_tokens), 0)
		FROM usage_records
		WHERE %s AND requested_at >= ? AND requested_at <= ? AND failed = 0
		GROUP BY slot_idx
	`, whereClause)

	queryArgs := make([]any, 0, len(args)+4)
	queryArgs = append(queryArgs, cutoffUnix, slotSeconds)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, cutoffUnix, nowUnix)

	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("usage store: query monitor hourly token slots: %w", err)
	}
	defer rows.Close()

	items := make([]MonitorHourlyTokenSlot, 0)
	for rows.Next() {
		var item MonitorHourlyTokenSlot
		if err = rows.Scan(&item.SlotIndex, &item.TotalTokens, &item.InputTokens, &item.OutputTokens, &item.ReasoningTokens, &item.CachedTokens); err != nil {
			return nil, fmt.Errorf("usage store: scan monitor hourly token slot: %w", err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usage store: iterate monitor hourly token slots: %w", err)
	}
	return items, nil
}

func (s *sqliteUsageStore) QueryMonitorHealthBlocks(ctx context.Context, windowStartUnix, windowEndUnix int64, blockSeconds int) ([]MonitorHealthBlock, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usage store: sqlite store not initialized")
	}
	if blockSeconds <= 0 {
		blockSeconds = 900
	}

	query := `
		SELECT (requested_at - ?) / ? AS block_idx,
			COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END), 0)
		FROM usage_records
		WHERE requested_at >= ? AND requested_at <= ?
		GROUP BY block_idx
	`

	rows, err := s.db.QueryContext(ctx, query, windowStartUnix, blockSeconds, windowStartUnix, windowEndUnix)
	if err != nil {
		return nil, fmt.Errorf("usage store: query monitor health blocks: %w", err)
	}
	defer rows.Close()

	items := make([]MonitorHealthBlock, 0)
	for rows.Next() {
		var item MonitorHealthBlock
		if err = rows.Scan(&item.BlockIndex, &item.Success, &item.Failure); err != nil {
			return nil, fmt.Errorf("usage store: scan monitor health block: %w", err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usage store: iterate monitor health blocks: %w", err)
	}
	return items, nil
}

func (s *sqliteUsageStore) QueryMonitorKeyStatsBlocks(ctx context.Context, windowStartUnix, windowEndUnix int64, blockSeconds int) ([]MonitorKeyStatsRow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usage store: sqlite store not initialized")
	}
	if blockSeconds <= 0 {
		blockSeconds = 600
	}

	query := `
		SELECT (requested_at - ?) / ? AS block_idx,
			COALESCE(NULLIF(source, ''), 'unknown'),
			COALESCE(NULLIF(auth_index, ''), 'unknown'),
			COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END), 0)
		FROM usage_records
		WHERE requested_at >= ? AND requested_at <= ?
		GROUP BY block_idx, COALESCE(NULLIF(source, ''), 'unknown'), COALESCE(NULLIF(auth_index, ''), 'unknown')
	`

	rows, err := s.db.QueryContext(ctx, query, windowStartUnix, blockSeconds, windowStartUnix, windowEndUnix)
	if err != nil {
		return nil, fmt.Errorf("usage store: query monitor key stats blocks: %w", err)
	}
	defer rows.Close()

	items := make([]MonitorKeyStatsRow, 0)
	for rows.Next() {
		var item MonitorKeyStatsRow
		if err = rows.Scan(&item.BlockIndex, &item.Source, &item.AuthIndex, &item.Success, &item.Failure); err != nil {
			return nil, fmt.Errorf("usage store: scan monitor key stats row: %w", err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usage store: iterate monitor key stats blocks: %w", err)
	}
	return items, nil
}
