package usagestats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// DefaultGroupLimit caps how many groups an aggregate query returns.
const DefaultGroupLimit = 2000

// ErrInvalidQuery marks errors caused by a malformed query rather than storage.
var ErrInvalidQuery = errors.New("invalid usage query")

// dimensionColumns maps the dimensions a query may group or filter by to
// their column. The time buckets "day" and "hour" are handled separately
// because they depend on the caller's UTC offset.
var dimensionColumns = map[string]string{
	"model":            "model",
	"alias":            "alias",
	"provider":         "provider",
	"account":          "account",
	"auth_index":       "auth_index",
	"auth_type":        "auth_type",
	"client_key":       "client_key",
	"client_ip":        "client_ip",
	"user_agent":       "user_agent",
	"endpoint":         "endpoint",
	"project":          "project",
	"session_id":       "session_id",
	"reasoning_effort": "reasoning_effort",
	"service_tier":     "service_tier",
	"status_code":      "status_code",
	"stream":           "stream",
	"failed":           "failed",
}

// Query selects and groups usage rows.
type Query struct {
	// From and To bound the request time as [From, To).
	From time.Time
	To   time.Time
	// UTCOffset shifts timestamps before bucketing into days or hours, so
	// buckets follow the viewer's calendar rather than UTC.
	UTCOffset time.Duration
	// GroupBy lists dimensions; "day" and "hour" are also accepted.
	GroupBy []string
	// Filters restrict rows to exact dimension values.
	Filters map[string]string
	// Limit caps the number of groups; zero means DefaultGroupLimit.
	Limit int
}

// Metrics are the summed measures of a group of rows. Token buckets are
// mutually exclusive, so TotalTokens is their sum.
type Metrics struct {
	Requests           int64 `json:"requests"`
	Failed             int64 `json:"failed"`
	InputTokens        int64 `json:"input_tokens"`
	CacheReadTokens    int64 `json:"cache_read_tokens"`
	CacheWriteTokens   int64 `json:"cache_write_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	ReasoningTokens    int64 `json:"reasoning_tokens"`
	UnclassifiedTokens int64 `json:"unclassified_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
	WebSearchRequests  int64 `json:"web_search_requests"`
	// CostUSD is the API-equivalent cost at list prices.
	CostUSD float64 `json:"cost_usd"`
	// UnpricedTokens counts tokens that CostUSD does not cover, either because
	// the model has no known price or because the tokens were unclassified.
	UnpricedTokens int64 `json:"unpriced_tokens"`
}

// Group is one aggregate row: its dimension values and summed metrics.
type Group struct {
	Dimensions map[string]string `json:"dimensions"`
	Metrics
}

// Aggregate is the result of an aggregate query.
type Aggregate struct {
	Groups []Group `json:"groups"`
	// Truncated reports that groups beyond the limit were dropped, smallest first.
	Truncated bool `json:"truncated"`
	// UnpricedModels lists models seen in the range that have no known price.
	UnpricedModels []string `json:"unpriced_models"`
}

// dimensionExpr returns the SQL expression for a dimension.
func dimensionExpr(name string, offset time.Duration) (string, error) {
	// The offset is an integer computed from a duration, so it is safe to inline.
	// Bound parameters cannot be reused across SELECT and GROUP BY positions.
	shifted := fmt.Sprintf("(ts_ms + %d) / 1000", offset.Milliseconds())
	switch name {
	case "day":
		return "strftime('%Y-%m-%d', " + shifted + ", 'unixepoch')", nil
	case "hour":
		return "strftime('%Y-%m-%dT%H:00', " + shifted + ", 'unixepoch')", nil
	}
	if column, ok := dimensionColumns[name]; ok {
		return column, nil
	}
	return "", fmt.Errorf("%w: unknown dimension %q", ErrInvalidQuery, name)
}

// whereClause builds the row filter shared by every query.
func (q Query) whereClause() (string, []any, error) {
	if q.From.IsZero() || q.To.IsZero() || !q.To.After(q.From) {
		return "", nil, fmt.Errorf("%w: from must be before to", ErrInvalidQuery)
	}
	clauses := []string{"ts_ms >= ?", "ts_ms < ?"}
	args := []any{q.From.UnixMilli(), q.To.UnixMilli()}
	// Sorted for a stable statement text.
	names := make([]string, 0, len(q.Filters))
	for name := range q.Filters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		column, ok := dimensionColumns[name]
		if !ok {
			return "", nil, fmt.Errorf("%w: unknown filter %q", ErrInvalidQuery, name)
		}
		clauses = append(clauses, column+" = ?")
		args = append(args, q.Filters[name])
	}
	return strings.Join(clauses, " AND "), args, nil
}

