package usage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

const defaultUsageEventsTable = "usage_events"

// PostgresStatisticsStore persists usage events in PostgreSQL.
type PostgresStatisticsStore struct {
	db        *sql.DB
	tableName string
}

func NewPostgresStatisticsStore(ctx context.Context, databaseURL string, autoMigrate bool) (*PostgresStatisticsStore, error) {
	return newPostgresStatisticsStore(ctx, databaseURL, autoMigrate, defaultUsageEventsTable)
}

func newPostgresStatisticsStore(ctx context.Context, databaseURL string, autoMigrate bool, tableName string) (*PostgresStatisticsStore, error) {
	dsn := strings.TrimSpace(databaseURL)
	if dsn == "" {
		return nil, fmt.Errorf("usage postgres store: usage-storage.database-url is required")
	}
	if strings.TrimSpace(tableName) == "" {
		tableName = defaultUsageEventsTable
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("usage postgres store: open database: %w", err)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("usage postgres store: ping database: %w", err)
	}

	store := &PostgresStatisticsStore{db: db, tableName: tableName}
	if autoMigrate {
		if err = store.ensureSchema(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return store, nil
}

func (s *PostgresStatisticsStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *PostgresStatisticsStore) ensureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("usage postgres store: not initialized")
	}
	tbl := quoteIdentifier(s.tableName)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGSERIAL PRIMARY KEY,
			dedup_key TEXT NOT NULL UNIQUE,
			requested_at TIMESTAMPTZ NOT NULL,
			provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			api_key_identifier TEXT NOT NULL DEFAULT '',
			auth_id TEXT NOT NULL DEFAULT '',
			auth_index TEXT NOT NULL DEFAULT '',
			latency_ms BIGINT NOT NULL DEFAULT 0,
			failed BOOLEAN NOT NULL DEFAULT FALSE,
			input_tokens BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			reasoning_tokens BIGINT NOT NULL DEFAULT 0,
			cached_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens BIGINT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, tbl)); err != nil {
		return fmt.Errorf("usage postgres store: create usage events table: %w", err)
	}

	indexStatements := []string{
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (requested_at DESC)", quoteIdentifier(s.tableName+"_requested_at_idx"), tbl),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (api_key_identifier, requested_at DESC)", quoteIdentifier(s.tableName+"_api_time_idx"), tbl),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (model, requested_at DESC)", quoteIdentifier(s.tableName+"_model_time_idx"), tbl),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (failed, requested_at DESC)", quoteIdentifier(s.tableName+"_failed_time_idx"), tbl),
	}
	for i := range indexStatements {
		if _, err := s.db.ExecContext(ctx, indexStatements[i]); err != nil {
			return fmt.Errorf("usage postgres store: create index: %w", err)
		}
	}
	return nil
}

func (s *PostgresStatisticsStore) Record(ctx context.Context, record coreusage.Record) error {
	if s == nil || s.db == nil {
		return nil
	}

	event := usageEventFromRecord(ctx, record)
	query := fmt.Sprintf(`
		INSERT INTO %s (
			dedup_key,
			requested_at,
			provider,
			model,
			source,
			api_key_identifier,
			auth_id,
			auth_index,
			latency_ms,
			failed,
			input_tokens,
			output_tokens,
			reasoning_tokens,
			cached_tokens,
			total_tokens
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15
		)
		ON CONFLICT (dedup_key) DO NOTHING
	`, quoteIdentifier(s.tableName))

	_, err := s.db.ExecContext(
		ctx,
		query,
		event.DedupKey,
		event.RequestedAt,
		event.Provider,
		event.Model,
		event.Source,
		event.APIKeyIdentifier,
		event.AuthID,
		event.AuthIndex,
		event.LatencyMs,
		event.Failed,
		event.Tokens.InputTokens,
		event.Tokens.OutputTokens,
		event.Tokens.ReasoningTokens,
		event.Tokens.CachedTokens,
		event.Tokens.TotalTokens,
	)
	if err != nil {
		return fmt.Errorf("usage postgres store: insert usage event: %w", err)
	}
	return nil
}

