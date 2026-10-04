package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// UsageError is the persisted representation of a failed request attempt. It
// mirrors UsageEvent (minus the redundant Failed boolean) plus the upstream
// error_message captured from the in-memory usage.Record.Fail.Body that
// UsageEvent intentionally drops. Failed attempts live in usage_errors so
// the success-table aggregates (request_count, totals) stay clean of
// failures while still powering error drill-down and failure_rate.
type UsageError struct {
	ID              int64  `json:"id,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
	APIKeyID        string `json:"api_key_id,omitempty"`
	APIKeyPrincipal string `json:"api_key_principal,omitempty"`
	UserID          string `json:"user_id,omitempty"`
	Provider        string `json:"provider"`
	ExecutorType    string `json:"executor_type,omitempty"`
	Model           string `json:"model"`
	// ServedModel is the model the upstream response reported serving. It
	// differs from Model when the provider silently substituted a different
	// model; empty when the upstream did not report one.
	ServedModel         string  `json:"served_model,omitempty"`
	Alias               string  `json:"alias,omitempty"`
	RouteModel          string  `json:"route_model,omitempty"`
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
	// DiscountPct mirrors UsageEvent.DiscountPct: the resolved model-group
	// discount percentage applied to CostUSD at flush time. 0 = no discount.
	DiscountPct float64 `json:"discount_pct,omitempty"`
	// OriginalCostUSD is the pre-discount cost stamped at flush time alongside
	// DiscountPct (mirrors UsageEvent). Equals CostUSD when no discount was
	// applied. Persisted column.
	OriginalCostUSD float64   `json:"original_cost_usd,omitempty"`
	LatencyMs       int64     `json:"latency_ms,omitempty"`
	TTFTMs          int64     `json:"ttft_ms,omitempty"`
	NetworkRTTMs    int64     `json:"network_rtt_ms,omitempty"`
	FailStatusCode  int       `json:"fail_status_code,omitempty"`
	ErrorMessage    string    `json:"error_message,omitempty"`
	Generate        bool      `json:"generate,omitempty"`
	RequestedAt     time.Time `json:"requested_at"`
}

// UsageErrorRow is the dashboard-friendly projection of a single usage_errors
// row. It mirrors UsageEventRow but replaces the sealed api_key_principal with
// the non-secret KeyAlias (resolved via a LEFT JOIN on api_keys) and surfaces
// the error_message column instead of the redundant Failed boolean.
type UsageErrorRow struct {
	ID        int64  `json:"id"`
	RequestID string `json:"request_id,omitempty"`
	APIKeyID  string `json:"api_key_id,omitempty"`
	KeyAlias  string `json:"key_alias,omitempty"`
	Provider  string `json:"provider"`
	// OfficialProvider is the official_provider label resolved from the models
	// catalog for this row's (provider key, model); populated best-effort by
	// the management layer's FillOfficialProvider. Empty when no catalog row
	// matches (file-only deployments, unknown model) — callers should fall back
	// to Provider. Never persisted in usage_errors itself.
	OfficialProvider string `json:"official_provider,omitempty"`
	ExecutorType     string `json:"executor_type,omitempty"`
	Model            string `json:"model"`
	Alias            string `json:"alias,omitempty"`
	// ServedModel is the model the upstream response reported serving. It
	// differs from Model when the provider silently substituted a different
	// model; empty when the upstream did not report one.
	ServedModel         string  `json:"served_model,omitempty"`
	RouteModel          string  `json:"route_model,omitempty"`
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
	// DiscountPct mirrors UsageEventRow.DiscountPct: the resolved model-group
	// discount percentage applied to CostUSD at flush time. 0 = no discount.
	DiscountPct float64 `json:"discount_pct,omitempty"`
	// OriginalCostUSD is the pre-discount cost, persisted at flush time
	// alongside DiscountPct (mirrors UsageEventRow), so the dashboard shows
	// an authoritative "was $X" for failed attempts too. Equals CostUSD when
	// no discount was applied.
	OriginalCostUSD float64 `json:"original_cost_usd,omitempty"`
	// CostBreakdown is populated only when the caller asks for it
	// (include=cost_breakdown on the errors endpoints). Mirrors the
	// UsageEventRow breakdown so the dashboard can reuse the same card.
	CostBreakdown *CostBreakdown `json:"cost_breakdown,omitempty"`
	// AppliedPricing mirrors UsageEventRow.AppliedPricing: the resolved unit
	// rates that produced CostBreakdown, populated alongside it so the dashboard
	// can render a tokens × rate → cost derivation for failed attempts too.
	AppliedPricing *Pricing  `json:"applied_pricing,omitempty"`
	LatencyMs      int64     `json:"latency_ms,omitempty"`
	TTFTMs         int64     `json:"ttft_ms,omitempty"`
	NetworkRTTMs   int64     `json:"network_rtt_ms,omitempty"`
	FailStatusCode int       `json:"fail_status_code,omitempty"`
	ErrorMessage   string    `json:"error_message"`
	Generate       bool      `json:"generate,omitempty"`
	RequestedAt    time.Time `json:"requested_at"`
}

// usageErrorColumnList is the canonical column list for INSERT statements.
// Order must stay in sync with the positional args built by InsertError and
// BatchInsertErrors, and with errorRowSelectColumns used by SelectErrors /
// GetError (which additionally projects the joined key_alias).
const usageErrorColumnList = `
	request_id, api_key_id, api_key_principal, user_id, provider, executor_type, model, served_model,
	alias, route_model, endpoint, client_ip, forwarded_for, auth_type, source, reasoning_effort,
	service_tier, response_service_tier, input_tokens, output_tokens, reasoning_tokens,
	cached_tokens, cache_creation_tokens, total_tokens, cost_usd, discount_pct, original_cost_usd, latency_ms,
	ttft_ms, network_rtt_ms, fail_status_code, error_message, generate, requested_at
`

// usageErrorColumnCount is the number of columns in usageErrorColumnList. It
// must stay in sync with the list; mirrors usageEventColumnCount.
const usageErrorColumnCount = 34

// errorRowSelectColumns is the column list used by SelectErrors and GetError.
// The api_key_principal column is intentionally not projected; KeyAlias is
// resolved via the api_keys LEFT JOIN. OfficialProvider is resolved via the
// same three-arm models_catalog LEFT JOIN described on eventJoin, mirroring
// the events path so the two surfaces agree on official_provider.
// api_key_id is COALESCE'd to ” because it is nullable on usage_errors
// (see eventRowSelectColumns in pg_usage.go for the rationale) and is
// scanned into a plain string in scanErrorRow.
const errorRowSelectColumns = `
	e.id, e.request_id, COALESCE(e.api_key_id, ''),
	COALESCE(NULLIF(k.key_alias, ''), NULLIF(lk.key_alias, ''), lk.name, k.name, '') AS key_alias,
	e.provider, e.executor_type, e.model, e.alias, e.route_model, e.endpoint,
	e.served_model,
	e.client_ip, e.forwarded_for,
	e.auth_type,
	e.source, e.reasoning_effort, e.service_tier, e.response_service_tier,
	e.input_tokens, e.output_tokens, e.reasoning_tokens,
	e.cached_tokens, e.cache_creation_tokens, e.total_tokens, e.cost_usd,
	e.discount_pct,
	e.original_cost_usd,
	e.latency_ms, e.ttft_ms, e.fail_status_code, e.error_message, e.generate,
	e.requested_at,
	COALESCE(mcAlias.official_provider, mcModel.official_provider, mcCompat.official_provider, '') AS official_provider
`

// errorJoin mirrors eventJoin: see the documented resolution precedence there.
// Failed-attempt rows share the same provider/alias/model columns as events,
// so the three-arm catalog JOIN resolves official_provider identically.
func (s *UsageStore) errorJoin() string {
	catalog := s.modelsCatalogTable
	return s.errorsTable + " e LEFT JOIN " + s.apiKeysTable + " k ON k.id = e.api_key_id" +
		" LEFT JOIN " + s.litellmKeysTable + " lk ON lk.id = e.api_key_id" +
		" LEFT JOIN " + catalog + " mcAlias ON mcAlias.id = COALESCE(NULLIF(e.alias, ''), e.model) AND LOWER(mcAlias.provider) = LOWER(e.provider)" +
		" LEFT JOIN " + catalog + " mcModel ON mcModel.id = e.model AND LOWER(mcModel.provider) = LOWER(e.provider)" +
		" LEFT JOIN " + catalog + " mcCompat ON mcCompat.id = COALESCE(NULLIF(e.alias, ''), e.model) AND LOWER(mcCompat.provider) = LOWER(REPLACE(e.provider, 'openai-compatible-', ''))"
}

// scanErrorRow scans one row from the column order defined above. Shared by
// SelectErrors and GetError so the two stay in sync, mirroring scanEventRow.
// route_model, client_ip, and forwarded_for are nullable (added via idempotent
// ALTER; pre-existing rows carry NULL), so they are scanned into sql.NullString
// and then resolved to plain strings — mirroring how other nullable TEXT
// columns are handled elsewhere in the store.
func scanErrorRow(scanner interface {
	Scan(dest ...any) error
}) (UsageErrorRow, error) {
	var r UsageErrorRow
	var routeModel, servedModel, clientIP, forwardedFor sql.NullString
	if err := scanner.Scan(
		&r.ID, &r.RequestID, &r.APIKeyID, &r.KeyAlias,
		&r.Provider, &r.ExecutorType, &r.Model, &r.Alias, &routeModel, &r.Endpoint,
		&servedModel,
		&clientIP, &forwardedFor,
		&r.AuthType,
		&r.Source, &r.ReasoningEffort, &r.ServiceTier, &r.ResponseServiceTier,
		&r.InputTokens, &r.OutputTokens, &r.ReasoningTokens, &r.CachedTokens,
		&r.CacheCreationTokens, &r.TotalTokens, &r.CostUSD, &r.DiscountPct, &r.OriginalCostUSD, &r.LatencyMs, &r.TTFTMs,
		&r.FailStatusCode, &r.ErrorMessage, &r.Generate, &r.RequestedAt,
		&r.OfficialProvider,
	); err != nil {
		return UsageErrorRow{}, err
	}
	if routeModel.Valid {
		r.RouteModel = routeModel.String
	}
	if servedModel.Valid {
		r.ServedModel = servedModel.String
	}
	if clientIP.Valid {
		r.ClientIP = clientIP.String
	}
	if forwardedFor.Valid {
		r.ForwardedFor = forwardedFor.String
	}
	return r, nil
}

// InsertError records a single failed-attempt row. The api_key_principal field
// is sealed at rest via the configured Sealer before being bound, mirroring
// InsertEvent so both tables keep identical confidentiality semantics.
func (s *UsageStore) InsertError(ctx context.Context, e UsageError) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if e.RequestedAt.IsZero() {
		e.RequestedAt = time.Now().UTC()
	}
	principal, err := s.sealer.Seal(e.APIKeyPrincipal)
	if err != nil {
		log.WithError(err).Warn("postgres store: seal api_key_principal failed; persisting empty")
		principal = ""
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (%s) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25,
			$26, $27, $28, $29, $30, $31, $32, $33, $34)
	`, s.errorsTable, usageErrorColumnList),
		e.RequestID, nullableString(e.APIKeyID), nullableString(principal),
		nullableString(e.UserID),
		e.Provider, e.ExecutorType, e.Model, e.ServedModel, e.Alias, nullableString(e.RouteModel), e.Endpoint,
		nullableString(e.ClientIP), nullableString(e.ForwardedFor),
		e.AuthType,
		e.Source, e.ReasoningEffort, e.ServiceTier, e.ResponseServiceTier,
		e.InputTokens, e.OutputTokens, e.ReasoningTokens, e.CachedTokens,
		e.CacheCreationTokens, e.TotalTokens, e.CostUSD, e.DiscountPct, e.OriginalCostUSD, e.LatencyMs, e.TTFTMs,
		e.NetworkRTTMs,
		e.FailStatusCode, e.ErrorMessage, e.Generate, e.RequestedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres store: insert usage error: %w", err)
	}
	return nil
}

