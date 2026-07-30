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

// Model Health Check status labels. Mirrored on the dashboard so an operator
// can distinguish a healthy probe from a slow/degraded one or a failed one.
const (
	ModelHealthStatusOperational = "operational"
	ModelHealthStatusDegraded    = "degraded"
	ModelHealthStatusUnavailable = "unavailable"
	ModelHealthStatusUnknown     = "unknown"
)

// modelHealthLogRetention is the default time-based retention for the
// model_health_log history table: rows older than this window are purged by
// the retention sweep. Operators can override it via the RetentionDays
// setting (0 disables time-based pruning). Used as the fallback default.
const modelHealthLogRetention = 30 * 24 * time.Hour

// defaultModelHealthRetentionDays mirrors modelHealthLogRetention in days so
// the settings struct carries a stable, human-editable value.
const defaultModelHealthRetentionDays = 30

// MinModelHealthRetentionDays is the floor for the operator-configured
// RetentionDays. A cap below 1 day would prune too aggressively against a
// sweep that may run hourly. 1 day is the sensible minimum. Exported so the
// management handler can reject sub-floor values with a clear 400.
// 0 is reserved for "no time-based pruning".
const MinModelHealthRetentionDays = 1

// MaxModelHealthRetentionDays is the ceiling for RetentionDays, guarding
// against an accidental typo that would retain rows essentially forever and
// grow the table unbounded. Exported for the management handler validation.
const MaxModelHealthRetentionDays = 365

// MinModelHealthMaxLogRows is the floor for the operator-configured
// MaxLogRows (the per-model row cap). A value below ~10 would prune history
// almost as fast as it is written, so 10 is the sensible minimum.
// 0 is reserved for "unlimited".
const MinModelHealthMaxLogRows = 10

// MaxModelHealthMaxLogRows is the ceiling for MaxLogRows to prevent an
// accidental typo from disabling per-model pruning entirely.
const MaxModelHealthMaxLogRows = 100_000

// defaultModelHealthMaxLogRows is the default per-model history row cap. At
// a 15-minute probe cadence this keeps roughly the last ~10 days per model.
const defaultModelHealthMaxLogRows = 1000

// defaultModelHealthInterval is the probe cadence applied when the operator
// has not yet configured an interval (or set a non-positive value). The sweep
// re-reads settings each tick so changes take effect without a restart.
const defaultModelHealthInterval = 15 * time.Minute

// MinModelHealthInterval is the floor for the operator-configured interval.
// A cap prevents operators from probing upstreams so often it becomes abusive
// to provider rate limits / quota. Exported so management handlers can reject
// sub-floor intervals with a clear 400 before reaching the store.
const MinModelHealthInterval = 5 * time.Minute

// MaxModelHealthTokens is the ceiling for the operator-configured max_tokens
// per probe. Generating more tokens produces a stabler tokens-per-second
// measurement but costs more upstream quota per check; the cap keeps a single
// config typo from burning through quota. Exported so the management handler
// can reject an over-cap value with a clear 400 before reaching the store.
const MaxModelHealthTokens = 1024

// maxModelHealthPromptBytes caps the stored prompt_message text so a single
// verbose probe request cannot bloat the snapshot/history rows unbounded. The
// final sealing happens in the store; truncation is applied first.
const maxModelHealthPromptBytes = 8 * 1024

// maxModelHealthCompletionBytes caps the stored completion text (the model's
// generated content) — symmetric to maxModelHealthPromptBytes.
const maxModelHealthCompletionBytes = 8 * 1024

