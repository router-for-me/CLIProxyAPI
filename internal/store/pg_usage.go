package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
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
	ID                  int64  `json:"id,omitempty"`
	RequestID           string `json:"request_id,omitempty"`
	APIKeyID            string `json:"api_key_id,omitempty"`
	APIKeyPrincipal     string `json:"api_key_principal,omitempty"`
	UserID              string `json:"user_id,omitempty"`
	Provider            string `json:"provider"`
	ExecutorType        string `json:"executor_type,omitempty"`
	Model               string `json:"model"`
	Alias               string `json:"alias,omitempty"`
	Endpoint            string `json:"endpoint,omitempty"`
	ClientIP            string `json:"client_ip,omitempty"`
	ForwardedFor        string `json:"forwarded_for,omitempty"`
	AuthType            string `json:"auth_type,omitempty"`
	Source              string `json:"source,omitempty"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	ServiceTier         string `json:"service_tier,omitempty"`
	ResponseServiceTier string `json:"response_service_tier,omitempty"`
	// Tier stores the Auto Router complexity tier (e.g. simple/medium/complex/
	// reasoning) chosen for the request. Empty for non-routed requests.
	Tier string `json:"tier,omitempty"`
	// RouterID stores the Auto Router id (e.g. "router:smart") that owned the
	// tier decision. Empty for non-routed requests.
	RouterID            string  `json:"router_id,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CachedTokens        int64   `json:"cached_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	CostUSD             float64 `json:"cost_usd"`
	// DiscountPct is the resolved model-group discount percentage (0-100)
	// applied to CostUSD when the requesting key's policy attaches to a model
	// group carrying a discount. The stored CostUSD is the post-discount value;
	// OriginalCostUSD (below) is the pre-discount figure stamped alongside.
	// 0 = no discount applied. Persisted column, stamped at write time.
	DiscountPct float64 `json:"discount_pct,omitempty"`
	// OriginalCostUSD is the pre-discount cost (before the model-group
	// discount was multiplied in). Stamped at flush time alongside DiscountPct
	// so the read-side never re-derives it (which would drift if the pricing
	// row changed between flush and read). Equals CostUSD when no discount was
	// applied. Persisted column.
	OriginalCostUSD float64   `json:"original_cost_usd,omitempty"`
	LatencyMs       int64     `json:"latency_ms,omitempty"`
	TTFTMs          int64     `json:"ttft_ms,omitempty"`
	Failed          bool      `json:"failed"`
	FailStatusCode  int       `json:"fail_status_code,omitempty"`
	Generate        bool      `json:"generate,omitempty"`
	RequestedAt     time.Time `json:"requested_at"`
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
	// RouterID narrows to events routed by a specific Auto Router id. Exact
	// match against usage_events.router_id; empty means no constraint.
	RouterID string
	UserID   string
	// RequestID narrows to a single per-request correlation identifier. Exact
	// match against usage_events.request_id / usage_errors.request_id; empty
	// means no constraint.
	RequestID string
	From      time.Time
	To        time.Time
	// GroupBy selects the aggregation dimension: "api_key_id" | "model" |
	// "provider" | "user_id" | "day" | "hour" | "" (no grouping, totals only).
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

// AutoRouterTierStat is one aggregated row per complexity tier for a router.
type AutoRouterTierStat struct {
	Tier         string  `json:"tier"`
	RequestCount int64   `json:"request_count"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// AutoRouterModelStat is one aggregated row per target model for a router.
type AutoRouterModelStat struct {
	Model         string  `json:"model"`
	RequestCount  int64   `json:"request_count"`
	TotalTokens   int64   `json:"total_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	AvgCostPerReq float64 `json:"avg_cost_per_request"`
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
	db                 *sql.DB
	apiKeysTable       string
	litellmKeysTable   string
	eventsTable        string
	errorsTable        string
	windowsTable       string
	rollupTable        string
	pricingTable       string
	internalUsersTable string
	modelsCatalogTable string
	// sealer encrypts api_key_principal at rest. nil when no passphrase was
	// configured (writes stay plaintext; reads tolerate plaintext rows).
	sealer *Sealer
	// cache short-TTL read-through store for aggregate usage reads. nil disables
	// caching. Never nil for stores built via NewUsageStore.
	cache *usageCache
}

// NewUsageStore builds a UsageStore from a PostgresStore connection. Returns
// nil when the parent store is nil so feature-detection is a single nil check.
// The sealer is derived from the parent's UsageEncryptionKey: when empty,
// encryption is disabled and the column is stored in plaintext.
func NewUsageStore(parent *PostgresStore) *UsageStore {
	if parent == nil {
		return nil
	}
	sealer, err := NewSealer(parent.cfg.UsageEncryptionKey)
	if err != nil {
		log.Printf("postgres store: usage encryption disabled due to key error: %v", err)
		sealer = nil
	}
	return &UsageStore{
		db:                 parent.DB(),
		apiKeysTable:       parent.APIKeysTable(),
		litellmKeysTable:   parent.LiteLLMKeysTable(),
		eventsTable:        parent.UsageEventsTable(),
		errorsTable:        parent.UsageErrorsTable(),
		windowsTable:       parent.UsageWindowsTable(),
		rollupTable:        parent.RollupTable(),
		pricingTable:       parent.ModelPricingTable(),
		internalUsersTable: parent.InternalUsersTable(),
		modelsCatalogTable: parent.ModelsTable(),
		sealer:             sealer,
		cache:              newUsageCache(),
	}
}

// SetSealer overrides the in-memory sealer. Used by tests that construct a
// UsageStore directly without going through NewPostgresStore. Production
// callers should rely on NewUsageStore instead.
func (s *UsageStore) SetSealer(sealer *Sealer) {
	if s == nil {
		return
	}
	s.sealer = sealer
}

// Sealer exposes the configured sealer (may be nil when encryption is
// disabled). Callers that need to seal/open fields outside the hot insert
// path (e.g. deriving a filter value) should reuse this.
func (s *UsageStore) Sealer() *Sealer {
	if s == nil {
		return nil
	}
	return s.sealer
}

// PricingTable returns the fully-qualified name of the model_pricing
// table backing this store. Used by callers that need to JOIN against it
// from outside the UsageStore API (e.g. the models catalog summary view).
func (s *UsageStore) PricingTable() string {
	if s == nil {
		return ""
	}
	return s.pricingTable
}

const usageEventColumnList = `
	request_id, api_key_id, api_key_principal, user_id, provider, executor_type, model,
	alias, endpoint, client_ip, forwarded_for, auth_type, source, reasoning_effort, service_tier,
	response_service_tier, tier, router_id, input_tokens, output_tokens, reasoning_tokens,
	cached_tokens, cache_creation_tokens, total_tokens, cost_usd, discount_pct, original_cost_usd, latency_ms,
	ttft_ms, failed, fail_status_code, generate, requested_at
`