// BatchInsertErrors records up to len(errors) failed-attempt rows in a single
// multi-value INSERT statement. Mirrors BatchInsertEvents so the flusher can
// coalesce error rows in the same batch as success rows.
func (s *UsageStore) BatchInsertErrors(ctx context.Context, errors []UsageError) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if len(errors) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(s.errorsTable)
	b.WriteString(" (")
	b.WriteString(usageErrorColumnList)
	b.WriteString(") VALUES ")
	args := make([]any, 0, len(errors)*usageErrorColumnCount)
	for i, ev := range errors {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		for j := 1; j <= usageErrorColumnCount; j++ {
			if j > 1 {
				b.WriteByte(',')
			}
			b.WriteByte('$')
			b.WriteString(itoa(i*usageErrorColumnCount + j))
		}
		b.WriteByte(')')
		if ev.RequestedAt.IsZero() {
			ev.RequestedAt = time.Now().UTC()
		}
		principal, err := s.sealer.Seal(ev.APIKeyPrincipal)
		if err != nil {
			log.WithError(err).Warn("postgres store: seal api_key_principal failed in batch; persisting empty")
			principal = ""
		}
		args = append(args, ev.RequestID, nullableString(ev.APIKeyID), nullableString(principal),
			nullableString(ev.UserID),
			ev.Provider, ev.ExecutorType, ev.Model, ev.ServedModel, ev.Alias, nullableString(ev.RouteModel), ev.Endpoint,
			nullableString(ev.ClientIP), nullableString(ev.ForwardedFor),
			ev.AuthType,
			ev.Source, ev.ReasoningEffort, ev.ServiceTier, ev.ResponseServiceTier,
			ev.InputTokens, ev.OutputTokens, ev.ReasoningTokens, ev.CachedTokens,
			ev.CacheCreationTokens, ev.TotalTokens, ev.CostUSD, ev.DiscountPct, ev.OriginalCostUSD, ev.LatencyMs, ev.TTFTMs,
			ev.NetworkRTTMs,
			ev.FailStatusCode, ev.ErrorMessage, ev.Generate, ev.RequestedAt)
	}
	if _, err := s.db.ExecContext(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("postgres store: batch insert usage errors: %w", err)
	}
	return nil
}