// Aggregate groups the selected rows by the query's dimensions and prices them.
func (s *Store) Aggregate(ctx context.Context, q Query) (Aggregate, error) {
	result := Aggregate{Groups: []Group{}, UnpricedModels: []string{}}
	if s == nil {
		return result, errors.New("usagestats: store is closed")
	}
	where, args, errWhere := q.whereClause()
	if errWhere != nil {
		return result, errWhere
	}
	exprs := make([]string, 0, len(q.GroupBy)+1)
	for _, name := range q.GroupBy {
		expr, errExpr := dimensionExpr(name, q.UTCOffset)
		if errExpr != nil {
			return result, errExpr
		}
		exprs = append(exprs, expr)
	}
	// Rows are always grouped by model as well, because cost depends on the
	// model. Groups are then folded back to the requested dimensions.
	exprs = append(exprs, "model")
	positions := make([]string, len(exprs))
	for i := range exprs {
		positions[i] = fmt.Sprint(i + 1)
	}
	statement := "SELECT " + strings.Join(exprs, ", ") + `,
		COUNT(*), SUM(failed), SUM(input_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens),
		SUM(cache_write_1h_tokens), SUM(output_tokens), SUM(reasoning_tokens), SUM(unclassified_tokens),
		SUM(total_tokens), SUM(web_search_requests)
		FROM requests WHERE ` + where + " GROUP BY " + strings.Join(positions, ", ")

	prices, errPrices := s.priceTable(ctx)
	if errPrices != nil {
		return result, errPrices
	}
	rows, errQuery := s.db.QueryContext(ctx, statement, args...)
	if errQuery != nil {
		return result, fmt.Errorf("usagestats: aggregate: %w", errQuery)
	}
	defer closeRows(rows)

	groups := make(map[string]*Group)
	unpriced := make(map[string]struct{})
	values := make([]string, len(exprs))
	for rows.Next() {
		var m Metrics
		var cacheWrite1h int64
		dest := make([]any, 0, len(exprs)+11)
		for i := range values {
			dest = append(dest, &values[i])
		}
		dest = append(dest, &m.Requests, &m.Failed, &m.InputTokens, &m.CacheReadTokens, &m.CacheWriteTokens,
			&cacheWrite1h, &m.OutputTokens, &m.ReasoningTokens, &m.UnclassifiedTokens, &m.TotalTokens, &m.WebSearchRequests)
		if errScan := rows.Scan(dest...); errScan != nil {
			return result, fmt.Errorf("usagestats: scan aggregate row: %w", errScan)
		}
		model := values[len(values)-1]
		if !prices.priceMetrics(model, &m, cacheWrite1h) && m.TotalTokens > 0 {
			unpriced[model] = struct{}{}
		}

		dims := values[:len(q.GroupBy)]
		key := strings.Join(dims, "\x00")
		group, ok := groups[key]
		if !ok {
			group = &Group{Dimensions: make(map[string]string, len(dims))}
			for i, name := range q.GroupBy {
				group.Dimensions[name] = dims[i]
			}
			groups[key] = group
		}
		group.add(m)
	}
	if errRows := rows.Err(); errRows != nil {
		return result, fmt.Errorf("usagestats: read aggregate rows: %w", errRows)
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultGroupLimit
	}
	if len(keys) > limit {
		// Keep the largest groups, which are the ones a dashboard shows.
		sort.Slice(keys, func(i, j int) bool { return groups[keys[i]].TotalTokens > groups[keys[j]].TotalTokens })
		keys = keys[:limit]
		result.Truncated = true
	}
	sort.Strings(keys)
	for _, key := range keys {
		result.Groups = append(result.Groups, *groups[key])
	}
	for model := range unpriced {
		result.UnpricedModels = append(result.UnpricedModels, model)
	}
	sort.Strings(result.UnpricedModels)
	return result, nil
}

