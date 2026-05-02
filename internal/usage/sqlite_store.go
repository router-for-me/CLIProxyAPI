package usage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const sqliteTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("usage sqlite path is empty")
	}
	if err := prepareSQLitePath(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("usage sqlite open: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &SQLiteStore{db: db}
	if err := store.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := restrictSQLiteDBFile(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func prepareSQLitePath(path string) error {
	if err := restrictSQLiteParentDir(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("usage sqlite create: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("usage sqlite close created file: %w", err)
	}
	return restrictSQLiteDBFile(path)
}

func restrictSQLiteParentDir(path string) error {
	dir := filepath.Clean(filepath.Dir(path))
	if dir == "." || filepath.Dir(dir) == dir {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("usage sqlite mkdir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("usage sqlite chmod dir: %w", err)
	}
	return nil
}

func restrictSQLiteDBFile(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("usage sqlite chmod db: %w", err)
	}
	return nil
}

func (s *SQLiteStore) initSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS usage_records (
	id TEXT PRIMARY KEY,
	timestamp TEXT NOT NULL,
	api_key TEXT NOT NULL DEFAULT '',
	provider TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	auth_index TEXT NOT NULL DEFAULT '',
	auth_type TEXT NOT NULL DEFAULT '',
	endpoint TEXT NOT NULL DEFAULT '',
	request_id TEXT NOT NULL DEFAULT '',
	latency_ms INTEGER NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
	first_byte_latency_ms INTEGER NOT NULL DEFAULT 0 CHECK (first_byte_latency_ms >= 0),
	generation_ms INTEGER NOT NULL DEFAULT 0 CHECK (generation_ms >= 0),
	thinking_effort TEXT NOT NULL DEFAULT '',
	input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
	output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
	reasoning_tokens INTEGER NOT NULL DEFAULT 0 CHECK (reasoning_tokens >= 0),
	cached_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cached_tokens >= 0),
	total_tokens INTEGER NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
	failed INTEGER NOT NULL DEFAULT 0 CHECK (failed IN (0, 1))
)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_timestamp ON usage_records(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_api_model ON usage_records(api_key, endpoint, provider, model)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("usage sqlite init schema: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) Insert(ctx context.Context, record Record) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("usage sqlite store is nil")
	}
	if strings.TrimSpace(record.ID) == "" {
		return fmt.Errorf("usage record id is empty")
	}

	tokens := nonNegativeTokenStats(record.Tokens)
	tokens.TotalTokens = normalizeTotalTokens(tokens)

	_, err := s.db.ExecContext(ctx, `
INSERT INTO usage_records (
	id, timestamp, api_key, provider, model, source, auth_index, auth_type, endpoint, request_id,
	latency_ms, first_byte_latency_ms, generation_ms, thinking_effort,
	input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		strings.TrimSpace(record.ID),
		formatSQLiteRecordTimestamp(record.Timestamp),
		strings.TrimSpace(record.APIKey),
		strings.TrimSpace(record.Provider),
		normalizeModel(record.Model),
		strings.TrimSpace(record.Source),
		strings.TrimSpace(record.AuthIndex),
		strings.TrimSpace(record.AuthType),
		strings.TrimSpace(record.Endpoint),
		strings.TrimSpace(record.RequestID),
		nonNegative(record.LatencyMs),
		nonNegative(record.FirstByteLatencyMs),
		nonNegative(record.GenerationMs),
		strings.TrimSpace(record.ThinkingEffort),
		tokens.InputTokens,
		tokens.OutputTokens,
		tokens.ReasoningTokens,
		tokens.CachedTokens,
		tokens.TotalTokens,
		boolToInt(record.Failed),
	)
	if err != nil {
		return fmt.Errorf("usage sqlite insert: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Query(ctx context.Context, rng QueryRange) (APIUsage, error) {
	if s == nil || s.db == nil {
		return APIUsage{}, nil
	}
	query := `
SELECT id, timestamp, api_key, endpoint, provider, model, source, auth_index, thinking_effort,
       latency_ms, first_byte_latency_ms, generation_ms,
       input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed
FROM usage_records`
	args := make([]any, 0, 2)
	where := make([]string, 0, 2)
	if rng.Start != nil && !rng.Start.IsZero() {
		where = append(where, "timestamp >= ?")
		args = append(args, formatSQLiteTimestamp(*rng.Start))
	}
	if rng.End != nil && !rng.End.IsZero() {
		where = append(where, "timestamp < ?")
		args = append(args, formatSQLiteTimestamp(*rng.End))
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY timestamp ASC, id ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usage sqlite query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := APIUsage{}
	for rows.Next() {
		var timestampText string
		var apiKey string
		var endpoint string
		var provider string
		var model string
		var failedInt int
		detail := RequestDetail{}
		if err := rows.Scan(
			&detail.ID,
			&timestampText,
			&apiKey,
			&endpoint,
			&provider,
			&model,
			&detail.Source,
			&detail.AuthIndex,
			&detail.ThinkingEffort,
			&detail.LatencyMs,
			&detail.FirstByteLatencyMs,
			&detail.GenerationMs,
			&detail.Tokens.InputTokens,
			&detail.Tokens.OutputTokens,
			&detail.Tokens.ReasoningTokens,
			&detail.Tokens.CachedTokens,
			&detail.Tokens.TotalTokens,
			&failedInt,
		); err != nil {
			return nil, fmt.Errorf("usage sqlite scan: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, timestampText)
		if err != nil {
			return nil, fmt.Errorf("usage sqlite parse timestamp: %w", err)
		}
		detail.Timestamp = parsed.UTC()
		detail.LatencyMs = nonNegative(detail.LatencyMs)
		detail.FirstByteLatencyMs = nonNegative(detail.FirstByteLatencyMs)
		detail.GenerationMs = nonNegative(detail.GenerationMs)
		detail.Failed = failedInt != 0

		key := groupingKey(apiKey, endpoint, provider)
		modelKey := normalizeModel(model)
		if result[key] == nil {
			result[key] = map[string][]RequestDetail{}
		}
		result[key][modelKey] = append(result[key][modelKey], detail)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usage sqlite rows: %w", err)
	}
	return result, nil
}

func (s *SQLiteStore) Delete(ctx context.Context, ids []string) (DeleteResult, error) {
	result := DeleteResult{Missing: []string{}}
	if s == nil || s.db == nil {
		result.Missing = append(result.Missing, ids...)
		return result, nil
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		res, err := s.db.ExecContext(ctx, "DELETE FROM usage_records WHERE id = ?", id)
		if err != nil {
			return result, fmt.Errorf("usage sqlite delete %s: %w", id, err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return result, fmt.Errorf("usage sqlite rows affected: %w", err)
		}
		if rows == 0 {
			result.Missing = append(result.Missing, id)
			continue
		}
		result.Deleted += rows
	}
	return result, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func formatSQLiteTimestamp(timestamp time.Time) string {
	return timestamp.UTC().Format(sqliteTimestampLayout)
}

func formatSQLiteRecordTimestamp(timestamp time.Time) string {
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	return formatSQLiteTimestamp(timestamp)
}

func groupingKey(apiKey, endpoint, provider string) string {
	if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(endpoint); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(provider); trimmed != "" {
		return trimmed
	}
	return "unknown"
}

func normalizeModel(model string) string {
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		return trimmed
	}
	return "unknown"
}

func normalizeTotalTokens(tokens TokenStats) int64 {
	if tokens.TotalTokens != 0 {
		return tokens.TotalTokens
	}
	total := tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	if total != 0 {
		return total
	}
	return tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
}

func nonNegativeTokenStats(tokens TokenStats) TokenStats {
	tokens.InputTokens = nonNegative(tokens.InputTokens)
	tokens.OutputTokens = nonNegative(tokens.OutputTokens)
	tokens.ReasoningTokens = nonNegative(tokens.ReasoningTokens)
	tokens.CachedTokens = nonNegative(tokens.CachedTokens)
	tokens.TotalTokens = nonNegative(tokens.TotalTokens)
	return tokens
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