// ImportLiteLLMErrors inserts historical failed-attempt rows from an external
// LiteLLM spend-log sync into usage_errors, deduplicating by request_id so
// re-running a migration is idempotent. Unlike usage_events, usage_errors has
// no unique index on request_id (the runtime flusher can legitimately record
// the same request_id more than once), so dedup is done in the store: rows
// whose request_id already exists are skipped. Rows without a request_id have
// no stable dedup key and are skipped too. Returns the number of rows actually
// inserted; batches are bounded to 100 rows to keep the multi-value INSERT small.
func (s *UsageStore) ImportLiteLLMErrors(ctx context.Context, errs []UsageError) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	const batchSize = 100
	imported := 0
	remaining := make([]UsageError, 0, len(errs))
	for _, e := range errs {
		if strings.TrimSpace(e.RequestID) != "" {
			remaining = append(remaining, e)
		}
	}
	for start := 0; start < len(remaining); start += batchSize {
		end := start + batchSize
		if end > len(remaining) {
			end = len(remaining)
		}
		chunk := remaining[start:end]
		// Find which request_ids already exist so we skip them (idempotent).
		existing := map[string]bool{}
		{
			ids := make([]string, 0, len(chunk))
			for _, e := range chunk {
				ids = append(ids, e.RequestID)
			}
			rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
				SELECT request_id FROM %s WHERE request_id = ANY($1::text[])
			`, s.errorsTable), pqStringArray(ids))
			if err != nil {
				return imported, fmt.Errorf("postgres store: check existing usage error request_ids: %w", err)
			}
			for rows.Next() {
				var rid string
				if err := rows.Scan(&rid); err != nil {
					rows.Close()
					return imported, fmt.Errorf("postgres store: scan existing usage error request_id: %w", err)
				}
				existing[rid] = true
			}
			rows.Close()
		}
		fresh := make([]UsageError, 0, len(chunk))
		seen := map[string]bool{}
		for _, e := range chunk {
			if existing[e.RequestID] || seen[e.RequestID] {
				continue
			}
			seen[e.RequestID] = true
			fresh = append(fresh, e)
		}
		if len(fresh) == 0 {
			continue
		}
		var b strings.Builder
		b.WriteString("INSERT INTO ")
		b.WriteString(s.errorsTable)
		b.WriteString(" (")
		b.WriteString(usageErrorColumnList)
		b.WriteString(") VALUES ")
		args := make([]any, 0, len(fresh)*usageErrorColumnCount)
		for i, ev := range fresh {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('(')
			for j := 1; j <= usageErrorColumnCount; j++ {
				if j > 1 {
					b.WriteByte(',')
				}
				b.WriteByte('$')
				b.WriteString(itoa(i*usageErrorColumnCount + j))
			}
			b.WriteByte(')')
			if ev.RequestedAt.IsZero() {
				ev.RequestedAt = time.Now().UTC()
			}
			principal, err := s.sealer.Seal(ev.APIKeyPrincipal)
			if err != nil {
				log.WithError(err).Warn("postgres store: seal api_key_principal failed in error import; persisting empty")
				principal = ""
			}
			args = append(args, ev.RequestID, nullableString(ev.APIKeyID), nullableString(principal),
				nullableString(ev.UserID),
				ev.Provider, ev.ExecutorType, ev.Model, ev.ServedModel, ev.Alias, nullableString(ev.RouteModel), ev.Endpoint,
				nullableString(ev.ClientIP), nullableString(ev.ForwardedFor),
				ev.AuthType,
				ev.Source, ev.ReasoningEffort, ev.ServiceTier, ev.ResponseServiceTier,
				ev.InputTokens, ev.OutputTokens, ev.ReasoningTokens, ev.CachedTokens,
				ev.CacheCreationTokens, ev.TotalTokens, ev.CostUSD, ev.DiscountPct, ev.OriginalCostUSD, ev.LatencyMs, ev.TTFTMs,
				ev.NetworkRTTMs,
				ev.FailStatusCode, ev.ErrorMessage, ev.Generate, ev.RequestedAt)
		}
		res, err := s.db.ExecContext(ctx, b.String(), args...)
		if err != nil {
			return imported, fmt.Errorf("postgres store: import litellm usage errors: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return imported, fmt.Errorf("postgres store: import litellm usage errors rows affected: %w", err)
		}
		imported += int(n)
	}
	return imported, nil
}

// SelectErrors returns a page of raw failed-attempt rows, newest first, with
// the non-secret KeyAlias projected from the JOINed api_keys row. page is
// 1-indexed; pageSize is clamped to [1, 200]. The returned total reflects the
// same filter so the caller can render a pager. Mirrors SelectEvents.
func (s *UsageStore) SelectErrors(ctx context.Context, filter UsageFilter, page, pageSize int) ([]UsageErrorRow, int64, error) {
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
	var b strings.Builder
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(s.errorJoin())
	countArgs := buildWhereClause(&b, filter)
	var total int64
	if err := s.db.QueryRowContext(ctx, b.String(), countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count usage errors: %w", err)
	}

	b.Reset()
	b.WriteString("SELECT ")
	b.WriteString(errorRowSelectColumns)
	b.WriteString(" FROM ")
	b.WriteString(s.errorJoin())
	args := buildWhereClause(&b, filter)
	b.WriteString(" ORDER BY e.requested_at DESC, e.id DESC")
	args = append(args, pageSize, (page-1)*pageSize)
	b.WriteString(" LIMIT $")
	b.WriteString(itoa(len(args) - 1))
	b.WriteString(" OFFSET $")
	b.WriteString(itoa(len(args)))

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: select usage errors: %w", err)
	}
	defer rows.Close()
	out := make([]UsageErrorRow, 0, pageSize)
	for rows.Next() {
		r, err := scanErrorRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan usage error row: %w", err)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// GetError returns a single failed-attempt row by its primary key. The
// principal column is replaced by KeyAlias (resolved via the api_keys LEFT
// JOIN). Mirrors GetEvent.
func (s *UsageStore) GetError(ctx context.Context, id int64) (UsageErrorRow, error) {
	if s == nil || s.db == nil {
		return UsageErrorRow{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(errorRowSelectColumns)
	b.WriteString(" FROM ")
	b.WriteString(s.errorJoin())
	b.WriteString(" WHERE e.id = $1")
	row := s.db.QueryRowContext(ctx, b.String(), id)
	r, err := scanErrorRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UsageErrorRow{}, ErrUsageErrorNotFound
		}
		return UsageErrorRow{}, fmt.Errorf("postgres store: get usage error: %w", err)
	}
	return r, nil
}

// ErrUsageErrorNotFound is returned by GetError when no row matches the id.
var ErrUsageErrorNotFound = errors.New("postgres store: usage error not found")

// FillCostBreakdownErrors populates the CostBreakdown field on every error row
// by looking up the model's pricing row, mirroring FillCostBreakdown for
// events. Safe to call on a nil store — returns nil immediately.
//
// Pricing is resolved via ResolvePricing(r.Model, r.Alias) (alias fallback);
// the cache key is the (model, alias) pair, mirroring FillCostBreakdown.
func (s *UsageStore) FillCostBreakdownErrors(ctx context.Context, rows []UsageErrorRow) error {
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
				return fmt.Errorf("postgres store: fill cost breakdown for errors: %w", err)
			}
			p = &resolved
			cache[key] = p
		}
		b := SegmentCosts(*p, r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CachedTokens, r.CacheCreationTokens)
		r.CostBreakdown = &b
		// AppliedPricing mirrors FillCostBreakdown's present-but-all-zero
		// missing-row behavior so the dashboard renders the same derivation
		// card for failed attempts.
		// Note: OriginalCostUSD is read straight from the persisted
		// usage_errors.original_cost_usd column (stamped at flush time) — no
		// re-derivation here.
		r.AppliedPricing = p
	}
	return nil
}

// SelectErrorCount returns the number of failed-attempt rows matching the
// supplied filter. The handler layers this on top of SelectTotals so the
// totals payload's failed_count reflects usage_errors rather than the (now
// failure-free) usage_events table. Uses buildWhereClause so the filter shape
// matches the success-table queries verbatim.
func (s *UsageStore) SelectErrorCount(ctx context.Context, filter UsageFilter) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	var b strings.Builder
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(s.errorsTable)
	b.WriteString(" e")
	args := buildWhereClause(&b, filter)
	var count int64
	if err := s.db.QueryRowContext(ctx, b.String(), args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres store: count usage errors: %w", err)
	}
	return count, nil
}

// SelectErrorAggregate runs a grouped count query over usage_errors, used to
// derive the failed_count column for time-series and top-N responses. Only
// the key/bucket + failed_count projection is meaningful here (token/cost
// sums for failed attempts are populated from usage_events-side aggregations
// if needed by callers; for failure_rate purposes only the count matters).
//
// GroupBy semantics mirror SelectAggregate: "" / "total" yields a single row
// whose Bucket is "total"; "day"/"hour" yield a per-bucket count suitable for
// aligning with SelectTimeSeries buckets; "api_key_id"/"model"/"provider"/
// "user_id" yield per-dimension counts for top-N alignment.
func (s *UsageStore) SelectErrorAggregate(ctx context.Context, filter UsageFilter) ([]UsageAggregate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	groupExpr, groupCol, err := aggregateGroupClause(filter.GroupBy)
	if err != nil {
		return nil, err
	}
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
		COUNT(*) AS failed_count,
		0 AS input_tokens, 0 AS output_tokens, 0 AS reasoning_tokens,
		0 AS cached_tokens, 0 AS total_tokens, 0 AS cost_usd
	FROM `)
	b.WriteString(s.errorsTable)
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
		return nil, fmt.Errorf("postgres store: select usage error aggregate: %w", err)
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
			return nil, fmt.Errorf("postgres store: scan usage error aggregate: %w", err)
		}
		if principal.Valid {
			a.APIKeyPrincipal = principal.String
		}
		// For error aggregates only FailedCount is meaningful; RequestCount
		// is reused to carry the error-row count so callers reading the totals
		// slice for failure_rate pick up the right value.
		a.FailedCount = a.RequestCount
		out = append(out, a)
	}
	return out, rows.Err()
}