// InsertEvent records a single usage event. The api_key_principal field is
// sealed at rest via the configured Sealer before being bound. When the
// sealer is nil (no passphrase configured), the value is stored as-is.
func (s *UsageStore) InsertEvent(ctx context.Context, e UsageEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if e.RequestedAt.IsZero() {
		e.RequestedAt = time.Now().UTC()
	}
	principal, err := s.sealer.Seal(e.APIKeyPrincipal)
	if err != nil {
		// Best-effort: drop the principal rather than failing the request,
		// but surface the error so operators can detect misconfiguration.
		log.WithError(err).Warn("postgres store: seal api_key_principal failed; persisting empty")
		principal = ""
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (%s) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25,
			$26, $27, $28, $29, $30, $31, $32, $33)
	`, s.eventsTable, usageEventColumnList),
		e.RequestID, nullableString(e.APIKeyID), nullableString(principal),
		nullableString(e.UserID),
		e.Provider, e.ExecutorType, e.Model, e.Alias, e.Endpoint,
		nullableString(e.ClientIP), nullableString(e.ForwardedFor),
		e.AuthType,
		e.Source, e.ReasoningEffort, e.ServiceTier, e.ResponseServiceTier,
		nullableString(e.Tier), nullableString(e.RouterID),
		e.InputTokens, e.OutputTokens, e.ReasoningTokens, e.CachedTokens,
		e.CacheCreationTokens, e.TotalTokens, e.CostUSD, e.DiscountPct, e.OriginalCostUSD, e.LatencyMs, e.TTFTMs,
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
	args := make([]any, 0, len(events)*33)
	for i, ev := range events {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		for j := 1; j <= 33; j++ {
			if j > 1 {
				b.WriteByte(',')
			}
			b.WriteByte('$')
			b.WriteString(itoa(i*33 + j))
		}
		b.WriteByte(')')
		if ev.RequestedAt.IsZero() {
			ev.RequestedAt = time.Now().UTC()
		}
		// Seal the principal once per event; failures fall back to an
		// empty value so a malformed row never aborts the whole batch.
		principal, err := s.sealer.Seal(ev.APIKeyPrincipal)
		if err != nil {
			log.WithError(err).Warn("postgres store: seal api_key_principal failed in batch; persisting empty")
			principal = ""
		}
		args = append(args, ev.RequestID, nullableString(ev.APIKeyID), nullableString(principal),
			nullableString(ev.UserID),
			ev.Provider, ev.ExecutorType, ev.Model, ev.Alias, ev.Endpoint,
			nullableString(ev.ClientIP), nullableString(ev.ForwardedFor),
			ev.AuthType,
			ev.Source, ev.ReasoningEffort, ev.ServiceTier, ev.ResponseServiceTier,
			nullableString(ev.Tier), nullableString(ev.RouterID),
			ev.InputTokens, ev.OutputTokens, ev.ReasoningTokens, ev.CachedTokens,
			ev.CacheCreationTokens, ev.TotalTokens, ev.CostUSD, ev.DiscountPct, ev.OriginalCostUSD, ev.LatencyMs, ev.TTFTMs,
			ev.Failed, ev.FailStatusCode, ev.Generate, ev.RequestedAt)
	}
	if _, err := s.db.ExecContext(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("postgres store: batch insert usage events: %w", err)
	}
	return nil
}

// ImportLiteLLMSpendLogs inserts historical spend-log events from an external
// LiteLLM into usage_events, deduplicating by request_id so re-running a
// migration is idempotent. Events without a request_id are skipped (they have
// no stable dedup key and would otherwise duplicate on every re-sync). Returns
// the number of rows actually inserted; rows already present are counted as
// zero. Batches are bounded to 100 rows to keep the multi-value INSERT small.
func (s *UsageStore) ImportLiteLLMSpendLogs(ctx context.Context, events []UsageEvent) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	const batchSize = 100
	imported := 0
	remaining := make([]UsageEvent, 0, len(events))
	for _, ev := range events {
		if strings.TrimSpace(ev.RequestID) != "" {
			remaining = append(remaining, ev)
		}
	}
	for start := 0; start < len(remaining); start += batchSize {
		end := start + batchSize
		if end > len(remaining) {
			end = len(remaining)
		}
		chunk := remaining[start:end]
		var b strings.Builder
		b.WriteString("INSERT INTO ")
		b.WriteString(s.eventsTable)
		b.WriteString(" (")
		b.WriteString(usageEventColumnList)
		b.WriteString(") VALUES ")
		args := make([]any, 0, len(chunk)*33)
		for i, ev := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('(')
			for j := 1; j <= 33; j++ {
				if j > 1 {
					b.WriteByte(',')
				}
				b.WriteByte('$')
				b.WriteString(itoa(i*33 + j))
			}
			b.WriteByte(')')
			if ev.RequestedAt.IsZero() {
				ev.RequestedAt = time.Now().UTC()
			}
			principal, err := s.sealer.Seal(ev.APIKeyPrincipal)
			if err != nil {
				log.WithError(err).Warn("postgres store: seal api_key_principal failed in spend-log import; persisting empty")
				principal = ""
			}
			args = append(args, ev.RequestID, nullableString(ev.APIKeyID), nullableString(principal),
				nullableString(ev.UserID),
				ev.Provider, ev.ExecutorType, ev.Model, ev.Alias, ev.Endpoint,
				nullableString(ev.ClientIP), nullableString(ev.ForwardedFor),
				ev.AuthType,
				ev.Source, ev.ReasoningEffort, ev.ServiceTier, ev.ResponseServiceTier,
				nullableString(ev.Tier), nullableString(ev.RouterID),
				ev.InputTokens, ev.OutputTokens, ev.ReasoningTokens, ev.CachedTokens,
				ev.CacheCreationTokens, ev.TotalTokens, ev.CostUSD, ev.DiscountPct, ev.OriginalCostUSD, ev.LatencyMs, ev.TTFTMs,
				ev.Failed, ev.FailStatusCode, ev.Generate, ev.RequestedAt)
		}
		b.WriteString(" ON CONFLICT (request_id) WHERE request_id IS NOT NULL AND request_id <> '' DO NOTHING")
		res, err := s.db.ExecContext(ctx, b.String(), args...)
		if err != nil {
			return imported, fmt.Errorf("postgres store: import litellm spend logs: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return imported, fmt.Errorf("postgres store: spend log import rows affected: %w", err)
		}
		imported += int(n)
	}
	return imported, nil
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
// ModelSpendForKey returns the total lifetime cost_usd a single API key has
// accrued for one model, summed on the fly from usage_events. Used by the
// policy service to enforce per-model max-budget caps on group-attached keys
// (mirrors the per-user GetModelSpend aggregation pattern). A missing model
// row returns 0, not an error. Failed requests still carry cost_usd when the
// upstream billed us, so they count toward the cap.
func (s *UsageStore) ModelSpendForKey(ctx context.Context, apiKeyID, model string) (float64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	if apiKeyID == "" || model == "" {
		return 0, fmt.Errorf("postgres store: ModelSpendForKey requires api_key_id and model")
	}
	var total float64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(cost_usd), 0)
		FROM %s
		WHERE api_key_id = $1 AND model = $2
	`, s.eventsTable), apiKeyID, model).Scan(&total); err != nil {
		return 0, fmt.Errorf("postgres store: select per-model spend for key: %w", err)
	}
	return total, nil
}

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
// (bucket, key_alias, request_count, failed_count, token sums, cost): the
// alias column is null when GROUP BY is not api_key_id (one bucket may
// contain many keys). The raw api_key_principal is sealed at rest and never
// projected; operators should resolve the human label via api_keys.key_alias.
func (s *UsageStore) SelectAggregate(ctx context.Context, filter UsageFilter) ([]UsageAggregate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	// Request-scoped reads are unique one-row lookups: bypass the cache.
	// RequestID is how the LiteLLM compat handlers drill into a single event.
	if s.cache == nil || UsageFilterIncludeRequestID(filter) {
		return s.selectAggregateMiss(ctx, filter)
	}
	return s.cache.getAggregate(ctx, filter, func() ([]UsageAggregate, error) {
		return s.selectAggregateMiss(ctx, filter)
	})
}