// ModelHealthRow mirrors a row in the model_health snapshot table (one row per
// model id) and, with an ID, a row in the model_health_log history table. It
// records the outcome of a single health-check probe: which model was probed,
// whether it succeeded, the response time, the measured tokens-per-second, the
// token usage, the error message (if any), the actual prompt/completion text,
// and the upstream provider that served the probe.
//
// ErrorMessage, PromptMessage, and Completion are AES-GCM-sealed at rest when
// PGSTORE_ENCRYPTION_KEY is configured (see Sealer); otherwise they are
// persisted in plaintext (legacy rows remain readable, forward-encrypting).
type ModelHealthRow struct {
	ID               int64    `json:"id,omitempty"`
	ModelID          string   `json:"model_id"`
	Status           string   `json:"status"`
	Success          bool     `json:"success"`
	ResponseTimeMs   int64    `json:"response_time_ms"`
	TokensPerSecond  *float64 `json:"tokens_per_second,omitempty"`
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	ErrorMessage     string   `json:"error_message,omitempty"`
	Provider         string   `json:"provider,omitempty"`
	// PromptMessage is the actual probe prompt text (truncated to
	// maxModelHealthPromptBytes). Sealed at rest.
	PromptMessage string `json:"prompt_message,omitempty"`
	// Completion is the model-generated completion text extracted from the
	// probe response (truncated to maxModelHealthCompletionBytes). Sealed.
	Completion string `json:"completion,omitempty"`
	// UpstreamProvider, UpstreamAuthID, UpstreamModel record which upstream
	// credential served the probe and the translated model it used — i.e. the
	// detail behind the "provider" summary field. Surfaced in the detail modal
	// so an operator can correlate a health outcome to a specific credential.
	UpstreamProvider string    `json:"upstream_provider,omitempty"`
	UpstreamAuthID   string    `json:"upstream_auth_id,omitempty"`
	UpstreamModel    string    `json:"upstream_model,omitempty"`
	CheckedAt        time.Time `json:"checked_at"`
}

// ModelHealthLogFilter captures the optional filter dimensions accepted by
// ListLogPaged. Zero values (and empty strings / nil pointers) are ignored.
type ModelHealthLogFilter struct {
	ModelID string
	Status  string
	// Success filters by outcome. nil = both success and failure rows.
	Success *bool
	From    time.Time
	To      time.Time
}