func (s *PostgresStatisticsStore) Snapshot(ctx context.Context) (StatisticsSnapshot, error) {
	if s == nil || s.db == nil {
		return StatisticsSnapshot{}, nil
	}

	query := fmt.Sprintf(`
		SELECT
			requested_at,
			provider,
			model,
			source,
			api_key_identifier,
			auth_id,
			auth_index,
			latency_ms,
			failed,
			input_tokens,
			output_tokens,
			reasoning_tokens,
			cached_tokens,
			total_tokens
		FROM %s
		ORDER BY requested_at ASC, id ASC
	`, quoteIdentifier(s.tableName))

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("usage postgres store: query snapshot events: %w", err)
	}
	defer rows.Close()

	snapshot := StatisticsSnapshot{
		APIs:           make(map[string]APISnapshot),
		RequestsByDay:  make(map[string]int64),
		RequestsByHour: make(map[string]int64),
		TokensByDay:    make(map[string]int64),
		TokensByHour:   make(map[string]int64),
	}

	for rows.Next() {
		var event usageEvent
		if err = rows.Scan(
			&event.RequestedAt,
			&event.Provider,
			&event.Model,
			&event.Source,
			&event.APIKeyIdentifier,
			&event.AuthID,
			&event.AuthIndex,
			&event.LatencyMs,
			&event.Failed,
			&event.Tokens.InputTokens,
			&event.Tokens.OutputTokens,
			&event.Tokens.ReasoningTokens,
			&event.Tokens.CachedTokens,
			&event.Tokens.TotalTokens,
		); err != nil {
			return StatisticsSnapshot{}, fmt.Errorf("usage postgres store: scan snapshot event: %w", err)
		}
		mergeEventIntoSnapshot(&snapshot, event)
	}
	if err = rows.Err(); err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("usage postgres store: iterate snapshot events: %w", err)
	}

	return snapshot, nil
}

func (s *PostgresStatisticsStore) Export(ctx context.Context) (StatisticsSnapshot, error) {
	return s.Snapshot(ctx)
}