// selectAggregateMiss is the uncached SQL+scan path shared by SelectAggregate.
// It is the loader for the aggregate cache.
func (s *UsageStore) selectAggregateMiss(ctx context.Context, filter UsageFilter) ([]UsageAggregate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	// Fast path: when the aggregate can be answered exactly from the daily
	// rollup table (a day-aligned group-by-user query with no non-rollup
	// dimension filters), read it from usage_stat_day instead of scanning
	// every usage_events row in the window. This is the query behind LiteLLM's
	// /litellm/spend/users, which many users poll.
	//
	// NOTE on freshness: a day-aligned result reflects the last rollup fold, so
	// "today" is at most ~24h stale (folded on the daily cadence + startup
	// backfill). The 15s aggregate cache hides DB load but does not change this.
	// This is an accepted tradeoff — see docs/operations/postgres-sizing.md.
	if rows, used, err := s.tryRollupAggregateUser(ctx, filter); err != nil {
		return nil, err
	} else if used {
		return rows, nil
	}
	groupExpr, groupCol, err := aggregateGroupClause(filter.GroupBy)
	if err != nil {
		return nil, err
	}
	// Build the SELECT clause. We always project two leading columns for
	// Scan: bucket (TEXT) and principal (TEXT, nullable) — the latter is
	// populated from the api_keys.key_alias column via a LEFT JOIN so the
	// sealed api_key_principal never leaves the database. Both columns must
	// appear in GROUP BY when grouping is active.
	var b strings.Builder
	b.WriteString("SELECT ")
	if groupExpr == "" {
		b.WriteString("'total' AS bucket, NULL::text AS principal")
	} else {
		b.WriteString(groupExpr)
		b.WriteString(" AS bucket, MAX(COALESCE(NULLIF(k.key_alias, ''), k.name)) AS principal")
	}
	b.WriteString(`
		, COUNT(*) AS request_count,
		0 AS failed_count,
		COALESCE(SUM(input_tokens), 0) AS input_tokens,
		COALESCE(SUM(output_tokens), 0) AS output_tokens,
		COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
		COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(cost_usd), 0) AS cost_usd
	FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(" e LEFT JOIN ")
	b.WriteString(s.apiKeysTable)
	b.WriteString(" k ON k.id = e.api_key_id WHERE 1=1")
	args := []any{}
	if filter.APIKeyID != "" {
		args = append(args, filter.APIKeyID)
		b.WriteString(" AND e.api_key_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.UserID != "" {
		args = append(args, filter.UserID)
		b.WriteString(" AND e.user_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Provider != "" {
		args = append(args, filter.Provider)
		b.WriteString(" AND e.provider = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Model != "" {
		args = append(args, filter.Model)
		b.WriteString(" AND e.model = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.RouterID != "" {
		args = append(args, filter.RouterID)
		b.WriteString(" AND e.router_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.RequestID != "" {
		args = append(args, filter.RequestID)
		b.WriteString(" AND e.request_id = $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		b.WriteString(" AND e.requested_at >= $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		b.WriteString(" AND e.requested_at < $")
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

// rollupDayExpr reports whether t lands exactly on a UTC calendar-day boundary
// (00:00:00 UTC). Midnights truncate to themselves; any other instant does not.
func rollupDayExpr(t time.Time) bool {
	return t.UTC().Truncate(24*time.Hour) == t.UTC()
}

// rollupGroupByUserSQL is the rollup-backed GROUP BY user_id aggregate over a
// day-aligned window. It reads usage_stat_day (one row per (day,user,key,model,
// provider,source) bucket) and rolls each per-user row up into a UsageAggregate.
//
// Locator columns match the on-the-fly path so callers see identical shapes:
// bucket carries the user id, principal is NULL (a user spans many api_keys, so
// the api_keys LEFT JOIN is not applicable here). The rollup does not track
// reasoning_tokens or cached_tokens, so those project as 0; and failed_count,
// like the on-the-fly scan, deliberately projects 0 (the rollup's fail_count
// column holds real counts, but matching the existing on-the-fly behavior keeps
// the two paths consistent). So on the group-by-user fast path
// reasoning_tokens, cached_tokens, and failed_count are all 0.
//
// stat_day is a DATE pinned to UTC, so the window is inclusive on both ends
// (>= From_date AND <= To_date). Parameters are bound as explicit YYYY-MM-DD
// date strings cast to ::date — never as time.Time — so the comparison is
// timezone-independent and matches the UTC-pinned stat_day regardless of the
// session TIMEZONE.
const rollupGroupByUserSQL = `
SELECT r.user_id AS bucket,
       NULL::text AS principal,
       SUM(r.request_count) AS request_count,
       0                    AS failed_count,
       SUM(r.input_tokens)   AS input_tokens,
       SUM(r.output_tokens)  AS output_tokens,
       0                     AS reasoning_tokens,
       0                     AS cached_tokens,
       SUM(r.tot_tokens)     AS total_tokens,
       SUM(r.cost_usd)       AS cost_usd
FROM %s r
WHERE 1=1%s%s
  AND r.user_id <> ''
GROUP BY r.user_id
ORDER BY bucket%s`

// tryRollupAggregateUser answers a group-by-user aggregate from the daily rollup
// when the filter's window is day-aligned and carries no non-rollup dimension
// filters. It returns usedRollup=false (falling back to the usage_events scan)
// whenever the fast path cannot faithfully reproduce the query's result.
//
// Results from this fast path reflect the last rollup fold (whole-day-aligned),
// so "today" may be up to ~24h stale and is cached for the 15s aggregate-cache
// TTL. Accepted tradeoff — see docs/operations/postgres-sizing.md.
func (s *UsageStore) tryRollupAggregateUser(ctx context.Context, filter UsageFilter) ([]UsageAggregate, bool, error) {
	if s.rollupTable == "" {
		return nil, false, nil
	}
	// Only the group-by-user aggregation is fast-pathed; all other GroupBy
	// dimensions (api_key_id, model, provider, day, hour, "") keep the
	// on-the-fly usage_events path unchanged.
	if strings.TrimSpace(strings.ToLower(filter.GroupBy)) != "user_id" {
		return nil, false, nil
	}
	// The rollup buckets by all six dimensions, so any non-rollup dimension
	// filter needs a filtered scan — fall back. router_id is not tracked by the
	// rollup either, so a router-filtered query must scan usage_events directly.
	if filter.UserID != "" || filter.APIKeyID != "" || filter.Model != "" ||
		filter.Provider != "" || filter.RequestID != "" || filter.RouterID != "" {
		return nil, false, nil
	}
	// Day alignment: both bounds, when set, must be UTC calendar-day midnights.
	// A partial day (non-midnight From or exclusive To) cannot be answered from
	// whole-day rollup buckets, so it falls back for partial-day accuracy.
	if !filter.From.IsZero() && !rollupDayExpr(filter.From) {
		return nil, false, nil
	}
	if !filter.To.IsZero() && !rollupDayExpr(filter.To) {
		return nil, false, nil
	}

	from := ""
	if !filter.From.IsZero() {
		from = "\n  AND r.stat_day >= '" + filter.From.UTC().Format("2006-01-02") + "'"
	}
	to := ""
	if !filter.To.IsZero() {
		to = "\n  AND r.stat_day <= '" + filter.To.UTC().Format("2006-01-02") + "'"
	}
	limit := ""
	args := []any{}
	if filter.Limit > 0 {
		limit = " LIMIT $1"
		args = append(args, filter.Limit)
	}
	query := fmt.Sprintf(rollupGroupByUserSQL, s.rollupTable, from, to, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("postgres store: select rollup aggregate by user: %w", err)
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
			return nil, false, fmt.Errorf("postgres store: scan rollup aggregate by user: %w", err)
		}
		if principal.Valid {
			a.APIKeyPrincipal = principal.String
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres store: iterate rollup aggregate by user: %w", err)
	}
	return out, true, nil
}

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
		return "e.api_key_id", "e.api_key_id", nil
	case "model":
		return "e.model", "e.model", nil
	case "provider":
		return "e.provider", "e.provider", nil
	case "user_id", "user":
		return "e.user_id", "e.user_id", nil
	case "day":
		return "date_trunc('day', e.requested_at)", "date_trunc('day', e.requested_at)", nil
	case "hour":
		return "date_trunc('hour', e.requested_at)", "date_trunc('hour', e.requested_at)", nil
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

// HasRates reports whether the pricing row carries at least one non-zero
// rate. GetPricing returns an all-zero Pricing for both a missing row and an
// explicitly all-zero row, so this is the discriminator callers use to decide
// whether to fall back to an alias (see ResolvePricing).
func (p Pricing) HasRates() bool {
	return p.InputPer1M > 0 ||
		p.OutputPer1M > 0 ||
		p.CachedInputPer1M > 0 ||
		p.CachedReadPer1M > 0 ||
		p.ReasoningPer1M > 0
}

// ResolvePricing looks up the pricing row for model, falling back to alias
// when the model's row has no usable rate. Pricing rows are keyed by the
// client-facing/alias model id (the models_catalog id that /v1/models lists),
// but usage records carry the resolved upstream model as Model (e.g.
// "glm-5.2-flex") and the client-requested name as Alias (e.g. "glm-5.2").
// Looking up the resolved model alone misses when the row was authored under
// the alias, silently yielding cost=0. The alias fallback closes that gap.
//
// Semantics:
//   - model's row has a rate → returned as-is.
//   - model's row is all-zero (missing or free) and alias differs → alias's
//     row is tried; if it has a rate it wins, otherwise the original zero row
//     is returned (unchanged "missing pricing ⇒ 0" behavior).
//
// Safe to call on a nil store (returns zero Pricing, no error).
func (s *UsageStore) ResolvePricing(ctx context.Context, model, alias string) (Pricing, error) {
	if s == nil || s.db == nil {
		return Pricing{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	p, err := s.GetPricing(ctx, model)
	if err != nil {
		return Pricing{}, err
	}
	if p.HasRates() || alias == "" || alias == model {
		return p, nil
	}
	if pa, errAlias := s.GetPricing(ctx, alias); errAlias == nil && pa.HasRates() {
		return pa, nil
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
// Semantics (provider parsers normalize to these before persistence — see
// internal/runtime/executor/helps/usage_helpers.go):
//   - input:           billable non-cached prompt tokens. For OpenAI/Gemini
//     the provider folds cached tokens into prompt_tokens; the parser
//     subtracts them so input is the freshly-evaluated portion only.
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

// CostBreakdown is the per-segment dollar attribution emitted alongside a
// UsageEventRow. Each segment is the cost of one token kind evaluated against
// the applicable Pricing row, so the sum of all segments equals the event's
// total cost_usd (modulo floating-point rounding). A nil/missing Pricing row
// leaves every segment at 0 — the same behavior as ComputeCost.
type CostBreakdown struct {
	Input         float64 `json:"input"`
	Output        float64 `json:"output"`
	Reasoning     float64 `json:"reasoning"`
	CachedRead    float64 `json:"cached_read"`
	CacheCreation float64 `json:"cache_creation"`
}

// SegmentCosts returns each segment's dollar cost independently so the
// dashboard can render a per-segment breakdown bar. The mapping mirrors
// ComputeCost's billing model:
//   - input_tokens        × InputPer1M
//   - output_tokens       × OutputPer1M
//   - reasoning_tokens    × ReasoningPer1M
//   - cache_creation_tokens × CachedInputPer1M (cache write surcharge)
//   - cached_tokens       × CachedReadPer1M    (cache read discount)
//
// Provider parsers normalize token counts before persistence so the segment
// sum reconciles with both total_tokens (Σ segments == total_tokens) and
// cost_usd (Σ segments == ComputeCost). See internal/runtime/executor/helps/
// usage_helpers.go.
func SegmentCosts(p Pricing, input, output, reasoning, cachedRead, cacheCreation int64) CostBreakdown {
	perM := func(value float64, tokens int64) float64 {
		if tokens <= 0 || value <= 0 {
			return 0
		}
		return float64(tokens) * value / 1_000_000
	}
	return CostBreakdown{
		Input:         perM(p.InputPer1M, input),
		Output:        perM(p.OutputPer1M, output),
		Reasoning:     perM(p.ReasoningPer1M, reasoning),
		CachedRead:    perM(p.CachedReadPer1M, cachedRead),
		CacheCreation: perM(p.CachedInputPer1M, cacheCreation),
	}
}

// Sum returns the total dollar cost of all segments. Equal to ComputeCost for
// the same inputs (up to floating-point rounding).
func (b CostBreakdown) Sum() float64 {
	return b.Input + b.Output + b.Reasoning + b.CachedRead + b.CacheCreation
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
		COUNT(*) AS request_count,
		0 AS failed_count,
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(reasoning_tokens), 0), COALESCE(SUM(cached_tokens), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0)
	FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(" e")
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
// "api_key_id", "api_key_principal" (the last resolves to key_alias). metric
// must be one of: "request_count", "total_tokens", "cost_usd" (default
// request_count).
func (s *UsageStore) SelectTop(ctx context.Context, filter UsageFilter, dimension, metric string, limit int) ([]TopEntry, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	dimCol, joinExtra, err := dimensionColumn(dimension, s.apiKeysTable, s.internalUsersTable)
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
	b.WriteString("SELECT COALESCE(")
	b.WriteString(dimCol)
	b.WriteString(`, '') AS key,
		COUNT(*) AS request_count,
		0 AS failed_count,
		COALESCE(SUM(input_tokens), 0) AS input_tokens,
		COALESCE(SUM(output_tokens), 0) AS output_tokens,
		COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(cost_usd), 0) AS cost_usd
	FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(" e")
	if joinExtra != "" {
		b.WriteString(joinExtra)
	}
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
	// Request-scoped reads are unique one-row lookups: bypass the cache.
	if s.cache == nil || UsageFilterIncludeRequestID(filter) {
		return s.selectTotalsMiss(ctx, filter)
	}
	return s.cache.getTotals(ctx, filter, func() (UsageAggregate, error) {
		return s.selectTotalsMiss(ctx, filter)
	})
}

