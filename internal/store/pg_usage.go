package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// UsageWindowType distinguishes the granularity at which a budget window rolls.
const (
	WindowTypeHourly  = "hourly"
	WindowTypeWeekly  = "weekly"
	WindowTypeMonthly = "monthly"
)

// UsageEvent is the persisted representation of a request's usage record. The
// fields parallel the in-memory coreusage.Record but include the resolved
// api_key_id and computed cost_usd that the hot path does not know at publish
// time.
type UsageEvent struct {
	ID                  int64     `json:"id,omitempty"`
	RequestID           string    `json:"request_id,omitempty"`
	APIKeyID            string    `json:"api_key_id,omitempty"`
	APIKeyPrincipal     string    `json:"api_key_principal,omitempty"`
	Provider            string    `json:"provider"`
	ExecutorType        string    `json:"executor_type,omitempty"`
	Model               string    `json:"model"`
	Alias               string    `json:"alias,omitempty"`
	Endpoint            string    `json:"endpoint,omitempty"`
	AuthType            string    `json:"auth_type,omitempty"`
	Source              string    `json:"source,omitempty"`
	ReasoningEffort     string    `json:"reasoning_effort,omitempty"`
	ServiceTier         string    `json:"service_tier,omitempty"`
	ResponseServiceTier string    `json:"response_service_tier,omitempty"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	ReasoningTokens     int64     `json:"reasoning_tokens"`
	CachedTokens        int64     `json:"cached_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	TotalTokens         int64     `json:"total_tokens"`
	CostUSD             float64   `json:"cost_usd"`
	LatencyMs           int64     `json:"latency_ms,omitempty"`
	TTFTMs              int64     `json:"ttft_ms,omitempty"`
	Failed              bool      `json:"failed"`
	FailStatusCode      int       `json:"fail_status_code,omitempty"`
	Generate            bool      `json:"generate,omitempty"`
	RequestedAt         time.Time `json:"requested_at"`
}

// Pricing captures per-model unit prices in USD per 1,000,000 tokens. A zero
// value (or missing row) is treated as "no cost" — usage still records the
// token counts but contributes 0 USD toward budget enforcement.
type Pricing struct {
	ID               string  `json:"id"`
	InputPer1M       float64 `json:"input_per_1m_usd"`
	OutputPer1M      float64 `json:"output_per_1m_usd"`
	CachedInputPer1M float64 `json:"cached_input_per_1m_usd"`
	CachedReadPer1M  float64 `json:"cached_read_per_1m_usd"`
	ReasoningPer1M   float64 `json:"reasoning_per_1m_usd"`
}

// UsageFilter specifies the slice of usage data to aggregate.
type UsageFilter struct {
	APIKeyID  string
	Principal string
	Provider  string
	Model     string
	From      time.Time
	To        time.Time
	// GroupBy selects the aggregation dimension: "api_key_id" | "model" |
	// "provider" | "day" | "hour" | "" (no grouping, totals only).
	GroupBy string
	Limit   int
}

