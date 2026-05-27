package usagestats

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	log "github.com/sirupsen/logrus"
)

const (
	defaultTableName = "usage_records"
	maxRecentLimit   = 100
)

// PostgresStoreConfig holds configuration for the PostgreSQL-backed usage store.
type PostgresStoreConfig struct {
	DSN    string
	Schema string
	Table  string
}

// PostgresStore implements Store backed by PostgreSQL.
type PostgresStore struct {
	db    *sql.DB
	cfg   PostgresStoreConfig
	table string // fully qualified, quoted table name
}

// NewPostgresStore opens a connection and validates reachability.
// It does NOT create schema; call EnsureSchema separately.
func NewPostgresStore(ctx context.Context, cfg PostgresStoreConfig) (*PostgresStore, error) {
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		return nil, fmt.Errorf("usagestats: postgres DSN is required")
	}
	table := strings.TrimSpace(cfg.Table)
	if table == "" {
		table = defaultTableName
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("usagestats: open database: %w", err)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("usagestats: ping database: %w", err)
	}

	return &PostgresStore{
		db:    db,
		cfg:   cfg,
		table: fullTableName(cfg.Schema, table),
	}, nil
}

// EnsureSchema creates the usage_records table and indexes idempotently.
func (s *PostgresStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("usagestats: store not initialized")
	}

	// Create schema if specified.
	if schema := strings.TrimSpace(s.cfg.Schema); schema != "" {
		_, err := s.db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteID(schema)))
		if err != nil {
			return fmt.Errorf("usagestats: create schema: %w", err)
		}
	}

	// Create table.
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGSERIAL PRIMARY KEY,
			request_id TEXT NOT NULL DEFAULT '',
			requested_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			call_type TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			alias TEXT NOT NULL DEFAULT '',
			endpoint TEXT NOT NULL DEFAULT '',
			api_key_id TEXT NOT NULL DEFAULT '',
			auth_id TEXT NOT NULL DEFAULT '',
			auth_index TEXT NOT NULL DEFAULT '',
			auth_type TEXT NOT NULL DEFAULT '',
			source_label TEXT NOT NULL DEFAULT '',
			reasoning_effort TEXT NOT NULL DEFAULT '',
			failed BOOLEAN NOT NULL DEFAULT FALSE,
			status_code INTEGER NOT NULL DEFAULT 0,
			error_type TEXT NOT NULL DEFAULT '',
			latency_ms BIGINT NOT NULL DEFAULT 0,
			input_tokens BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			reasoning_tokens BIGINT NOT NULL DEFAULT 0,
			cached_tokens BIGINT NOT NULL DEFAULT 0,
			cache_read_tokens BIGINT NOT NULL DEFAULT 0,
			cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens BIGINT NOT NULL DEFAULT 0,
			cost_known BOOLEAN NOT NULL DEFAULT FALSE,
			input_cost_micros BIGINT NOT NULL DEFAULT 0,
			output_cost_micros BIGINT NOT NULL DEFAULT 0,
			total_cost_micros BIGINT NOT NULL DEFAULT 0
		)`, s.table))
	if err != nil {
		return fmt.Errorf("usagestats: create table: %w", err)
	}

	// Create indexes.
	indexes := []string{
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS usage_records_requested_at_idx ON %s (requested_at DESC)", s.table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS usage_records_call_type_idx ON %s (call_type, requested_at DESC)", s.table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS usage_records_provider_model_idx ON %s (provider, model, requested_at DESC)", s.table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS usage_records_api_key_idx ON %s (api_key_id, requested_at DESC)", s.table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS usage_records_auth_idx ON %s (auth_index, requested_at DESC)", s.table),
	}
	for _, idx := range indexes {
		if _, err = s.db.ExecContext(ctx, idx); err != nil {
			return fmt.Errorf("usagestats: create index: %w", err)
		}
	}

	return nil
}

// Append inserts a sanitized usage event into PostgreSQL.
func (s *PostgresStore) Append(ctx context.Context, event Event) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("usagestats: store not initialized")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			request_id, requested_at,
			call_type,
			provider, model, alias, endpoint,
			api_key_id, auth_id, auth_index, auth_type, source_label,
			reasoning_effort,
			failed, status_code, error_type, latency_ms,
			input_tokens, output_tokens, reasoning_tokens,
			cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
			cost_known, input_cost_micros, output_cost_micros, total_cost_micros
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)
	`, s.table),
		event.RequestID, event.RequestedAt,
		event.CallType,
		event.Provider, event.Model, event.Alias, event.Endpoint,
		event.APIKeyID, event.AuthID, event.AuthIndex, event.AuthType, event.SourceLabel,
		event.ReasoningEffort,
		event.Failed, event.StatusCode, event.ErrorType, event.LatencyMs,
		event.InputTokens, event.OutputTokens, event.ReasoningTokens,
		event.CachedTokens, event.CacheReadTokens, event.CacheCreationTokens, event.TotalTokens,
		event.CostKnown, event.InputCostMicros, event.OutputCostMicros, event.TotalCostMicros,
	)
	if err != nil {
		return fmt.Errorf("usagestats: append: %w", err)
	}
	return nil
}