// ModelHealthSettings mirrors the singleton operator configuration row for the
// model health check sweep. ExcludedModels is the list of model ids (already
// normalized: trimmed) skipped entirely by the sweep.
//
// RetentionDays and MaxLogRows bound how long history rows are kept:
//   - RetentionDays: time-based. Rows older than this many days are purged by
//     the hourly retention sweep. 0 disables time-based pruning.
//   - MaxLogRows: count-based, per model id. The sweep trims each model's
//     history to the newest N rows. 0 disables the per-model cap.
//
// Both are enforced on the same hourly sweep that re-reads settings, so
// changes take effect on the next tick without a restart.
type ModelHealthSettings struct {
	Enabled         bool     `json:"enabled"`
	IntervalSeconds int      `json:"interval_seconds"`
	ExcludedModels  []string `json:"excluded_models"`
	MaxTokens       int      `json:"max_tokens"`
	// RetentionDays bounds the model_health_log age. 0 = no time-based pruning.
	RetentionDays int `json:"retention_days"`
	// MaxLogRows is the per-model history row cap. 0 = unlimited.
	MaxLogRows int       `json:"max_log_rows"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ModelHealthStore provides insert/list/upsert for the model_health snapshot
// table, the model_health_log history table, and the model_health_settings
// singleton. It is backed by the same *sql.DB connection as PostgresStore and
// reuses the shared Sealer so error messages are sealed at rest when configured.
type ModelHealthStore struct {
	db            *sql.DB
	healthTable   string
	logTable      string
	settingsTable string
	sealer        *Sealer
}

// NewModelHealthStore builds a ModelHealthStore that reuses the PostgresStore
// connection and table names. Returns nil if the parent store is nil so callers
// can feature-detect the absence of the PG backend with a nil check (mirrors
// NewSyncLogStore).
func NewModelHealthStore(parent *PostgresStore) *ModelHealthStore {
	if parent == nil {
		return nil
	}
	sealer, err := NewSealer(parent.cfg.UsageEncryptionKey)
	if err != nil {
		log.WithError(err).Warn("postgres store: model health encryption disabled due to key error")
		sealer = nil
	}
	return &ModelHealthStore{
		db:            parent.DB(),
		healthTable:   parent.ModelHealthTable(),
		logTable:      parent.ModelHealthLogTable(),
		settingsTable: parent.ModelHealthSettingsTable(),
		sealer:        sealer,
	}
}

// SetSealer overrides the in-memory sealer. Used by tests that construct a
// ModelHealthStore directly without going through NewPostgresStore.
func (s *ModelHealthStore) SetSealer(sealer *Sealer) {
	if s == nil {
		return
	}
	s.sealer = sealer
}

// sealErrorMessage seals an error message when a Sealer is enabled, truncating
// to maxAuditBodyBytes first to bound row size. Returns the value to persist.
func (s *ModelHealthStore) sealErrorMessage(errMsg string) string {
	return s.sealText(truncateAuditBody(errMsg), "error_message")
}

// sealPrompt seals the probe prompt text, truncating to maxModelHealthPromptBytes
// first to bound row size. Mirrors sealErrorMessage.
func (s *ModelHealthStore) sealPrompt(text string) string {
	return s.sealText(truncateTo(text, maxModelHealthPromptBytes), "prompt_message")
}

// sealCompletion seals the probe completion text, truncating to
// maxModelHealthCompletionBytes first to bound row size.
func (s *ModelHealthStore) sealCompletion(text string) string {
	return s.sealText(truncateTo(text, maxModelHealthCompletionBytes), "completion")
}

// sealText is the shared seal-or-plaintext helper for a single string column.
// label names the column in debug logs so a failed seal is attributable.
func (s *ModelHealthStore) sealText(value, label string) string {
	if value == "" {
		return ""
	}
	if s.sealer != nil && s.sealer.Enabled() {
		if sealed, errSeal := s.sealer.Seal(value); errSeal == nil {
			return sealed
		} else {
			log.WithError(errSeal).Debug("postgres store: seal model_health " + label + " failed; storing plaintext")
		}
	}
	return value
}

// unsealText unseals a persisted sealed column when a Sealer is configured;
// legacy plaintext rows pass through unchanged.
func (s *ModelHealthStore) unsealText(value string) string {
	return unsealAuditBody(s.sealer, value)
}

// unsealErrorMessage unseals a persisted error message when a Sealer is
// configured; legacy plaintext rows pass through unchanged.
func (s *ModelHealthStore) unsealErrorMessage(errMsg string) string {
	return unsealAuditBody(s.sealer, errMsg)
}

// truncateTo bounds a string to maxBytes, appending an ellipsis marker when
// truncation occurred. Used for prompt/completion columns before sealing.
func truncateTo(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "…[truncated]"
}

// nullableTPS converts a *float64 tokens-per-second into the value/nil pair
// expected by the pgx nullable scan target. nil is persisted when the value
// was not measured (e.g. a failed probe).
func nullableTPS(tps *float64) any {
	if tps == nil {
		return nil
	}
	return *tps
}

// UpsertSnapshot persists (or replaces) the latest health-check snapshot for a
// model id in model_health. One row per model id; INSERT ... ON CONFLICT
// upserts in place. The same row is also appended to model_health_log via
// InsertLog (callers that want both should call RecordResult).
func (s *ModelHealthStore) UpsertSnapshot(ctx context.Context, r ModelHealthRow) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: model health store not initialized")
	}
	if r.CheckedAt.IsZero() {
		r.CheckedAt = time.Now().UTC()
	}
	errMsg := s.sealErrorMessage(r.ErrorMessage)
	prompt := s.sealPrompt(r.PromptMessage)
	completion := s.sealCompletion(r.Completion)
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (model_id, status, success, response_time_ms, tokens_per_second, prompt_tokens, completion_tokens, error_message, provider, prompt_message, completion, upstream_provider, upstream_auth_id, upstream_model, checked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (model_id) DO UPDATE SET
			status            = EXCLUDED.status,
			success           = EXCLUDED.success,
			response_time_ms  = EXCLUDED.response_time_ms,
			tokens_per_second = EXCLUDED.tokens_per_second,
			prompt_tokens     = EXCLUDED.prompt_tokens,
			completion_tokens = EXCLUDED.completion_tokens,
			error_message     = EXCLUDED.error_message,
			provider          = EXCLUDED.provider,
			prompt_message    = EXCLUDED.prompt_message,
			completion        = EXCLUDED.completion,
			upstream_provider = EXCLUDED.upstream_provider,
			upstream_auth_id  = EXCLUDED.upstream_auth_id,
			upstream_model    = EXCLUDED.upstream_model,
			checked_at        = EXCLUDED.checked_at
	`, s.healthTable),
		r.ModelID, r.Status, r.Success, r.ResponseTimeMs,
		nullableTPS(r.TokensPerSecond), r.PromptTokens, r.CompletionTokens,
		nullableString(errMsg), nullableString(r.Provider),
		nullableString(prompt), nullableString(completion),
		nullableString(r.UpstreamProvider), nullableString(r.UpstreamAuthID),
		nullableString(r.UpstreamModel), r.CheckedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres store: upsert model_health snapshot: %w", err)
	}
	return nil
}