// UsageAggregate mirrors a single aggregated row returned by SelectAggregate.
type UsageAggregate struct {
	Bucket          string  `json:"bucket,omitempty"`
	APIKeyID        string  `json:"api_key_id,omitempty"`
	APIKeyPrincipal string  `json:"api_key_principal,omitempty"`
	Model           string  `json:"model,omitempty"`
	Provider        string  `json:"provider,omitempty"`
	RequestCount    int64   `json:"request_count"`
	FailedCount     int64   `json:"failed_count"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	CostUSD         float64 `json:"cost_usd"`
}

// UsageWindow mirrors a row in usage_windows and is used both for budget
// enforcement reads and management API exposure.
type UsageWindow struct {
	APIKeyID     string    `json:"api_key_id"`
	WindowType   string    `json:"window_type"`
	WindowStart  time.Time `json:"window_start"`
	WindowEnd    time.Time `json:"window_end"`
	RequestCount int64     `json:"request_count"`
	TotalTokens  int64     `json:"total_tokens"`
	CostUSD      float64   `json:"cost_usd"`
}

// UsageStore wraps the database handle with usage-persistence operations. It
// is safe for concurrent use: the parent *sql.DB manages pooling.
type UsageStore struct {
	db           *sql.DB
	eventsTable  string
	windowsTable string
	pricingTable string
}

// NewUsageStore builds a UsageStore from a PostgresStore connection. Returns
// nil when the parent store is nil so feature-detection is a single nil check.
func NewUsageStore(parent *PostgresStore) *UsageStore {
	if parent == nil {
		return nil
	}
	return &UsageStore{
		db:           parent.DB(),
		eventsTable:  parent.UsageEventsTable(),
		windowsTable: parent.UsageWindowsTable(),
		pricingTable: parent.ModelPricingTable(),
	}
}

const usageEventColumnList = `
	request_id, api_key_id, api_key_principal, provider, executor_type, model,
	alias, endpoint, auth_type, source, reasoning_effort, service_tier,
	response_service_tier, input_tokens, output_tokens, reasoning_tokens,
	cached_tokens, cache_creation_tokens, total_tokens, cost_usd, latency_ms,
	ttft_ms, failed, fail_status_code, generate, requested_at
`

// InsertEvent records a single usage event.
func (s *UsageStore) InsertEvent(ctx context.Context, e UsageEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if e.RequestedAt.IsZero() {
		e.RequestedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (%s) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23,
			$24, $25, $26)
	`, s.eventsTable, usageEventColumnList),
		e.RequestID, nullableString(e.APIKeyID), nullableString(e.APIKeyPrincipal),
		e.Provider, e.ExecutorType, e.Model, e.Alias, e.Endpoint, e.AuthType,
		e.Source, e.ReasoningEffort, e.ServiceTier, e.ResponseServiceTier,
		e.InputTokens, e.OutputTokens, e.ReasoningTokens, e.CachedTokens,
		e.CacheCreationTokens, e.TotalTokens, e.CostUSD, e.LatencyMs, e.TTFTMs,
		e.Failed, e.FailStatusCode, e.Generate, e.RequestedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres store: insert usage event: %w", err)
	}
	return nil
}

// BatchInsertEvents records up to len(events) usage events in a single
// multi-value INSERT statement. Callers should bound the slice length to a
// sane batch size (the flusher defaults to 100).
func (s *UsageStore) BatchInsertEvents(ctx context.Context, events []UsageEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if len(events) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(s.eventsTable)
	b.WriteString(" (")
	b.WriteString(usageEventColumnList)
	b.WriteString(") VALUES ")
	args := make([]any, 0, len(events)*26)
	for i, ev := range events {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		for j := 1; j <= 26; j++ {
			if j > 1 {
				b.WriteByte(',')
			}
			b.WriteByte('$')
			b.WriteString(itoa(i*26 + j))
		}
		b.WriteByte(')')
		if ev.RequestedAt.IsZero() {
			ev.RequestedAt = time.Now().UTC()
		}
		args = append(args, ev.RequestID, nullableString(ev.APIKeyID), nullableString(ev.APIKeyPrincipal),
			ev.Provider, ev.ExecutorType, ev.Model, ev.Alias, ev.Endpoint, ev.AuthType,
			ev.Source, ev.ReasoningEffort, ev.ServiceTier, ev.ResponseServiceTier,
			ev.InputTokens, ev.OutputTokens, ev.ReasoningTokens, ev.CachedTokens,
			ev.CacheCreationTokens, ev.TotalTokens, ev.CostUSD, ev.LatencyMs, ev.TTFTMs,
			ev.Failed, ev.FailStatusCode, ev.Generate, ev.RequestedAt)
	}
	if _, err := s.db.ExecContext(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("postgres store: batch insert usage events: %w", err)
	}
	return nil
}