// SelectErrorTimeSeries returns per-bucket failed-attempt counts aligned with
// SelectTimeSeries so the dashboard can plot errors on the same axis as
// successes. Intervals mirror SelectTimeSeries: "minute"/"hour"/"day"
// (default hour).
func (s *UsageStore) SelectErrorTimeSeries(ctx context.Context, filter UsageFilter, interval string) ([]UsageTimeSeriesPoint, error) {
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
		0 AS request_count,
		COUNT(*) AS failed_count,
		0, 0, 0, 0, 0, 0
	FROM `)
	b.WriteString(s.errorsTable)
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
		return nil, fmt.Errorf("postgres store: select usage error timeseries: %w", err)
	}
	defer rows.Close()
	out := make([]UsageTimeSeriesPoint, 0, 32)
	for rows.Next() {
		var p UsageTimeSeriesPoint
		var bucket time.Time
		if err = rows.Scan(&bucket, &p.BucketTimestamp, &p.RequestCount, &p.FailedCount,
			&p.InputTokens, &p.OutputTokens, &p.ReasoningTokens, &p.CachedTokens,
			&p.TotalTokens, &p.CostUSD); err != nil {
			return nil, fmt.Errorf("postgres store: scan usage error timeseries row: %w", err)
		}
		p.Bucket = bucket.UTC().Format(time.RFC3339)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SelectErrorTop returns the top-N dimensions ordered by failed-attempt count,
// mirroring SelectTop so the dashboard leaderboards can surface failure
// distribution. The TopEntry.RequestCount field carries the error-row count
// (since for an errors-only aggregation "requests" and "failures" coincide);
// FailedCount is set to the same value for shape compatibility.
func (s *UsageStore) SelectErrorTop(ctx context.Context, filter UsageFilter, dimension string, limit int) ([]TopEntry, error) {
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
	var b strings.Builder
	b.WriteString("SELECT COALESCE(")
	b.WriteString(dimCol)
	b.WriteString(`, '') AS key,
		COUNT(*) AS request_count,
		COUNT(*) AS failed_count,
		0 AS input_tokens, 0 AS output_tokens, 0 AS reasoning_tokens,
		0 AS total_tokens, 0 AS cost_usd
	FROM `)
	b.WriteString(s.errorsTable)
	b.WriteString(" e")
	if joinExtra != "" {
		b.WriteString(joinExtra)
	}
	args := buildWhereClause(&b, filter)
	b.WriteString(" GROUP BY key ORDER BY request_count DESC, key ASC")
	args = append(args, limit)
	b.WriteString(" LIMIT $")
	b.WriteString(itoa(len(args)))
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select usage error top: %w", err)
	}
	defer rows.Close()
	out := make([]TopEntry, 0, limit)
	for rows.Next() {
		var t TopEntry
		if err = rows.Scan(&t.Key, &t.RequestCount, &t.FailedCount,
			&t.InputTokens, &t.OutputTokens, &t.ReasoningTokens,
			&t.TotalTokens, &t.CostUSD); err != nil {
			return nil, fmt.Errorf("postgres store: scan top error row: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