func (m *Metrics) add(other Metrics) {
	m.Requests += other.Requests
	m.Failed += other.Failed
	m.InputTokens += other.InputTokens
	m.CacheReadTokens += other.CacheReadTokens
	m.CacheWriteTokens += other.CacheWriteTokens
	m.OutputTokens += other.OutputTokens
	m.ReasoningTokens += other.ReasoningTokens
	m.UnclassifiedTokens += other.UnclassifiedTokens
	m.TotalTokens += other.TotalTokens
	m.WebSearchRequests += other.WebSearchRequests
	m.CostUSD += other.CostUSD
	m.UnpricedTokens += other.UnpricedTokens
}

// Speed summarizes how fast the successful requests of a group completed.
type Speed struct {
	Value        string `json:"value"`
	Requests     int64  `json:"requests"`
	LatencyP50Ms int64  `json:"latency_p50_ms"`
	LatencyP95Ms int64  `json:"latency_p95_ms"`
	TTFTP50Ms    int64  `json:"ttft_p50_ms"`
	TTFTP95Ms    int64  `json:"ttft_p95_ms"`
	// OutputTokensPerSecond is output throughput after the first token, over
	// streamed requests, where generation time can be told apart from waiting.
	OutputTokensPerSecond float64 `json:"output_tokens_per_second"`
}

// Speed returns latency percentiles and output throughput of successful
// requests, grouped by one dimension.
func (s *Store) Speed(ctx context.Context, q Query, dimension string) ([]Speed, error) {
	if s == nil {
		return nil, errors.New("usagestats: store is closed")
	}
	column, ok := dimensionColumns[dimension]
	if !ok {
		return nil, fmt.Errorf("%w: unknown dimension %q", ErrInvalidQuery, dimension)
	}
	where, args, errWhere := q.whereClause()
	if errWhere != nil {
		return nil, errWhere
	}
	rows, errQuery := s.db.QueryContext(ctx,
		"SELECT "+column+", latency_ms, ttft_ms, output_tokens + reasoning_tokens, stream FROM requests WHERE "+where+" AND failed = 0",
		args...,
	)
	if errQuery != nil {
		return nil, fmt.Errorf("usagestats: speed: %w", errQuery)
	}
	defer closeRows(rows)

	type samples struct {
		latency, ttft            []int64
		outputTokens, generateMs int64
	}
	byValue := make(map[string]*samples)
	for rows.Next() {
		var value string
		var latency, ttft, output int64
		var stream bool
		if errScan := rows.Scan(&value, &latency, &ttft, &output, &stream); errScan != nil {
			return nil, fmt.Errorf("usagestats: scan speed row: %w", errScan)
		}
		group, okGroup := byValue[value]
		if !okGroup {
			group = &samples{}
			byValue[value] = group
		}
		group.latency = append(group.latency, latency)
		if ttft > 0 {
			group.ttft = append(group.ttft, ttft)
		}
		if stream && ttft > 0 && latency > ttft && output > 0 {
			group.outputTokens += output
			group.generateMs += latency - ttft
		}
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("usagestats: read speed rows: %w", errRows)
	}

	result := make([]Speed, 0, len(byValue))
	for value, group := range byValue {
		slices.Sort(group.latency)
		slices.Sort(group.ttft)
		speed := Speed{
			Value:        value,
			Requests:     int64(len(group.latency)),
			LatencyP50Ms: percentile(group.latency, 50),
			LatencyP95Ms: percentile(group.latency, 95),
			TTFTP50Ms:    percentile(group.ttft, 50),
			TTFTP95Ms:    percentile(group.ttft, 95),
		}
		if group.generateMs > 0 {
			speed.OutputTokensPerSecond = float64(group.outputTokens) / (float64(group.generateMs) / 1000)
		}
		result = append(result, speed)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultGroupLimit
	}
	if len(result) > limit {
		// Keep the groups with the most requests, as Aggregate keeps the largest.
		sort.Slice(result, func(i, j int) bool {
			if result[i].Requests != result[j].Requests {
				return result[i].Requests > result[j].Requests
			}
			return result[i].Value < result[j].Value
		})
		result = result[:limit]
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Value < result[j].Value })
	return result, nil
}

// closeRows releases a result set, logging rather than dropping a close error.
func closeRows(rows *sql.Rows) {
	if errClose := rows.Close(); errClose != nil {
		log.Warnf("usage statistics: close rows: %v", errClose)
	}
}

// percentile returns the nearest-rank percentile of sorted values, or 0 when empty.
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (len(sorted)*p + 99) / 100
	return sorted[max(rank, 1)-1]
}