// itoa writes a small non-negative integer as a string without pulling in
// strconv at the call site (kept tight for the hot batch-insert builder).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// UpsertWindow increments the budget window counter atomically. The row is
// created on first use; subsequent calls add delta values. Window boundaries
// must be pre-computed by the caller (the policy service does this via the
// Clock helper).
func (s *UsageStore) UpsertWindow(ctx context.Context, apiKeyID, windowType string, windowStart, windowEnd time.Time, deltaReq, deltaTokens int64, deltaCost float64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if apiKeyID == "" || windowType == "" {
		return fmt.Errorf("postgres store: upsert window requires api_key_id and window_type")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (api_key_id, window_type, window_start, window_end,
			request_count, total_tokens, cost_usd)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (api_key_id, window_type, window_start) DO UPDATE SET
			request_count = %s.request_count + EXCLUDED.request_count,
			total_tokens  = %s.total_tokens + EXCLUDED.total_tokens,
			cost_usd      = %s.cost_usd + EXCLUDED.cost_usd
	`, s.windowsTable, s.windowsTable, s.windowsTable, s.windowsTable),
		apiKeyID, windowType, windowStart, windowEnd, deltaReq, deltaTokens, deltaCost)
	if err != nil {
		return fmt.Errorf("postgres store: upsert usage window: %w", err)
	}
	return nil
}

// GetWindow returns the budget window for a key covering the supplied
// reference time. The caller picks windowStart/windowEnd; this method just
// reflects the persisted counter values (zero row → zero counters).
func (s *UsageStore) GetWindow(ctx context.Context, apiKeyID, windowType string, windowStart time.Time) (UsageWindow, error) {
	if s == nil || s.db == nil {
		return UsageWindow{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	var w UsageWindow
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT api_key_id, window_type, window_start, window_end,
			COALESCE(request_count, 0), COALESCE(total_tokens, 0),
			COALESCE(cost_usd, 0)
		FROM %s
		WHERE api_key_id = $1 AND window_type = $2 AND window_start = $3
	`, s.windowsTable), apiKeyID, windowType, windowStart)
	w.WindowType = windowType
	err := row.Scan(&w.APIKeyID, &w.WindowType, &w.WindowStart, &w.WindowEnd,
		&w.RequestCount, &w.TotalTokens, &w.CostUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return UsageWindow{
			APIKeyID:    apiKeyID,
			WindowType:  windowType,
			WindowStart: windowStart,
		}, nil
	}
	if err != nil {
		return UsageWindow{}, fmt.Errorf("postgres store: get usage window: %w", err)
	}
	return w, nil
}