// selectTotalsMiss is the uncached SQL+scan path shared by SelectTotals. It is
// the loader for the totals cache.
func (s *UsageStore) selectTotalsMiss(ctx context.Context, filter UsageFilter) (UsageAggregate, error) {
	var b strings.Builder
	b.WriteString(`SELECT 'total', NULL::text,
		COUNT(*),
		0,
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(reasoning_tokens), 0), COALESCE(SUM(cached_tokens), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0)
	FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(" e")
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

// UsageEventRow is the dashboard-friendly projection of a single
// usage_events row. It mirrors UsageEvent but replaces the sealed
// api_key_principal with the non-secret KeyAlias (resolved via a LEFT JOIN
// on api_keys). The raw principal is never returned to API callers.
type UsageEventRow struct {
	ID        int64  `json:"id"`
	RequestID string `json:"request_id,omitempty"`
	APIKeyID  string `json:"api_key_id,omitempty"`
	KeyAlias  string `json:"key_alias,omitempty"`
	Provider  string `json:"provider"`
	// OfficialProvider is the official_provider label resolved from the models
	// catalog for this row's (provider key, model); populated best-effort by
	// the management layer's FillOfficialProvider. Empty when no catalog row
	// matches (file-only deployments, unknown model) — callers should fall back
	// to Provider. Never persisted in usage_events itself.
	OfficialProvider    string  `json:"official_provider,omitempty"`
	ExecutorType        string  `json:"executor_type,omitempty"`
	Model               string  `json:"model"`
	Alias               string  `json:"alias,omitempty"`
	Endpoint            string  `json:"endpoint,omitempty"`
	ClientIP            string  `json:"client_ip,omitempty"`
	ForwardedFor        string  `json:"forwarded_for,omitempty"`
	AuthType            string  `json:"auth_type,omitempty"`
	Source              string  `json:"source,omitempty"`
	ReasoningEffort     string  `json:"reasoning_effort,omitempty"`
	ServiceTier         string  `json:"service_tier,omitempty"`
	ResponseServiceTier string  `json:"response_service_tier,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CachedTokens        int64   `json:"cached_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	CostUSD             float64 `json:"cost_usd"`
	// DiscountPct is the resolved model-group discount percentage (0-100) that
	// was applied to CostUSD at flush time. 0 = no discount. Populated from the
	// persisted usage_events.discount_pct column.
	DiscountPct float64 `json:"discount_pct,omitempty"`
	// OriginalCostUSD is the pre-discount cost (before the model-group
	// discount was multiplied in), persisted at flush time alongside
	// DiscountPct so the dashboard shows an authoritative "was $X" figure
	// (never re-derived on read, so immune to pricing drift). Equals CostUSD
	// when no discount was applied.
	OriginalCostUSD float64 `json:"original_cost_usd,omitempty"`
	// CostBreakdown is populated only when the caller asks for it
	// (include=cost_breakdown on the events endpoints). It attributes
	// cost_usd to each token kind using the model's pricing row. Stays nil
	// otherwise so existing API consumers see no payload change.
	CostBreakdown *CostBreakdown `json:"cost_breakdown,omitempty"`
	// AppliedPricing is the resolved pricing row (unit rates) that produced
	// CostBreakdown. Populated alongside CostBreakdown so the dashboard can
	// render a tokens × rate → cost derivation. Present-but-all-zero when no
	// pricing row matched (i.e. "no pricing configured"), mirroring
	// CostBreakdown's missing-row semantics. Stays nil when CostBreakdown is
	// not requested so the list endpoint's payload is unchanged.
	AppliedPricing *Pricing  `json:"applied_pricing,omitempty"`
	LatencyMs      int64     `json:"latency_ms,omitempty"`
	TTFTMs         int64     `json:"ttft_ms,omitempty"`
	Failed         bool      `json:"failed"`
	FailStatusCode int       `json:"fail_status_code,omitempty"`
	Generate       bool      `json:"generate,omitempty"`
	RequestedAt    time.Time `json:"requested_at"`
}

// eventRowSelectColumns lists the columns projected by SelectEvents and
// GetEvent, in the order scanEventRow expects. KeyAlias is resolved via the
// api_keys LEFT JOIN. OfficialProvider is resolved via LEFT JOINs on
// models_catalog — see eventJoin for the resolution precedence. Rows that
// have no matching catalog row at all get an empty value here and are left
// to the best-effort registry resolver in the management layer.
// api_key_id is COALESCE'd to ” because it is nullable on usage_events
// (the flusher records only api_key_principal when no api_keys row can be
// resolved), and it is scanned into a plain string in scanEventRow — a raw
// NULL would fail the scan.
const eventRowSelectColumns = `
	e.id, e.request_id, COALESCE(e.api_key_id, ''),
	COALESCE(NULLIF(k.key_alias, ''), NULLIF(lk.key_alias, ''), lk.name, k.name, '') AS key_alias,
	e.provider, e.executor_type, e.model, e.alias, e.endpoint,
	e.client_ip, e.forwarded_for,
	e.auth_type,
	e.source, e.reasoning_effort, e.service_tier, e.response_service_tier,
	e.input_tokens, e.output_tokens, e.reasoning_tokens,
	e.cached_tokens, e.cache_creation_tokens, e.total_tokens, e.cost_usd,
	e.discount_pct,
	e.original_cost_usd,
	e.latency_ms, e.ttft_ms, e.failed, e.fail_status_code, e.generate,
	e.requested_at,
	COALESCE(mcAlias.official_provider, mcModel.official_provider, mcCompat.official_provider, '') AS official_provider
`

// eventJoin builds the FROM clause shared by SelectEvents and GetEvent. It
// joins api_keys (for the non-secret KeyAlias) and models_catalog three times
// to resolve official_provider deterministically from the persisted catalog,
// independent of the in-memory registry connection state:
//
//  1. mcAlias — (catalog id = COALESCE(event alias, event model),
//     catalog provider ILIKE event provider). Most built-in providers (claude,
//     gemini, antigravity, ...) persist catalog rows keyed by the alias / model
//     id with provider = OwnedBy == the internal provider key, so this exact
//     match is the common case. Using COALESCE(alias, model) handles "thinking"
//     model variants (e.model = "claude-opus-4-6-thinking") whose catalog row
//     is keyed by the alias "claude-opus-4-6".
//  2. mcModel — falls back to matching the raw event model id directly (for
//     rows where the alias differs but only the model id has a catalog row).
//  3. mcCompat — strips the "openai-compatible-" prefix from the internal key
//     and matches ILIKE against the catalog provider column. OpenAI-compatible
//     catalog rows are keyed by the compat name (e.g. "SemutSSH", "opencode"),
//     never the internal "openai-compatible-*" key, so this third arm resolves
//     events for OpenAI-compatible upstreams — including ones that have since
//     been deleted (the catalog row survives key deletion, so we still surface
//     the operator-edited official_provider rather than the internal key).
//
// Provider matching is LOWER()/LOWER() so capitalisation differences between
// the internal key / compat name and the catalog row never break resolution.
// All three arms seek the (id, provider) primary key, so the join stays an
// index lookup with no row amplification.
func (s *UsageStore) eventJoin() string {
	catalog := s.modelsCatalogTable
	// Join the Manage-LiteLLM keys table (litellm_api_keys) as a fallback alias
	// source: spend-log events imported by the LiteLLM sync carry the remote
	// key hash, which may only resolve in litellm_api_keys when the runtime
	// api_keys table has not been migrated yet. The key_alias column COALESCEs
	// runtime over litellm so the runtime label wins when both exist.
	return s.eventsTable + " e LEFT JOIN " + s.apiKeysTable + " k ON k.id = e.api_key_id" +
		" LEFT JOIN " + s.litellmKeysTable + " lk ON lk.id = e.api_key_id" +
		" LEFT JOIN " + catalog + " mcAlias ON mcAlias.id = COALESCE(NULLIF(e.alias, ''), e.model) AND LOWER(mcAlias.provider) = LOWER(e.provider)" +
		" LEFT JOIN " + catalog + " mcModel ON mcModel.id = e.model AND LOWER(mcModel.provider) = LOWER(e.provider)" +
		" LEFT JOIN " + catalog + " mcCompat ON mcCompat.id = COALESCE(NULLIF(e.alias, ''), e.model) AND LOWER(mcCompat.provider) = LOWER(REPLACE(e.provider, 'openai-compatible-', ''))"
}

// scanEventRow scans one row from the column order defined above. Shared by
// SelectEvents and GetEvent so the two stay in sync. client_ip and
// forwarded_for are nullable (added via idempotent ALTER; pre-existing rows
// carry NULL), so they are scanned into sql.NullString and then resolved to
// plain strings — mirroring how route_model is handled on the errors path.
func scanEventRow(scanner interface {
	Scan(dest ...any) error
}) (UsageEventRow, error) {
	var r UsageEventRow
	var clientIP, forwardedFor sql.NullString
	if err := scanner.Scan(
		&r.ID, &r.RequestID, &r.APIKeyID, &r.KeyAlias,
		&r.Provider, &r.ExecutorType, &r.Model, &r.Alias, &r.Endpoint,
		&clientIP, &forwardedFor,
		&r.AuthType,
		&r.Source, &r.ReasoningEffort, &r.ServiceTier, &r.ResponseServiceTier,
		&r.InputTokens, &r.OutputTokens, &r.ReasoningTokens, &r.CachedTokens,
		&r.CacheCreationTokens, &r.TotalTokens, &r.CostUSD, &r.DiscountPct, &r.OriginalCostUSD, &r.LatencyMs, &r.TTFTMs,
		&r.Failed, &r.FailStatusCode, &r.Generate, &r.RequestedAt,
		&r.OfficialProvider,
	); err != nil {
		return UsageEventRow{}, err
	}
	if clientIP.Valid {
		r.ClientIP = clientIP.String
	}
	if forwardedFor.Valid {
		r.ForwardedFor = forwardedFor.String
	}
	return r, nil
}

// SelectEvents returns a page of raw usage events, newest first, with the
// non-secret KeyAlias projected from the JOINed api_keys row. page is
// 1-indexed; pageSize is clamped to [1, 200]. The returned total reflects the
// same filter so the caller can render a pager.
func (s *UsageStore) SelectEvents(ctx context.Context, filter UsageFilter, page, pageSize int) ([]UsageEventRow, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	// Count first so the pager math is correct even when the page is empty.
	var b strings.Builder
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(s.eventJoin())
	countArgs := buildWhereClause(&b, filter)
	var total int64
	if err := s.db.QueryRowContext(ctx, b.String(), countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count usage events: %w", err)
	}

	b.Reset()
	b.WriteString("SELECT ")
	b.WriteString(eventRowSelectColumns)
	b.WriteString(" FROM ")
	b.WriteString(s.eventJoin())
	args := buildWhereClause(&b, filter)
	b.WriteString(" ORDER BY e.requested_at DESC, e.id DESC")
	args = append(args, pageSize, (page-1)*pageSize)
	b.WriteString(" LIMIT $")
	b.WriteString(itoa(len(args) - 1))
	b.WriteString(" OFFSET $")
	b.WriteString(itoa(len(args)))

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: select usage events: %w", err)
	}
	defer rows.Close()
	out := make([]UsageEventRow, 0, pageSize)
	for rows.Next() {
		r, err := scanEventRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan usage event row: %w", err)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// GetEvent returns a single usage event by its primary key. The principal
// column is replaced by KeyAlias (resolved via the api_keys LEFT JOIN).
func (s *UsageStore) GetEvent(ctx context.Context, id int64) (UsageEventRow, error) {
	if s == nil || s.db == nil {
		return UsageEventRow{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(eventRowSelectColumns)
	b.WriteString(" FROM ")
	b.WriteString(s.eventJoin())
	b.WriteString(" WHERE e.id = $1")
	row := s.db.QueryRowContext(ctx, b.String(), id)
	r, err := scanEventRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UsageEventRow{}, ErrUsageEventNotFound
		}
		return UsageEventRow{}, fmt.Errorf("postgres store: get usage event: %w", err)
	}
	return r, nil
}

// ErrUsageEventNotFound is returned by GetEvent when no row matches the id.
var ErrUsageEventNotFound = errors.New("postgres store: usage event not found")

// FillCostBreakdown populates the CostBreakdown field on every row by looking
// up the model's pricing row and applying SegmentCosts. Missing pricing rows
// leave the breakdown at a zero-value *CostBreakdown so the caller can
// distinguish "no price set" (breakdown present, all-zero) from "breakdown
// not requested" (nil pointer). The pricing lookup is deduplicated per model
// id, so a page of 25 events for the same model costs a single GetPricing
// round-trip. Safe to call on a nil store — returns nil immediately.
//
// Pricing is resolved via ResolvePricing(r.Model, r.Alias) so events whose row
// is keyed by the client-facing alias (not the resolved upstream model) still
// get a non-zero breakdown. The cache key is the (model, alias) pair so two
// events sharing a resolved model but differing in alias can't reuse a wrong
// alias's resolved row.
func (s *UsageStore) FillCostBreakdown(ctx context.Context, rows []UsageEventRow) error {
	if s == nil || s.db == nil || len(rows) == 0 {
		return nil
	}
	cache := make(map[string]*Pricing, len(rows))
	for i := range rows {
		r := &rows[i]
		key := r.Model + "\x00" + r.Alias
		p, ok := cache[key]
		if !ok {
			resolved, err := s.ResolvePricing(ctx, r.Model, r.Alias)
			if err != nil {
				return fmt.Errorf("postgres store: fill cost breakdown: %w", err)
			}
			p = &resolved
			cache[key] = p
		}
		b := SegmentCosts(*p, r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CachedTokens, r.CacheCreationTokens)
		r.CostBreakdown = &b
		// AppliedPricing mirrors CostBreakdown's present-but-all-zero semantics
		// for a missing pricing row so the dashboard can show "no pricing
		// configured" rather than conflating it with "free".
		// Note: OriginalCostUSD is read straight from the persisted
		// usage_events.original_cost_usd column (stamped at flush time) — no
		// re-derivation here, so it is immune to the pricing row drifting
		// between flush and read.
		r.AppliedPricing = p
	}
	return nil
}

// FilterOptions is the materialized set of distinct filter values for a usage
// window. The dashboard renders these as dropdown options so operators never
// have to type free-text filter values (which would be impossible against the
// sealed api_key_principal column anyway).
type FilterOptions struct {
	APIKeys   []APIKeyOption       `json:"api_keys"`
	Providers []string             `json:"providers"`
	Models    []string             `json:"models"`
	Users     []InternalUserOption `json:"users"`
}

// APIKeyOption pairs an api_key_id with its non-secret alias/name so the
// dashboard's API Key dropdown can show the human label while binding the
// id back into the filter.
type APIKeyOption struct {
	ID    string `json:"id"`
	Alias string `json:"alias"`
}

// InternalUserOption pairs an internal user id with its non-secret
// alias/email so the dashboard's Internal User dropdown can show the human
// label while binding the id back into the filter.
type InternalUserOption struct {
	ID    string `json:"id"`
	Alias string `json:"alias"`
}

// SelectFilterOptions returns the distinct api_keys / providers / models
// observed in usage_events for the supplied filter window. The api_keys list
// is sourced from a JOIN so it surfaces only keys that actually have usage in
// the window (rather than the full catalog).
func (s *UsageStore) SelectFilterOptions(ctx context.Context, filter UsageFilter) (FilterOptions, error) {
	if s == nil || s.db == nil {
		return FilterOptions{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	out := FilterOptions{}

	// Distinct API keys contributing events in the window, with alias/name.
	var b strings.Builder
	b.WriteString(`SELECT DISTINCT e.api_key_id, COALESCE(NULLIF(k.key_alias, ''), k.name, '') AS alias
		FROM `)
	b.WriteString(s.eventJoin())
	args := buildWhereClause(&b, filter)
	b.WriteString(" AND e.api_key_id IS NOT NULL AND e.api_key_id <> '' ORDER BY alias ASC")
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return FilterOptions{}, fmt.Errorf("postgres store: select usage filter api_keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var opt APIKeyOption
		if err = rows.Scan(&opt.ID, &opt.Alias); err != nil {
			return FilterOptions{}, fmt.Errorf("postgres store: scan usage filter api_keys: %w", err)
		}
		out.APIKeys = append(out.APIKeys, opt)
	}
	if err = rows.Err(); err != nil {
		return FilterOptions{}, err
	}

	// Distinct providers.
	b.Reset()
	b.WriteString("SELECT DISTINCT e.provider FROM ")
	b.WriteString(s.eventJoin())
	args = buildWhereClause(&b, filter)
	b.WriteString(" AND e.provider IS NOT NULL AND e.provider <> '' ORDER BY e.provider ASC")
	rows2, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return FilterOptions{}, fmt.Errorf("postgres store: select usage filter providers: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var p string
		if err = rows2.Scan(&p); err != nil {
			return FilterOptions{}, fmt.Errorf("postgres store: scan usage filter providers: %w", err)
		}
		out.Providers = append(out.Providers, p)
	}
	if err = rows2.Err(); err != nil {
		return FilterOptions{}, err
	}

	// Distinct models.
	b.Reset()
	b.WriteString("SELECT DISTINCT e.model FROM ")
	b.WriteString(s.eventJoin())
	args = buildWhereClause(&b, filter)
	b.WriteString(" AND e.model IS NOT NULL AND e.model <> '' ORDER BY e.model ASC")
	rows3, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return FilterOptions{}, fmt.Errorf("postgres store: select usage filter models: %w", err)
	}
	defer rows3.Close()
	for rows3.Next() {
		var m string
		if err = rows3.Scan(&m); err != nil {
			return FilterOptions{}, fmt.Errorf("postgres store: scan usage filter models: %w", err)
		}
		out.Models = append(out.Models, m)
	}
	if err = rows3.Err(); err != nil {
		return FilterOptions{}, err
	}

	// Distinct internal users contributing events in the window, with alias.
	b.Reset()
	b.WriteString(`SELECT DISTINCT e.user_id,
		COALESCE(NULLIF(u.user_alias, ''), u.user_email, e.user_id) AS alias
		FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(" e LEFT JOIN ")
	b.WriteString(s.internalUsersTable)
	b.WriteString(" u ON u.id = e.user_id")
	args = buildWhereClause(&b, filter)
	b.WriteString(" AND e.user_id IS NOT NULL AND e.user_id <> '' ORDER BY alias ASC")
	rows4, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return FilterOptions{}, fmt.Errorf("postgres store: select usage filter users: %w", err)
	}
	defer rows4.Close()
	for rows4.Next() {
		var opt InternalUserOption
		if err = rows4.Scan(&opt.ID, &opt.Alias); err != nil {
			return FilterOptions{}, fmt.Errorf("postgres store: scan usage filter users: %w", err)
		}
		out.Users = append(out.Users, opt)
	}
	if err = rows4.Err(); err != nil {
		return FilterOptions{}, err
	}
	return out, nil
}

// buildWhereClause appends a parameterized WHERE clause to the supplied
// builder based on the parts of filter the caller populated. Returns the
// args slice (with new parameters appended) so callers can chain LIMIT. This
// is shared by the usage_events read queries and by the usage_errors queries
// (pg_usage_errors.go), which operate on a table mirroring only the columns
// common to both. Only filter fields backed by a column present in BOTH
// tables (usage_events and usage_errors) may be applied here; columns unique
// to usage_events (e.g. router_id) must be filtered by the caller's own query
// instead of being widened into this shared clause.
//
// The Principal field is intentionally NOT applied: api_key_principal is
// sealed at rest and cannot be matched against a plaintext WHERE value.
// Operators should filter via APIKeyID (resolved in the dashboard from the
// alias-driven dropdown).
func buildWhereClause(b *strings.Builder, filter UsageFilter) []any {
	b.WriteString(" WHERE 1=1")
	args := []any{}
	if filter.APIKeyID != "" {
		args = append(args, filter.APIKeyID)
		b.WriteString(" AND e.api_key_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.UserID != "" {
		args = append(args, filter.UserID)
		b.WriteString(" AND e.user_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Provider != "" {
		args = append(args, filter.Provider)
		b.WriteString(" AND e.provider = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Model != "" {
		args = append(args, filter.Model)
		b.WriteString(" AND e.model = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.RequestID != "" {
		args = append(args, filter.RequestID)
		b.WriteString(" AND e.request_id = $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		b.WriteString(" AND e.requested_at >= $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		b.WriteString(" AND e.requested_at < $")
		b.WriteString(itoa(len(args)))
	}
	return args
}

// intervalExpr maps a granularity name to a PG date_trunc expression. The
// returned expression references the e alias used by all usage_events queries
// in this file.
func intervalExpr(interval string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "", "hour":
		return "date_trunc('hour', e.requested_at)", nil
	case "minute":
		return "date_trunc('minute', e.requested_at)", nil
	case "day":
		return "date_trunc('day', e.requested_at)", nil
	default:
		return "", fmt.Errorf("unsupported interval: %q (use minute|hour|day)", interval)
	}
}

// dimensionColumn returns the column reference for a top-N query dimension.
// The returned joinExtra (when non-empty) is extra LEFT JOIN clauses the
// caller must append after the `usage_events e` table. apiKeysTable and
// internalUsersTable are the fully-qualified table names the caller has
// resolved; either may be empty when its respective dimension is not used.
func dimensionColumn(dimension, apiKeysTable, internalUsersTable string) (column string, joinExtra string, err error) {
	switch strings.ToLower(strings.TrimSpace(dimension)) {
	case "model":
		return "e.model", "", nil
	case "provider":
		return "e.provider", "", nil
	case "api_key_id", "key", "apikey":
		return "e.api_key_id", "", nil
	case "principal", "api_key_principal":
		// Resolve to the human label (key_alias, falling back to name) via
		// the api_keys join. The raw api_key_principal column is sealed and
		// never projected to API callers.
		return "COALESCE(NULLIF(k.key_alias, ''), k.name)",
			" LEFT JOIN " + apiKeysTable + " k ON k.id = e.api_key_id", nil
	case "user_id", "user":
		// Resolve the internal user's alias (falling back to email/id) via the
		// internal_users join. The user_id column on usage_events is plaintext
		// and safe to GROUP BY directly.
		return "COALESCE(NULLIF(u.user_alias, ''), u.user_email, e.user_id)",
			" LEFT JOIN " + internalUsersTable + " u ON u.id = e.user_id", nil
	default:
		return "", "", fmt.Errorf("unsupported dimension: %q", dimension)
	}
}

// metricExpr returns the SQL aggregate expression to order a top-N query by.
// We return the underlying SUM(...) expression (rather than the SELECT alias)
// so PostgreSQL does not mis-resolve ORDER BY against the un-grouped base
// column (which triggers SQLSTATE 42803 when GROUP BY is active).
func metricExpr(metric string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(metric)) {
	case "", "request_count", "requests":
		return "COUNT(*)", nil
	case "total_tokens", "tokens":
		return "COALESCE(SUM(total_tokens), 0)", nil
	case "cost_usd", "cost":
		return "COALESCE(SUM(cost_usd), 0)", nil
	default:
		return "", fmt.Errorf("unsupported metric: %q", metric)
	}
}

// addUsageScopeArgs appends the api_key_id, user_id and time-range WHERE
// clauses shared by the auto-router aggregation queries; router_id is handled
// by each caller. Appends to args and grows b via the builder.
func addUsageScopeArgs(b *strings.Builder, args *[]any, filter UsageFilter) {
	if filter.APIKeyID != "" {
		*args = append(*args, filter.APIKeyID)
		b.WriteString(` AND e.api_key_id = $`)
		b.WriteString(itoa(len(*args)))
	}
	if filter.UserID != "" {
		*args = append(*args, filter.UserID)
		b.WriteString(` AND e.user_id = $`)
		b.WriteString(itoa(len(*args)))
	}
	if !filter.From.IsZero() {
		*args = append(*args, filter.From)
		b.WriteString(` AND e.requested_at >= $`)
		b.WriteString(itoa(len(*args)))
	}
	if !filter.To.IsZero() {
		*args = append(*args, filter.To)
		b.WriteString(` AND e.requested_at < $`)
		b.WriteString(itoa(len(*args)))
	}
}

// SelectAutoRouterTierStats returns per-tier request/token/cost totals for a
// router, optionally scoped by api_key_id and time range. It always returns
// the 4 canonical tiers; tiers with no requests are zero-valued. Events with a
// NULL tier (non-routed or pre-feature rows) are excluded.
func (s *UsageStore) SelectAutoRouterTierStats(ctx context.Context, filter UsageFilter) ([]AutoRouterTierStat, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	if strings.TrimSpace(filter.RouterID) == "" {
		return nil, fmt.Errorf("postgres store: router_id is required for tier stats")
	}
	out := []AutoRouterTierStat{
		{Tier: "simple"}, {Tier: "medium"}, {Tier: "complex"}, {Tier: "reasoning"},
	}
	var b strings.Builder
	b.WriteString(`SELECT e.tier,
		COUNT(*), COALESCE(SUM(e.total_tokens), 0), COALESCE(SUM(e.cost_usd), 0)
		FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(` e WHERE e.tier IS NOT NULL AND e.router_id = $1`)
	args := []any{filter.RouterID}
	addUsageScopeArgs(&b, &args, filter)
	b.WriteString(` GROUP BY e.tier ORDER BY e.tier`)
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select auto-router tier stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tier string
		var st AutoRouterTierStat
		if err := rows.Scan(&tier, &st.RequestCount, &st.TotalTokens, &st.CostUSD); err != nil {
			return nil, err
		}
		for i := range out {
			if out[i].Tier == tier {
				st.Tier = tier
				out[i] = st
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SelectAutoRouterModelStats returns per-target-model request/token/cost totals
// for a router, optionally scoped by api_key_id and time range, ordered by cost
// descending.
func (s *UsageStore) SelectAutoRouterModelStats(ctx context.Context, filter UsageFilter) ([]AutoRouterModelStat, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	if strings.TrimSpace(filter.RouterID) == "" {
		return nil, fmt.Errorf("postgres store: router_id is required for model stats")
	}
	var b strings.Builder
	b.WriteString(`SELECT e.model,
		COUNT(*), COALESCE(SUM(e.total_tokens), 0), COALESCE(SUM(e.cost_usd), 0)
		FROM `)
	b.WriteString(s.eventsTable)
	// Mirror SelectAutoRouterTierStats: restrict to events that were actually
	// routed (tier IS NOT NULL), so a non-routed event that happens to carry a
	// router_id (e.g. a future attribution change) cannot leak into the
	// per-target-model rollup.
	b.WriteString(` e WHERE e.tier IS NOT NULL AND e.router_id = $1`)
	args := []any{filter.RouterID}
	addUsageScopeArgs(&b, &args, filter)
	b.WriteString(` GROUP BY e.model ORDER BY COALESCE(SUM(e.cost_usd), 0) DESC`)
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		b.WriteString(` LIMIT $`)
		b.WriteString(itoa(len(args)))
	}
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select auto-router model stats: %w", err)
	}
	defer rows.Close()
	out := make([]AutoRouterModelStat, 0, 8)
	for rows.Next() {
		var st AutoRouterModelStat
		if err := rows.Scan(&st.Model, &st.RequestCount, &st.TotalTokens, &st.CostUSD); err != nil {
			return nil, err
		}
		if st.RequestCount > 0 {
			st.AvgCostPerReq = st.CostUSD / float64(st.RequestCount)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