func (s *PostgresStatisticsStore) Import(ctx context.Context, snapshot StatisticsSnapshot) (MergeResult, error) {
	if s == nil || s.db == nil {
		return MergeResult{}, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MergeResult{}, fmt.Errorf("usage postgres store: begin import tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	query := fmt.Sprintf(`
		INSERT INTO %s (
			dedup_key,
			requested_at,
			provider,
			model,
			source,
			api_key_identifier,
			auth_id,
			auth_index,
			latency_ms,
			failed,
			input_tokens,
			output_tokens,
			reasoning_tokens,
			cached_tokens,
			total_tokens
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15
		)
		ON CONFLICT (dedup_key) DO NOTHING
	`, quoteIdentifier(s.tableName))

	result := MergeResult{}
	for apiName, apiSnapshot := range snapshot.APIs {
		apiIdentifier := strings.TrimSpace(apiName)
		if apiIdentifier == "" {
			continue
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			model := strings.TrimSpace(modelName)
			if model == "" {
				model = "unknown"
			}
			for i := range modelSnapshot.Details {
				detail := modelSnapshot.Details[i]
				normalized := normaliseTokenStats(detail.Tokens)
				timestamp := detail.Timestamp
				if timestamp.IsZero() {
					timestamp = time.Now().UTC()
				}
				latencyMs := detail.LatencyMs
				if latencyMs < 0 {
					latencyMs = 0
				}

				event := usageEvent{
					RequestedAt:       timestamp,
					Provider:          "",
					Model:             model,
					Source:            strings.TrimSpace(detail.Source),
					APIKeyIdentifier:  apiIdentifier,
					AuthID:            "",
					AuthIndex:         strings.TrimSpace(detail.AuthIndex),
					LatencyMs:         latencyMs,
					Failed:            detail.Failed,
					Tokens:            normalized,
				}
				event.DedupKey = dedupKeyForEvent(event)

				execResult, execErr := tx.ExecContext(
					ctx,
					query,
					event.DedupKey,
					event.RequestedAt,
					event.Provider,
					event.Model,
					event.Source,
					event.APIKeyIdentifier,
					event.AuthID,
					event.AuthIndex,
					event.LatencyMs,
					event.Failed,
					event.Tokens.InputTokens,
					event.Tokens.OutputTokens,
					event.Tokens.ReasoningTokens,
					event.Tokens.CachedTokens,
					event.Tokens.TotalTokens,
				)
				if execErr != nil {
					return MergeResult{}, fmt.Errorf("usage postgres store: import usage event: %w", execErr)
				}
				if rowsAffected, rowsErr := execResult.RowsAffected(); rowsErr == nil && rowsAffected > 0 {
					result.Added++
				} else {
					result.Skipped++
				}
			}
		}
	}

	if err = tx.Commit(); err != nil {
		return MergeResult{}, fmt.Errorf("usage postgres store: commit import tx: %w", err)
	}
	return result, nil
}

type usageEvent struct {
	DedupKey         string
	RequestedAt      time.Time
	Provider         string
	Model            string
	Source           string
	APIKeyIdentifier string
	AuthID           string
	AuthIndex        string
	LatencyMs        int64
	Failed           bool
	Tokens           TokenStats
}

func usageEventFromRecord(ctx context.Context, record coreusage.Record) usageEvent {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	tokens := normaliseDetail(record.Detail)
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	model := strings.TrimSpace(record.Model)
	if model == "" {
		model = "unknown"
	}
	apiIdentifier := strings.TrimSpace(record.APIKey)
	if apiIdentifier == "" {
		apiIdentifier = resolveAPIIdentifier(ctx, record)
	}
	event := usageEvent{
		RequestedAt:      timestamp,
		Provider:         strings.TrimSpace(record.Provider),
		Model:            model,
		Source:           strings.TrimSpace(record.Source),
		APIKeyIdentifier: apiIdentifier,
		AuthID:           strings.TrimSpace(record.AuthID),
		AuthIndex:        strings.TrimSpace(record.AuthIndex),
		LatencyMs:        normaliseLatency(record.Latency),
		Failed:           failed,
		Tokens:           tokens,
	}
	event.DedupKey = dedupKeyForEvent(event)
	return event
}

func dedupKeyForEvent(event usageEvent) string {
	seed := fmt.Sprintf(
		"%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d",
		event.RequestedAt.UTC().Format(time.RFC3339Nano),
		event.APIKeyIdentifier,
		event.Model,
		event.Source,
		event.AuthIndex,
		event.Failed,
		event.Tokens.InputTokens,
		event.Tokens.OutputTokens,
		event.Tokens.ReasoningTokens,
		event.Tokens.CachedTokens,
		event.Tokens.TotalTokens,
	)
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func mergeEventIntoSnapshot(snapshot *StatisticsSnapshot, event usageEvent) {
	if snapshot == nil {
		return
	}
	tokens := normaliseTokenStats(event.Tokens)
	if tokens.TotalTokens < 0 {
		tokens.TotalTokens = 0
	}
	timestamp := event.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}

	snapshot.TotalRequests++
	if event.Failed {
		snapshot.FailureCount++
	} else {
		snapshot.SuccessCount++
	}
	snapshot.TotalTokens += tokens.TotalTokens

	apiIdentifier := strings.TrimSpace(event.APIKeyIdentifier)
	if apiIdentifier == "" {
		apiIdentifier = "unknown"
	}
	apiSnapshot := snapshot.APIs[apiIdentifier]
	if apiSnapshot.Models == nil {
		apiSnapshot.Models = make(map[string]ModelSnapshot)
	}
	apiSnapshot.TotalRequests++
	apiSnapshot.TotalTokens += tokens.TotalTokens

	model := strings.TrimSpace(event.Model)
	if model == "" {
		model = "unknown"
	}
	modelSnapshot := apiSnapshot.Models[model]
	modelSnapshot.TotalRequests++
	modelSnapshot.TotalTokens += tokens.TotalTokens
	modelSnapshot.Details = append(modelSnapshot.Details, RequestDetail{
		Timestamp: timestamp,
		LatencyMs: event.LatencyMs,
		Source:    event.Source,
		AuthIndex: event.AuthIndex,
		Tokens:    tokens,
		Failed:    event.Failed,
	})
	apiSnapshot.Models[model] = modelSnapshot
	snapshot.APIs[apiIdentifier] = apiSnapshot

	dayKey := timestamp.Format("2006-01-02")
	hourKey := formatHour(timestamp.Hour())
	snapshot.RequestsByDay[dayKey]++
	snapshot.RequestsByHour[hourKey]++
	snapshot.TokensByDay[dayKey] += tokens.TotalTokens
	snapshot.TokensByHour[hourKey] += tokens.TotalTokens
}

func quoteIdentifier(identifier string) string {
	replaced := strings.ReplaceAll(identifier, "\"", "\"\"")
	return "\"" + replaced + "\""
}