// Summary returns aggregated usage statistics.
func (s *PostgresStore) Summary(ctx context.Context, query Query) (*SummaryResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usagestats: store not initialized")
	}

	from := query.From
	to := query.To
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.AddDate(0, 0, -30) // default 30 days
	}

	groupBy := query.GroupBy
	if groupBy == "" {
		groupBy = GroupByDay
	}

	// Build overall summary.
	total, err := s.queryTotal(ctx, from, to)
	if err != nil {
		return nil, err
	}

	// Build grouped rows.
	groups, err := s.queryGroups(ctx, from, to, groupBy)
	if err != nil {
		return nil, err
	}

	// Build recent records.
	var recent []RecentRecord
	if query.RecentLimit > 0 {
		limit := query.RecentLimit
		if limit > maxRecentLimit {
			limit = maxRecentLimit
		}
		recent, err = s.queryRecent(ctx, from, to, limit)
		if err != nil {
			return nil, err
		}
	}

	return &SummaryResult{
		From:    from.Format(time.RFC3339),
		To:      to.Format(time.RFC3339),
		GroupBy: string(groupBy),
		Summary: *total,
		Groups:  groups,
		Recent:  recent,
	}, nil
}

// Close releases the database connection.
func (s *PostgresStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *PostgresStore) queryTotal(ctx context.Context, from, to time.Time) (*SummaryTotal, error) {
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT
			COUNT(*) AS requests,
			COUNT(*) FILTER (WHERE NOT failed) AS success,
			COUNT(*) FILTER (WHERE failed) AS failed,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0),
			COALESCE(SUM(cached_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(total_cost_micros) FILTER (WHERE cost_known), 0),
			COUNT(*) FILTER (WHERE NOT cost_known),
			COALESCE(SUM(total_tokens) FILTER (WHERE NOT cost_known), 0)
		FROM %s WHERE requested_at >= $1 AND requested_at < $2
	`, s.table), from, to)

	var t SummaryTotal
	var totalCostMicros int64
	err := row.Scan(
		&t.Reqs, &t.OK, &t.Fail,
		&t.Tokens.InputTokens, &t.Tokens.OutputTokens, &t.Tokens.ReasoningTokens,
		&t.Tokens.CachedTokens, &t.Tokens.CacheReadTokens, &t.Tokens.CacheCreationTokens,
		&t.Tokens.TotalTokens,
		&totalCostMicros,
		&t.Cost.UnknownReqs,
		&t.Cost.UnknownTokens,
	)
	if err != nil {
		return nil, fmt.Errorf("usagestats: query total: %w", err)
	}
	t.Cost.TotalMicros = totalCostMicros
	t.Cost.TotalUSD = MicrosToUSD(totalCostMicros)
	t.Cost.Known = t.Cost.UnknownReqs == 0 && t.Reqs > 0
	return &t, nil
}

func (s *PostgresStore) queryGroups(ctx context.Context, from, to time.Time, groupBy GroupBy) ([]SummaryRow, error) {
	groupExpr := groupExprFor(groupBy)
	if groupExpr == "" {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT
			%s AS key,
			COUNT(*) AS requests,
			COUNT(*) FILTER (WHERE NOT failed) AS success,
			COUNT(*) FILTER (WHERE failed) AS failed,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0),
			COALESCE(SUM(cached_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(total_cost_micros) FILTER (WHERE cost_known), 0),
			COUNT(*) FILTER (WHERE NOT cost_known),
			COALESCE(SUM(total_tokens) FILTER (WHERE NOT cost_known), 0)
		FROM %s WHERE requested_at >= $1 AND requested_at < $2
		GROUP BY %s ORDER BY MIN(requested_at) ASC
	`, groupExpr, s.table, groupExpr), from, to)
	if err != nil {
		return nil, fmt.Errorf("usagestats: query groups: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("usagestats: close group rows")
		}
	}()

	var result []SummaryRow
	for rows.Next() {
		var r SummaryRow
		var totalCostMicros int64
		err = rows.Scan(
			&r.Key, &r.Reqs, &r.OK, &r.Fail,
			&r.Tokens.InputTokens, &r.Tokens.OutputTokens, &r.Tokens.ReasoningTokens,
			&r.Tokens.CachedTokens, &r.Tokens.CacheReadTokens, &r.Tokens.CacheCreationTokens,
			&r.Tokens.TotalTokens,
			&totalCostMicros,
			&r.Cost.UnknownReqs,
			&r.Cost.UnknownTokens,
		)
		if err != nil {
			return nil, fmt.Errorf("usagestats: scan group row: %w", err)
		}
		r.Cost.TotalMicros = totalCostMicros
		r.Cost.TotalUSD = MicrosToUSD(totalCostMicros)
		r.Cost.Known = r.Cost.UnknownReqs == 0 && r.Reqs > 0
		result = append(result, r)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usagestats: iterate group rows: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) queryRecent(ctx context.Context, from, to time.Time, limit int) ([]RecentRecord, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT
			requested_at, request_id, provider, model, alias,
			failed, status_code, error_type, latency_ms,
			input_tokens, output_tokens, reasoning_tokens,
			cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
			cost_known, total_cost_micros,
			api_key_id, auth_index, auth_type, reasoning_effort
		FROM %s WHERE requested_at >= $1 AND requested_at < $2
		ORDER BY requested_at DESC LIMIT $3
	`, s.table), from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("usagestats: query recent: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("usagestats: close recent rows")
		}
	}()

	var result []RecentRecord
	for rows.Next() {
		var rec RecentRecord
		var requestedAt time.Time
		var costKnown bool
		var totalCostMicros int64
		err = rows.Scan(
			&requestedAt, &rec.RequestID, &rec.Provider, &rec.Model, &rec.Alias,
			&rec.Failed, &rec.StatusCode, &rec.ErrorType, &rec.LatencyMs,
			&rec.Tokens.InputTokens, &rec.Tokens.OutputTokens, &rec.Tokens.ReasoningTokens,
			&rec.Tokens.CachedTokens, &rec.Tokens.CacheReadTokens, &rec.Tokens.CacheCreationTokens, &rec.Tokens.TotalTokens,
			&costKnown, &totalCostMicros,
			&rec.APIKeyID, &rec.AuthIndex, &rec.AuthType, &rec.ReasoningEffort,
		)
		if err != nil {
			return nil, fmt.Errorf("usagestats: scan recent row: %w", err)
		}
		rec.Time = requestedAt.Format(time.RFC3339)
		rec.Cost.Known = costKnown
		rec.Cost.TotalUSD = MicrosToUSD(totalCostMicros)
		rec.Cost.TotalMicros = totalCostMicros
		result = append(result, rec)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("usagestats: iterate recent rows: %w", err)
	}
	return result, nil
}

// groupExprFor returns the SQL expression for the given group_by dimension.
// Returns empty string for invalid/unsupported values.
func groupExprFor(gb GroupBy) string {
	switch gb {
	case GroupByDay:
		return "TO_CHAR(requested_at, 'YYYY-MM-DD')"
	case GroupByProvider:
		return "provider"
	case GroupByModel:
		return "provider || '/' || model"
	case GroupByAPIKey:
		return "api_key_id"
	case GroupByAuth:
		return "auth_index"
	case GroupByCallType:
		return "call_type"
	default:
		return ""
	}
}

// fullTableName returns a properly quoted table name, optionally schema-qualified.
func fullTableName(schema, table string) string {
	if schema = strings.TrimSpace(schema); schema != "" {
		return quoteID(schema) + "." + quoteID(table)
	}
	return quoteID(table)
}

// quoteID safely quotes a PostgreSQL identifier.
func quoteID(id string) string {
	return "\"" + strings.ReplaceAll(id, "\"", "\"\"") + "\""
}

// ParseTime parses a query parameter as either RFC3339 or YYYY-MM-DD.
func ParseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	// Try YYYY-MM-DD first.
	if len(s) == 10 && s[4] == '-' && s[7] == '-' {
		t, err := time.Parse("2006-01-02", s)
		if err == nil {
			return t, nil
		}
	}
	// Try RFC3339.
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time format %q: expected RFC3339 or YYYY-MM-DD", s)
	}
	return t, nil
}

// parseInt parses a query parameter as an integer with bounds.
func parseInt(s string, min, max, def int) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q", s)
	}
	if v < min {
		v = min
	}
	if max > 0 && v > max {
		v = max
	}
	return v, nil
}