// InsertLog appends one history row. ErrorMessage, PromptMessage, and
// Completion are sealed at rest when a Sealer is configured and truncated to
// their byte caps before sealing. The call is fire-and-forget from the probe
// runner; errors are surfaced so the runner can log them without blocking the
// sweep.
func (s *ModelHealthStore) InsertLog(ctx context.Context, r ModelHealthRow) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: model health store not initialized")
	}
	if r.CheckedAt.IsZero() {
		r.CheckedAt = time.Now().UTC()
	}
	errMsg := s.sealErrorMessage(r.ErrorMessage)
	prompt := s.sealPrompt(r.PromptMessage)
	completion := s.sealCompletion(r.Completion)
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (model_id, status, success, response_time_ms, tokens_per_second, prompt_tokens, completion_tokens, error_message, provider, prompt_message, completion, upstream_provider, upstream_auth_id, upstream_model, checked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`, s.logTable),
		r.ModelID, r.Status, r.Success, r.ResponseTimeMs,
		nullableTPS(r.TokensPerSecond), r.PromptTokens, r.CompletionTokens,
		nullableString(errMsg), nullableString(r.Provider),
		nullableString(prompt), nullableString(completion),
		nullableString(r.UpstreamProvider), nullableString(r.UpstreamAuthID),
		nullableString(r.UpstreamModel), r.CheckedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres store: insert model_health_log: %w", err)
	}
	return nil
}

// RecordResult atomically upserts the latest snapshot for a model AND appends
// a history row. Both writes share one bounded-time context so a slow DB
// never pins the probe goroutine. Used by the probe runner.
func (s *ModelHealthStore) RecordResult(ctx context.Context, r ModelHealthRow) error {
	if err := s.UpsertSnapshot(ctx, r); err != nil {
		return err
	}
	return s.InsertLog(ctx, r)
}

// scanModelHealthRow scans one model_health / model_health_log row into a
// ModelHealthRow. The shared column order matches both SELECT lists used here.
// errorMessage, prompt_message, and completion are unsealed.
func (s *ModelHealthStore) scanModelHealthRow(scan func(dest ...any) error, includeID bool) (ModelHealthRow, error) {
	var (
		r             ModelHealthRow
		errMsg        sql.NullString
		provider      sql.NullString
		prompt        sql.NullString
		completion    sql.NullString
		upstreamProv  sql.NullString
		upstreamAuth  sql.NullString
		upstreamModel sql.NullString
		tps           sql.NullFloat64
	)
	var id sql.NullInt64
	if includeID {
		if err := scan(&id, &r.ModelID, &r.Status, &r.Success, &r.ResponseTimeMs, &tps, &r.PromptTokens, &r.CompletionTokens, &errMsg, &provider, &prompt, &completion, &upstreamProv, &upstreamAuth, &upstreamModel, &r.CheckedAt); err != nil {
			return r, err
		}
		r.ID = id.Int64
	} else {
		if err := scan(&r.ModelID, &r.Status, &r.Success, &r.ResponseTimeMs, &tps, &r.PromptTokens, &r.CompletionTokens, &errMsg, &provider, &prompt, &completion, &upstreamProv, &upstreamAuth, &upstreamModel, &r.CheckedAt); err != nil {
			return r, err
		}
	}
	if tps.Valid {
		v := tps.Float64
		r.TokensPerSecond = &v
	}
	r.ErrorMessage = s.unsealErrorMessage(errMsg.String)
	r.Provider = provider.String
	r.PromptMessage = s.unsealText(prompt.String)
	r.Completion = s.unsealText(completion.String)
	r.UpstreamProvider = upstreamProv.String
	r.UpstreamAuthID = upstreamAuth.String
	r.UpstreamModel = upstreamModel.String
	return r, nil
}

// ListSnapshots returns the latest health-check row for every model id,
// ordered by model id. Surfaced on the Analysis → Model Health page and
// aggregated by the public uptime endpoint.
func (s *ModelHealthStore) ListSnapshots(ctx context.Context) ([]ModelHealthRow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: model health store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT model_id, status, success, response_time_ms, tokens_per_second, prompt_tokens, completion_tokens, error_message, provider, prompt_message, completion, upstream_provider, upstream_auth_id, upstream_model, checked_at
		FROM %s ORDER BY model_id ASC
	`, s.healthTable))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list model_health snapshots: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("postgres store: close model_health snapshot rows failed")
		}
	}()
	out := make([]ModelHealthRow, 0)
	for rows.Next() {
		r, errScan := s.scanModelHealthRow(rows.Scan, false)
		if errScan != nil {
			return nil, fmt.Errorf("postgres store: scan model_health snapshot: %w", errScan)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate model_health snapshots: %w", err)
	}
	return out, nil
}