// ListWindows returns all budget windows for an API key (any type) ordered by
// window_start desc, capped at limit (default 100 when limit<=0).
func (s *UsageStore) ListWindows(ctx context.Context, apiKeyID string, limit int) ([]UsageWindow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT api_key_id, window_type, window_start, window_end,
			request_count, total_tokens, cost_usd
		FROM %s
		WHERE api_key_id = $1
		ORDER BY window_start DESC
		LIMIT $2
	`, s.windowsTable), apiKeyID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list usage windows: %w", err)
	}
	defer rows.Close()
	out := make([]UsageWindow, 0, limit)
	for rows.Next() {
		var w UsageWindow
		if err = rows.Scan(&w.APIKeyID, &w.WindowType, &w.WindowStart, &w.WindowEnd,
			&w.RequestCount, &w.TotalTokens, &w.CostUSD); err != nil {
			return nil, fmt.Errorf("postgres store: scan usage window: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SelectAggregate runs a grouped sum query over usage_events. The grouping
// dimension is derived from filter.GroupBy. The result rows always expose
// (bucket, principal, request_count, failed_count, token sums, cost): the
// principal column is null when GROUP BY is not api_key_id (one bucket may
// contain many principals).
func (s *UsageStore) SelectAggregate(ctx context.Context, filter UsageFilter) ([]UsageAggregate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	groupExpr, groupCol, err := aggregateGroupClause(filter.GroupBy)
	if err != nil {
		return nil, err
	}
	// Build the SELECT clause. We always project two leading columns for
	// Scan: bucket (TEXT) and principal (TEXT, nullable). Both columns must
	// appear in GROUP BY when grouping is active.
	var b strings.Builder
	b.WriteString("SELECT ")
	if groupExpr == "" {
		b.WriteString("'total' AS bucket, NULL::text AS principal")
	} else {
		b.WriteString(groupExpr)
		b.WriteString(" AS bucket, MAX(api_key_principal) AS principal")
	}
	b.WriteString(`
		, COALESCE(SUM(CASE WHEN NOT failed THEN 1 ELSE 0 END), 0) AS request_count,
		COALESCE(SUM(CASE WHEN failed THEN 1 ELSE 0 END), 0) AS failed_count,
		COALESCE(SUM(input_tokens), 0) AS input_tokens,
		COALESCE(SUM(output_tokens), 0) AS output_tokens,
		COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
		COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(cost_usd), 0) AS cost_usd
	FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(" WHERE 1=1")
	args := []any{}
	if filter.APIKeyID != "" {
		args = append(args, filter.APIKeyID)
		b.WriteString(" AND api_key_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Principal != "" {
		args = append(args, filter.Principal)
		b.WriteString(" AND api_key_principal = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Provider != "" {
		args = append(args, filter.Provider)
		b.WriteString(" AND provider = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Model != "" {
		args = append(args, filter.Model)
		b.WriteString(" AND model = $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		b.WriteString(" AND requested_at >= $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		b.WriteString(" AND requested_at < $")
		b.WriteString(itoa(len(args)))
	}
	if groupCol != "" {
		b.WriteString(" GROUP BY ")
		b.WriteString(groupCol)
		b.WriteString(" ORDER BY bucket")
	}
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		b.WriteString(" LIMIT $")
		b.WriteString(itoa(len(args)))
	}

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select usage aggregate: %w", err)
	}
	defer rows.Close()
	out := make([]UsageAggregate, 0, 16)
	for rows.Next() {
		var (
			a         UsageAggregate
			principal sql.NullString
		)
		if err = rows.Scan(&a.Bucket, &principal, &a.RequestCount, &a.FailedCount,
			&a.InputTokens, &a.OutputTokens, &a.ReasoningTokens, &a.CachedTokens,
			&a.TotalTokens, &a.CostUSD); err != nil {
			return nil, fmt.Errorf("postgres store: scan usage aggregate: %w", err)
		}
		if principal.Valid {
			a.APIKeyPrincipal = principal.String
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// aggregateGroupClause resolves the GROUP BY settings for an aggregate query.
// Returns:
//   - groupExpr: the SELECT projection that becomes the bucket (already
//     contains any function call like date_trunc).
//   - groupCol: the GROUP BY clause body (a single column reference or
//     function expression). Empty string means "no grouping, totals only".
//
// The split is needed because PostgreSQL requires GROUP BY to reference the
// underlying column/function rather than the SELECT alias when the alias
// points at a function expression in some versions.
func aggregateGroupClause(groupBy string) (groupExpr, groupCol string, err error) {
	switch strings.ToLower(strings.TrimSpace(groupBy)) {
	case "", "total":
		return "", "", nil
	case "api_key_id", "apikey", "key":
		return "api_key_id", "api_key_id", nil
	case "model":
		return "model", "model", nil
	case "provider":
		return "provider", "provider", nil
	case "day":
		return "date_trunc('day', requested_at)", "date_trunc('day', requested_at)", nil
	case "hour":
		return "date_trunc('hour', requested_at)", "date_trunc('hour', requested_at)", nil
	default:
		return "", "", fmt.Errorf("unsupported group_by: %q", groupBy)
	}
}

// GetPricing returns the pricing row for model id. Missing rows return a
// zero-value Pricing (treated as no-cost) without error.
func (s *UsageStore) GetPricing(ctx context.Context, id string) (Pricing, error) {
	if s == nil || s.db == nil {
		return Pricing{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	var p Pricing
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, input_per_1m_usd, output_per_1m_usd,
			cached_input_per_1m_usd, cached_read_per_1m_usd, reasoning_per_1m_usd
		FROM %s WHERE id = $1
	`, s.pricingTable), id)
	p.ID = id
	err := row.Scan(&p.ID, &p.InputPer1M, &p.OutputPer1M, &p.CachedInputPer1M, &p.CachedReadPer1M, &p.ReasoningPer1M)
	if errors.Is(err, sql.ErrNoRows) {
		return Pricing{ID: id}, nil
	}
	if err != nil {
		return Pricing{}, fmt.Errorf("postgres store: get pricing: %w", err)
	}
	return p, nil
}

// UpsertPricing inserts or replaces the pricing row for the given model id.
// Used by the model_pricing management endpoint.
func (s *UsageStore) UpsertPricing(ctx context.Context, p Pricing) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if p.ID == "" {
		return fmt.Errorf("postgres store: upsert pricing requires id")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, input_per_1m_usd, output_per_1m_usd,
			cached_input_per_1m_usd, cached_read_per_1m_usd, reasoning_per_1m_usd, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (id) DO UPDATE SET
			input_per_1m_usd = EXCLUDED.input_per_1m_usd,
			output_per_1m_usd = EXCLUDED.output_per_1m_usd,
			cached_input_per_1m_usd = EXCLUDED.cached_input_per_1m_usd,
			cached_read_per_1m_usd = EXCLUDED.cached_read_per_1m_usd,
			reasoning_per_1m_usd = EXCLUDED.reasoning_per_1m_usd,
			updated_at = NOW()
	`, s.pricingTable), p.ID, p.InputPer1M, p.OutputPer1M, p.CachedInputPer1M, p.CachedReadPer1M, p.ReasoningPer1M)
	if err != nil {
		return fmt.Errorf("postgres store: upsert pricing: %w", err)
	}
	return nil
}

// ComputeCost derives the dollar cost of a single usage event given the
// applicable Pricing. Token counts are expressed per-1M units, matching the
// pricing table columns.
//
// Semantics:
//   - input:           prompt tokens freshly processed by the upstream.
//   - output:          completion tokens generated by the upstream.
//   - reasoning:       reasoning/thinking tokens (Claude extended thinking, OpenAI o1).
//   - cached:          cache-creation (write) tokens — providers bill these as
//     an input-token surcharge for storing the prompt.
//   - cacheRead:       cache-read tokens — providers bill these at a discount
//     because the prompt was served from cache rather than
//     re-evaluated. Anthropic distinguishes the two; OpenAI's
//     "cached_tokens" is conventionally cache-read.
func ComputeCost(p Pricing, input, output, reasoning, cached, cacheRead int64) float64 {
	perM := func(value float64, tokens int64) float64 {
		if tokens <= 0 || value <= 0 {
			return 0
		}
		return float64(tokens) * value / 1_000_000
	}
	return perM(p.InputPer1M, input) +
		perM(p.OutputPer1M, output) +
		perM(p.ReasoningPer1M, reasoning) +
		perM(p.CachedInputPer1M, cached) +
		perM(p.CachedReadPer1M, cacheRead)
}

// ListPricing returns all model pricing rows (used for cache warming + management UI).
func (s *UsageStore) ListPricing(ctx context.Context) ([]Pricing, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, input_per_1m_usd, output_per_1m_usd,
			cached_input_per_1m_usd, cached_read_per_1m_usd, reasoning_per_1m_usd
		FROM %s ORDER BY id
	`, s.pricingTable))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list pricing: %w", err)
	}
	defer rows.Close()
	out := make([]Pricing, 0, 32)
	for rows.Next() {
		var p Pricing
		if err = rows.Scan(&p.ID, &p.InputPer1M, &p.OutputPer1M, &p.CachedInputPer1M, &p.CachedReadPer1M, &p.ReasoningPer1M); err != nil {
			return nil, fmt.Errorf("postgres store: scan pricing: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteEventsBefore purges usage_events older than the cutoff timestamp. Use
// for retention cleanup (optional,configured by PGSTORE_USAGE_RETENTION_DAYS).
func (s *UsageStore) DeleteEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE requested_at < $1`, s.eventsTable,
	), cutoff)
	if err != nil {
		return 0, fmt.Errorf("postgres store: delete old usage events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("postgres store: usage delete rows affected: %w", err)
	}
	return n, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// MarshalModelsHelper exists so internal/store does not import testing-only
// json helpers; package-internal callers can reuse the pattern.
var _ = json.Marshal

// UsageTimeSeriesPoint is one bucket in a time-series query.
type UsageTimeSeriesPoint struct {
	Bucket          string  `json:"bucket"`    // RFC3339 timestamp string (UTC)
	BucketTimestamp int64   `json:"bucket_ts"` // epoch seconds, handy for client JS new Date()
	RequestCount    int64   `json:"request_count"`
	FailedCount     int64   `json:"failed_count"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	CostUSD         float64 `json:"cost_usd"`
}

// TopEntry is one row in a "top N models / keys / providers" leaderboard.
type TopEntry struct {
	Key             string  `json:"key"` // model id / principal / provider / api_key_id
	RequestCount    int64   `json:"request_count"`
	FailedCount     int64   `json:"failed_count"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	CostUSD         float64 `json:"cost_usd"`
}

// SelectTimeSeries returns one bucket per interval across the requested
// window. The `interval` parameter controls bucket granularity:
//
//	"minute", "hour", "day" (default hour).
//
// Filters are applied identically to SelectAggregate. The result is always
// sorted ascending by bucket_start; buckets with zero activity are
// omitted (callers may pad client-side if they want a smooth axis).
func (s *UsageStore) SelectTimeSeries(ctx context.Context, filter UsageFilter, interval string) ([]UsageTimeSeriesPoint, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	intervalExpr, err := intervalExpr(interval)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(intervalExpr)
	b.WriteString(" AS bucket, EXTRACT(EPOCH FROM ")
	b.WriteString(intervalExpr)
	b.WriteString(`)::bigint AS bucket_ts,
		COALESCE(SUM(CASE WHEN NOT failed THEN 1 ELSE 0 END), 0) AS request_count,
		COALESCE(SUM(CASE WHEN failed THEN 1 ELSE 0 END), 0) AS failed_count,
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(reasoning_tokens), 0), COALESCE(SUM(cached_tokens), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0)
	FROM `)
	b.WriteString(s.eventsTable)
	args := buildWhereClause(&b, filter)
	b.WriteString(" GROUP BY bucket, bucket_ts ORDER BY bucket ASC")
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		b.WriteString(" LIMIT $")
		b.WriteString(itoa(len(args)))
	}
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select usage timeseries: %w", err)
	}
	defer rows.Close()
	out := make([]UsageTimeSeriesPoint, 0, 32)
	for rows.Next() {
		var p UsageTimeSeriesPoint
		var bucket time.Time
		if err = rows.Scan(&bucket, &p.BucketTimestamp, &p.RequestCount, &p.FailedCount,
			&p.InputTokens, &p.OutputTokens, &p.ReasoningTokens, &p.CachedTokens,
			&p.TotalTokens, &p.CostUSD); err != nil {
			return nil, fmt.Errorf("postgres store: scan timeseries row: %w", err)
		}
		p.Bucket = bucket.UTC().Format(time.RFC3339)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SelectTop returns the top-N rows ordered by a chosen metric across the
// supplied dimension. dimension must be one of: "model", "provider",
// "api_key_id", "api_key_principal". metric must be one of:
// "request_count", "total_tokens", "cost_usd" (default request_count).
func (s *UsageStore) SelectTop(ctx context.Context, filter UsageFilter, dimension, metric string, limit int) ([]TopEntry, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	dimCol, err := dimensionColumn(dimension)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 500 {
		limit = 500
	}
	metricExpr, err := metricExpr(metric)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(dimCol)
	b.WriteString(` AS key,
		COALESCE(SUM(CASE WHEN NOT failed THEN 1 ELSE 0 END), 0) AS request_count,
		COALESCE(SUM(CASE WHEN failed THEN 1 ELSE 0 END), 0) AS failed_count,
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(reasoning_tokens), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0)
	FROM `)
	b.WriteString(s.eventsTable)
	args := buildWhereClause(&b, filter)
	b.WriteString(" GROUP BY key ORDER BY ")
	b.WriteString(metricExpr)
	b.WriteString(" DESC, key ASC")
	args = append(args, limit)
	b.WriteString(" LIMIT $")
	b.WriteString(itoa(len(args)))
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select usage top: %w", err)
	}
	defer rows.Close()
	out := make([]TopEntry, 0, limit)
	for rows.Next() {
		var t TopEntry
		if err = rows.Scan(&t.Key, &t.RequestCount, &t.FailedCount,
			&t.InputTokens, &t.OutputTokens, &t.ReasoningTokens,
			&t.TotalTokens, &t.CostUSD); err != nil {
			return nil, fmt.Errorf("postgres store: scan top row: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SelectTotals returns roll-up counts across the same filtered window. Useful
// for KPI cards (total requests / tokens / cost / failure rate).
func (s *UsageStore) SelectTotals(ctx context.Context, filter UsageFilter) (UsageAggregate, error) {
	if s == nil || s.db == nil {
		return UsageAggregate{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	var b strings.Builder
	b.WriteString(`SELECT 'total', NULL::text,
		COALESCE(SUM(CASE WHEN NOT failed THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN failed THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(reasoning_tokens), 0), COALESCE(SUM(cached_tokens), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0)
	FROM `)
	b.WriteString(s.eventsTable)
	args := buildWhereClause(&b, filter)
	row := s.db.QueryRowContext(ctx, b.String(), args...)
	var (
		a         UsageAggregate
		principal sql.NullString
	)
	if err := row.Scan(&a.Bucket, &principal, &a.RequestCount, &a.FailedCount,
		&a.InputTokens, &a.OutputTokens, &a.ReasoningTokens, &a.CachedTokens,
		&a.TotalTokens, &a.CostUSD); err != nil {
		return UsageAggregate{}, fmt.Errorf("postgres store: select usage totals: %w", err)
	}
	if principal.Valid {
		a.APIKeyPrincipal = principal.String
	}
	return a, nil
}

// buildWhereClause appends a parameterized WHERE clause to the supplied
// builder based on the parts of filter the caller populated. Returns the
// args slice (with new parameters appended) so callers can chain LIMIT.
func buildWhereClause(b *strings.Builder, filter UsageFilter) []any {
	b.WriteString(" WHERE 1=1")
	args := []any{}
	if filter.APIKeyID != "" {
		args = append(args, filter.APIKeyID)
		b.WriteString(" AND api_key_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Principal != "" {
		args = append(args, filter.Principal)
		b.WriteString(" AND api_key_principal = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Provider != "" {
		args = append(args, filter.Provider)
		b.WriteString(" AND provider = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Model != "" {
		args = append(args, filter.Model)
		b.WriteString(" AND model = $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		b.WriteString(" AND requested_at >= $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		b.WriteString(" AND requested_at < $")
		b.WriteString(itoa(len(args)))
	}
	return args
}

// intervalExpr maps a granularity name to a PG date_trunc expression.
func intervalExpr(interval string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "", "hour":
		return "date_trunc('hour', requested_at)", nil
	case "minute":
		return "date_trunc('minute', requested_at)", nil
	case "day":
		return "date_trunc('day', requested_at)", nil
	default:
		return "", fmt.Errorf("unsupported interval: %q (use minute|hour|day)", interval)
	}
}

// dimensionColumn returns the column reference for a top-N query dimension.
func dimensionColumn(dimension string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(dimension)) {
	case "model":
		return "model", nil
	case "provider":
		return "provider", nil
	case "api_key_id", "key", "apikey":
		return "api_key_id", nil
	case "principal", "api_key_principal":
		return "api_key_principal", nil
	default:
		return "", fmt.Errorf("unsupported dimension: %q", dimension)
	}
}

// metricExpr returns the SQL expression to order a top-N query by.
func metricExpr(metric string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(metric)) {
	case "", "request_count", "requests":
		return "request_count", nil
	case "total_tokens", "tokens":
		return "total_tokens", nil
	case "cost_usd", "cost":
		return "cost_usd", nil
	default:
		return "", fmt.Errorf("unsupported metric: %q", metric)
	}
}