// FilterExcludedSnapshots returns a new slice holding only the rows whose
// model_id is not in the excluded set. Matching is case-insensitive and each
// excluded entry is trimmed (mirroring how the probe runner applies the same
// list at write time), so a model excluded as "GPT-4" hides a snapshot stored
// as "gpt-4". Input order is preserved (ListSnapshots returns rows ordered by
// model_id ASC, so the filtered result stays sorted).
//
// Used on the read path (the dashboard latest-status endpoint and the public
// uptime endpoint) so an excluded model id never appears in the latest status,
// never counts toward the KPI / rollup aggregates, and a stale checked_at is
// not surfaced once the operator has excluded the model. Exclusion at write
// time (the probe runner) only stops new snapshots being written; this filter
// is what hides the last-recorded row.
func FilterExcludedSnapshots(rows []ModelHealthRow, excluded []string) []ModelHealthRow {
	if len(excluded) == 0 {
		return rows
	}
	set := make(map[string]struct{}, len(excluded))
	for _, m := range excluded {
		if v := strings.TrimSpace(m); v != "" {
			set[strings.ToLower(v)] = struct{}{}
		}
	}
	if len(set) == 0 {
		return rows
	}
	out := make([]ModelHealthRow, 0, len(rows))
	for _, r := range rows {
		if _, skip := set[strings.ToLower(r.ModelID)]; skip {
			continue
		}
		out = append(out, r)
	}
	return out
}

// DeleteSnapshotsByModels removes the latest-snapshot rows (in model_health,
// not the model_health_log history) for each model id, matching
// case-insensitively. Used when an operator excludes models in Settings so the
// now-stale snapshot row does not linger invisibly in the table (the sweep
// stops writing it). Returns the number of rows deleted; 0 (nil) when models is
// empty. Mirrors ClearLog / PurgeLogBefore in shape.
func (s *ModelHealthStore) DeleteSnapshotsByModels(ctx context.Context, models []string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: model health store not initialized")
	}
	// Normalize + lowercase so the lower(model_id) = ANY($1) predicate matches
	// regardless of how the id was originally stored.
	normalized := make([]string, 0, len(models))
	for _, m := range models {
		if v := strings.TrimSpace(m); v != "" {
			normalized = append(normalized, strings.ToLower(v))
		}
	}
	if len(normalized) == 0 {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE lower(model_id) = ANY($1)`, s.healthTable),
		normalized,
	)
	if err != nil {
		return 0, fmt.Errorf("postgres store: delete model_health snapshots: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListLogPaged returns a page of model_health_log entries plus the total row
// count matching the filter. ErrorMessage is unsealed when a Sealer is
// configured; legacy plaintext rows pass through unchanged.
func (s *ModelHealthStore) ListLogPaged(ctx context.Context, f ModelHealthLogFilter, page, pageSize int) ([]*ModelHealthRow, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: model health store not initialized")
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	var (
		where []string
		args  []any
	)
	addFilter := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if strings.TrimSpace(f.ModelID) != "" {
		addFilter("model_id = $%d", strings.TrimSpace(f.ModelID))
	}
	if strings.TrimSpace(f.Status) != "" {
		addFilter("status = $%d", strings.TrimSpace(f.Status))
	}
	if f.Success != nil {
		addFilter("success = $%d", *f.Success)
	}
	if !f.From.IsZero() {
		addFilter("checked_at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		addFilter("checked_at <= $%d", f.To)
	}
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s%s`, s.logTable, whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count model_health_log (paged): %w", err)
	}

	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, pageSize, (page-1)*pageSize)
	listQuery := fmt.Sprintf(`
		SELECT id, model_id, status, success, response_time_ms, tokens_per_second, prompt_tokens, completion_tokens, error_message, provider, prompt_message, completion, upstream_provider, upstream_auth_id, upstream_model, checked_at
		FROM %s%s
		ORDER BY checked_at DESC
		LIMIT $%d OFFSET $%d
	`, s.logTable, whereClause, len(listArgs)-1, len(listArgs))

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list model_health_log (paged): %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("postgres store: close model_health_log rows failed")
		}
	}()

	entries := make([]*ModelHealthRow, 0, pageSize)
	for rows.Next() {
		r, errScan := s.scanModelHealthRow(rows.Scan, true)
		if errScan != nil {
			return nil, 0, fmt.Errorf("postgres store: scan model_health_log row (paged): %w", errScan)
		}
		entries = append(entries, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres store: iterate model_health_log (paged): %w", err)
	}
	return entries, total, nil
}

// ErrModelHealthLogNotFound is returned when no model_health_log row matches
// the supplied ID.
var ErrModelHealthLogNotFound = errors.New("postgres store: model health log not found")

// GetLogEntry returns a single model_health_log row by ID. ErrorMessage is
// unsealed.
func (s *ModelHealthStore) GetLogEntry(ctx context.Context, id int64) (*ModelHealthRow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: model health store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, model_id, status, success, response_time_ms, tokens_per_second, prompt_tokens, completion_tokens, error_message, provider, prompt_message, completion, upstream_provider, upstream_auth_id, upstream_model, checked_at
		FROM %s WHERE id = $1
	`, s.logTable), id)
	r, err := s.scanModelHealthRow(row.Scan, true)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrModelHealthLogNotFound
		}
		return nil, fmt.Errorf("postgres store: get model_health_log: %w", err)
	}
	return &r, nil
}

// ClearLog removes all model_health_log rows (history only; the latest
// snapshots in model_health are preserved). Used by the operator-facing
// DELETE /v0/management/model-health/log endpoint. Auto-sweep handles routine
// retention; this is an explicit manual clear. Returns the number deleted.
func (s *ModelHealthStore) ClearLog(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: model health store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, s.logTable))
	if err != nil {
		return 0, fmt.Errorf("postgres store: clear model_health_log: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PurgeLogBefore deletes model_health_log rows older than the cutoff, returning
// the number deleted. Invoked by the StartModelHealthRetentionSweep goroutine.
func (s *ModelHealthStore) PurgeLogBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: model health store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE checked_at < $1`, s.logTable), cutoff)
	if err != nil {
		return 0, fmt.Errorf("postgres store: purge model_health_log: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListLogModels returns the distinct model_id values currently present in the
// model_health_log, ordered alphabetically. Used by the dashboard to populate
// the model filter dropdown. Returns an empty slice when no rows exist.
func (s *ModelHealthStore) ListLogModels(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: model health store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT DISTINCT model_id FROM %s ORDER BY model_id ASC
	`, s.logTable))
	if err != nil {
		return nil, fmt.Errorf("postgres store: distinct model_health_log models: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("postgres store: close distinct model_health_log models failed")
		}
	}()
	var models []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("postgres store: scan distinct model_health_log model: %w", err)
		}
		models = append(models, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate distinct model_health_log models: %w", err)
	}
	return models, nil
}

// scanModelHealthSettings scans one settings row into ModelHealthSettings.
// excluded_models (JSONB) is unmarshaled into the slice.
func (s *ModelHealthStore) scanModelHealthSettings(scan func(dest ...any) error) (ModelHealthSettings, error) {
	var (
		set           ModelHealthSettings
		exclRaw       []byte
		retentionDays sql.NullInt64
		maxLogRows    sql.NullInt64
	)
	// The retention columns are NOT NULL DEFAULT but were backfilled via ALTER
	// on older schemas (see EnsureSchema). Scanning into sql.NullInt64 tolerates
	// a NULL result during the narrow upgrade window and is harmless otherwise.
	if err := scan(&set.Enabled, &set.IntervalSeconds, &exclRaw, &set.MaxTokens, &retentionDays, &maxLogRows, &set.UpdatedAt); err != nil {
		return set, err
	}
	if len(exclRaw) > 0 {
		var list []string
		if err := json.Unmarshal(exclRaw, &list); err == nil {
			set.ExcludedModels = list
		}
	}
	if set.ExcludedModels == nil {
		set.ExcludedModels = []string{}
	}
	if retentionDays.Valid {
		set.RetentionDays = int(retentionDays.Int64)
	}
	if maxLogRows.Valid {
		set.MaxLogRows = int(maxLogRows.Int64)
	}
	return set, nil
}

// GetSettings returns the singleton operator configuration row. If the row is
// missing (e.g. a freshly created table where the seed INSERT has not yet run),
// the default settings are returned so the sweep can run with safe defaults.
func (s *ModelHealthStore) GetSettings(ctx context.Context) (ModelHealthSettings, error) {
	if s == nil || s.db == nil {
		return defaultModelHealthSettings(), fmt.Errorf("postgres store: model health store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT enabled, interval_seconds, excluded_models, max_tokens, retention_days, max_log_rows, updated_at
		FROM %s WHERE id = 1
	`, s.settingsTable))
	set, err := s.scanModelHealthSettings(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Seed row missing — return defaults rather than failing so the
			// sweep and the dashboard still work. The singleton seed INSERT
			// runs at EnsureSchema time; this guards against an empty row set.
			return defaultModelHealthSettings(), nil
		}
		return defaultModelHealthSettings(), fmt.Errorf("postgres store: get model_health_settings: %w", err)
	}
	if set.IntervalSeconds < int(MinModelHealthInterval.Seconds()) {
		set.IntervalSeconds = int(defaultModelHealthInterval.Seconds())
	}
	if set.MaxTokens < 1 {
		set.MaxTokens = 1
	}
	if set.MaxTokens > MaxModelHealthTokens {
		set.MaxTokens = MaxModelHealthTokens
	}
	set.RetentionDays = clampRetentionDays(set.RetentionDays)
	set.MaxLogRows = clampMaxLogRows(set.MaxLogRows)
	return set, nil
}

// UpsertSettings replaces the singleton operator configuration row. Excluded
// models are normalized (trimmed; empty entries dropped) and marshaled to a
// JSONB array. MaxTokens is clamped to [1, MaxModelHealthTokens]. IntervalSeconds
// is clamped to at least MinModelHealthInterval seconds. RetentionDays and
// MaxLogRows are clamped to their valid ranges (0 = disabled).
func (s *ModelHealthStore) UpsertSettings(ctx context.Context, set ModelHealthSettings) (ModelHealthSettings, error) {
	if s == nil || s.db == nil {
		return defaultModelHealthSettings(), fmt.Errorf("postgres store: model health store not initialized")
	}
	if set.IntervalSeconds < int(MinModelHealthInterval.Seconds()) {
		set.IntervalSeconds = int(defaultModelHealthInterval.Seconds())
	}
	if set.MaxTokens < 1 {
		set.MaxTokens = 1
	}
	if set.MaxTokens > MaxModelHealthTokens {
		set.MaxTokens = MaxModelHealthTokens
	}
	set.RetentionDays = clampRetentionDays(set.RetentionDays)
	set.MaxLogRows = clampMaxLogRows(set.MaxLogRows)
	if set.ExcludedModels == nil {
		set.ExcludedModels = []string{}
	}
	normalized := make([]string, 0, len(set.ExcludedModels))
	for _, m := range set.ExcludedModels {
		if v := strings.TrimSpace(m); v != "" {
			normalized = append(normalized, v)
		}
	}
	set.ExcludedModels = normalized
	exclJSON, err := json.Marshal(set.ExcludedModels)
	if err != nil {
		return set, fmt.Errorf("postgres store: marshal model_health excluded_models: %w", err)
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, enabled, interval_seconds, excluded_models, max_tokens, retention_days, max_log_rows, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (id) DO UPDATE SET
			enabled          = EXCLUDED.enabled,
			interval_seconds = EXCLUDED.interval_seconds,
			excluded_models  = EXCLUDED.excluded_models,
			max_tokens       = EXCLUDED.max_tokens,
			retention_days   = EXCLUDED.retention_days,
			max_log_rows     = EXCLUDED.max_log_rows,
			updated_at       = EXCLUDED.updated_at
	`, s.settingsTable),
		set.Enabled, set.IntervalSeconds, exclJSON, set.MaxTokens, set.RetentionDays, set.MaxLogRows,
	)
	if err != nil {
		return set, fmt.Errorf("postgres store: upsert model_health_settings: %w", err)
	}
	return set, nil
}

// clampRetentionDays normalizes a RetentionDays value: 0 keeps "disabled"
// semantics; any positive value below MinModelHealthRetentionDays rises to the
// floor; anything above the ceiling is clamped down. Negative values reset to
// the default. This keeps the sweep safe against operator typos.
func clampRetentionDays(v int) int {
	if v < 0 {
		return defaultModelHealthRetentionDays
	}
	if v == 0 {
		return 0
	}
	if v < MinModelHealthRetentionDays {
		return MinModelHealthRetentionDays
	}
	if v > MaxModelHealthRetentionDays {
		return MaxModelHealthRetentionDays
	}
	return v
}

// clampMaxLogRows normalizes a MaxLogRows value: 0 keeps "unlimited"
// semantics; any positive value below MinModelHealthMaxLogRows rises to the
// floor; anything above the ceiling is clamped down. Negative values reset to
// the default.
func clampMaxLogRows(v int) int {
	if v < 0 {
		return defaultModelHealthMaxLogRows
	}
	if v == 0 {
		return 0
	}
	if v < MinModelHealthMaxLogRows {
		return MinModelHealthMaxLogRows
	}
	if v > MaxModelHealthMaxLogRows {
		return MaxModelHealthMaxLogRows
	}
	return v
}

// defaultModelHealthSettings returns the safe default settings used before the
// operator customizes anything (and as a fallback when PG is unavailable).
func defaultModelHealthSettings() ModelHealthSettings {
	return ModelHealthSettings{
		Enabled:         true,
		IntervalSeconds: int(defaultModelHealthInterval.Seconds()),
		ExcludedModels:  []string{},
		MaxTokens:       1,
		RetentionDays:   defaultModelHealthRetentionDays,
		MaxLogRows:      defaultModelHealthMaxLogRows,
	}
}

// TrimLogToPerModel keeps the newest maxRows history rows per model_id and
// deletes the rest. Returns the total number of rows deleted across all
// models. Invoked by the retention sweep when MaxLogRows is configured. A
// CTE ranks each model's rows by checked_at DESC; rows ranked beyond the cap
// are deleted in one statement so a single sweep pass trims every model.
func (s *ModelHealthStore) TrimLogToPerModel(ctx context.Context, maxRows int) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: model health store not initialized")
	}
	if maxRows <= 0 {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		DELETE FROM %s
		WHERE id IN (
			SELECT id FROM (
				SELECT id,
				       ROW_NUMBER() OVER (PARTITION BY model_id ORDER BY checked_at DESC) AS rn
				FROM %s
			) ranked
			WHERE ranked.rn > $1
		)
	`, s.logTable, s.logTable), maxRows)
	if err != nil {
		return 0, fmt.Errorf("postgres store: trim model_health_log per model: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// StartModelHealthRetentionSweep launches a background goroutine that
// periodically enforces the configured history retention: rows older than
// RetentionDays are purged (when RetentionDays > 0), and each model's history
// is trimmed to the newest MaxLogRows (when MaxLogRows > 0). The sweep re-reads
// settings on each hourly tick so operator changes take effect without a
// restart. No-op (returns immediately) when the store is nil so callers can
// unconditionally invoke it. Mirrors StartSyncLogSweep.
func StartModelHealthRetentionSweep(store *ModelHealthStore) {
	if store == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			settings, err := store.GetSettings(context.Background())
			if err != nil {
				log.WithError(err).Warn("postgres store: model_health retention sweep load settings failed; using defaults")
				settings = defaultModelHealthSettings()
			}
			deletedTotal := int64(0)
			// Time-based pruning.
			if settings.RetentionDays > 0 {
				cutoff := time.Now().Add(-time.Duration(settings.RetentionDays) * 24 * time.Hour)
				deleted, errPurge := store.PurgeLogBefore(context.Background(), cutoff)
				if errPurge != nil {
					log.WithError(errPurge).Warn("postgres store: model_health_log retention sweep failed")
				} else if deleted > 0 {
					deletedTotal += deleted
					log.WithField("deleted", deleted).WithField("cutoff", cutoff).Debug("model_health_log retention sweep purged aged rows")
				}
			}
			// Per-model count cap.
			if settings.MaxLogRows > 0 {
				deleted, errTrim := store.TrimLogToPerModel(context.Background(), settings.MaxLogRows)
				if errTrim != nil {
					log.WithError(errTrim).Warn("postgres store: model_health_log per-model trim failed")
				} else if deleted > 0 {
					deletedTotal += deleted
					log.WithField("deleted", deleted).WithField("max_log_rows", settings.MaxLogRows).Debug("model_health_log retention sweep trimmed per model")
				}
			}
			if deletedTotal == 0 {
				log.Debug("model_health_log retention sweep: nothing to prune")
			}
		}
	}()
}
