package usage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	_ "modernc.org/sqlite"
)

// UsageRecord represents a single usage record for database persistence.
type UsageRecord struct {
	APIKey          string
	Model           string
	Source          string
	AuthIndex       string
	Failed          bool
	RequestedAt     time.Time
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
	Method          string // HTTP method (GET, POST, etc.)
	Path            string // Request URL path (/v1/chat/completions, etc.)
}

// APIStats holds aggregated metrics for a single API key from database.
type APIStats struct {
	TotalRequests int64
	TotalTokens   int64
	Models        map[string]ModelStats
}

// ModelStats holds aggregated metrics for a single model from database.
type ModelStats struct {
	TotalRequests int64
	TotalTokens   int64
}

// AggregatedStats represents aggregated usage statistics from database.
type AggregatedStats struct {
	TotalRequests  int64
	SuccessCount   int64
	FailureCount   int64
	TotalTokens    int64
	APIs           map[string]APIStats
	RequestsByDay  map[string]int64
	RequestsByHour map[string]int64
	TokensByDay    map[string]int64
	TokensByHour   map[string]int64
	DetailCount    int64          // Total count of detail records (for pagination)
	Details        []DetailRecord // Only populated when using GetDetails with pagination
}

// DetailRecord represents a single request record from database.
type DetailRecord struct {
	APIKey          string
	Model           string
	Source          string
	AuthIndex       string
	Failed          bool
	RequestedAt     time.Time
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
}

// UsageStore defines the interface for usage record persistence.
type UsageStore interface {
	Insert(ctx context.Context, record UsageRecord) error
	InsertBatch(ctx context.Context, records []UsageRecord) (added, skipped int64, err error)
	GetAggregatedStats(ctx context.Context) (AggregatedStats, error)
	GetDetails(ctx context.Context, offset, limit int) ([]DetailRecord, error)
	DeleteOldRecords(ctx context.Context, retentionDays int) (deleted int64, err error)
	EnsureSchema(ctx context.Context) error
	Close() error
}

const (
	defaultMirrorSyncBatchSize = 10000
	defaultLocalUsageFileName  = "usage.db"
)

// NewUsageStore creates a UsageStore based on environment configuration.
// If pgDSN is provided, it uses PostgreSQL for writes and a local SQLite mirror for reads;
// otherwise it uses SQLite in authDir directly.
func NewUsageStore(ctx context.Context, pgDSN, pgSchema, authDir string) (UsageStore, error) {
	if strings.TrimSpace(pgDSN) != "" {
		return newMirrorUsageStore(ctx, pgDSN, pgSchema, authDir)
	}
	return newSQLiteUsageStore(authDir)
}

type mirrorUsageStore struct {
	primary *pgUsageStore
	local   *sqliteUsageStore
}

func newMirrorUsageStore(ctx context.Context, pgDSN, pgSchema, authDir string) (*mirrorUsageStore, error) {
	primary, err := newPgUsageStore(ctx, pgDSN, pgSchema)
	if err != nil {
		return nil, err
	}

	localPath := resolveLocalUsageDBPath(authDir)
	local, err := newSQLiteUsageStoreAtPath(localPath)
	if err != nil {
		_ = primary.Close()
		return nil, err
	}

	store := &mirrorUsageStore{primary: primary, local: local}
	if err = store.bootstrapLocalFromPrimary(ctx); err != nil {
		_ = local.Close()
		_ = primary.Close()
		return nil, err
	}
	return store, nil
}

func resolveLocalUsageDBPath(authDir string) string {
	if localPath := util.GetEnvTrimmed("PGSTORE_LOCAL_PATH", "pgstore_local_path"); localPath != "" {
		cleaned := filepath.Clean(localPath)
		if strings.EqualFold(filepath.Ext(cleaned), ".db") {
			return cleaned
		}
		return filepath.Join(cleaned, defaultLocalUsageFileName)
	}
	if authDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			authDir = cwd
		}
	}
	if authDir == "" {
		return defaultLocalUsageFileName
	}
	return filepath.Join(authDir, defaultLocalUsageFileName)
}

func (s *mirrorUsageStore) bootstrapLocalFromPrimary(ctx context.Context) error {
	if s == nil || s.primary == nil || s.local == nil {
		return fmt.Errorf("usage store: mirror store not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.local.Reset(ctx); err != nil {
		return fmt.Errorf("usage store: reset local sqlite mirror: %w", err)
	}

	lastID := int64(0)
	for {
		records, newLastID, err := s.primary.ListRecordsAfterID(ctx, lastID, defaultMirrorSyncBatchSize)
		if err != nil {
			return fmt.Errorf("usage store: sync records from postgres: %w", err)
		}
		if len(records) == 0 {
			break
		}
		added, skipped, err := s.local.InsertBatch(ctx, records)
		if err != nil {
			return fmt.Errorf("usage store: write mirror sqlite batch: %w", err)
		}
		if skipped > 0 || added != int64(len(records)) {
			return fmt.Errorf("usage store: sqlite mirror batch mismatch (added=%d skipped=%d expected=%d)", added, skipped, len(records))
		}
		lastID = newLastID
	}
	return nil
}

func (s *mirrorUsageStore) Insert(ctx context.Context, record UsageRecord) error {
	if s == nil || s.primary == nil || s.local == nil {
		return fmt.Errorf("usage store: mirror store not initialized")
	}
	if err := s.primary.Insert(ctx, record); err != nil {
		return err
	}
	if err := s.local.Insert(ctx, record); err != nil {
		return fmt.Errorf("usage store: insert sqlite mirror: %w", err)
	}
	return nil
}

func (s *mirrorUsageStore) InsertBatch(ctx context.Context, records []UsageRecord) (added, skipped int64, err error) {
	if len(records) == 0 {
		return 0, 0, nil
	}
	if s == nil || s.primary == nil || s.local == nil {
		return 0, 0, fmt.Errorf("usage store: mirror store not initialized")
	}

	var firstErr error
	for _, record := range records {
		if err = s.primary.Insert(ctx, record); err != nil {
			skipped++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err = s.local.Insert(ctx, record); err != nil {
			skipped++
			if firstErr == nil {
				firstErr = fmt.Errorf("usage store: insert sqlite mirror: %w", err)
			}
			continue
		}
		added++
	}

	if firstErr != nil {
		return added, skipped, firstErr
	}
	return added, skipped, nil
}

func (s *mirrorUsageStore) GetAggregatedStats(ctx context.Context) (AggregatedStats, error) {
	if s == nil || s.local == nil {
		return AggregatedStats{}, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.GetAggregatedStats(ctx)
}

func (s *mirrorUsageStore) GetDetails(ctx context.Context, offset, limit int) ([]DetailRecord, error) {
	if s == nil || s.local == nil {
		return nil, fmt.Errorf("usage store: mirror store not initialized")
	}
	return s.local.GetDetails(ctx, offset, limit)
}

func (s *mirrorUsageStore) DeleteOldRecords(ctx context.Context, retentionDays int) (deleted int64, err error) {
	if s == nil || s.primary == nil || s.local == nil {
		return 0, fmt.Errorf("usage store: mirror store not initialized")
	}
	deletedPG, err := s.primary.DeleteOldRecords(ctx, retentionDays)
	if err != nil {
		return 0, err
	}
	deletedLocal, err := s.local.DeleteOldRecords(ctx, retentionDays)
	if err != nil {
		return 0, fmt.Errorf("usage store: delete old records in sqlite mirror: %w", err)
	}
	if deletedPG > deletedLocal {
		return deletedPG, nil
	}
	return deletedLocal, nil
}

func (s *mirrorUsageStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.primary == nil || s.local == nil {
		return fmt.Errorf("usage store: mirror store not initialized")
	}
	if err := s.primary.EnsureSchema(ctx); err != nil {
		return err
	}
	return s.local.EnsureSchema(ctx)
}

func (s *mirrorUsageStore) Close() error {
	if s == nil {
		return nil
	}
	var firstErr error
	if s.local != nil {
		if err := s.local.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.primary != nil {
		if err := s.primary.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// pgUsageStore implements UsageStore using PostgreSQL.
type pgUsageStore struct {
	db     *sql.DB
	schema string
}

func newPgUsageStore(ctx context.Context, dsn, schema string) (*pgUsageStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("usage store: open postgres: %w", err)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("usage store: ping postgres: %w", err)
	}
	store := &pgUsageStore{db: db, schema: strings.TrimSpace(schema)}
	if err = store.EnsureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *pgUsageStore) fullTableName(name string) string {
	if s.schema == "" {
		return quoteIdentifier(name)
	}
	return quoteIdentifier(s.schema) + "." + quoteIdentifier(name)
}

func (s *pgUsageStore) fullIndexName(name string) string {
	if s.schema == "" {
		return quoteIdentifier(name)
	}
	return quoteIdentifier(s.schema) + "." + quoteIdentifier(name)
}

func (s *pgUsageStore) EnsureSchema(ctx context.Context) error {
	if s.schema != "" {
		query := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(s.schema))
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("usage store: create schema: %w", err)
		}
	}
	table := s.fullTableName("usage_records")
	createTable := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id               BIGSERIAL PRIMARY KEY,
			api_key          TEXT NOT NULL,
			model            TEXT NOT NULL,
			source           TEXT,
			auth_index       TEXT,
			failed           INTEGER NOT NULL DEFAULT 0,
			requested_at     TIMESTAMPTZ NOT NULL,
			input_tokens     BIGINT NOT NULL DEFAULT 0,
			output_tokens    BIGINT NOT NULL DEFAULT 0,
			reasoning_tokens BIGINT NOT NULL DEFAULT 0,
			cached_tokens    BIGINT NOT NULL DEFAULT 0,
			total_tokens     BIGINT NOT NULL DEFAULT 0
		)
	`, table)
	if _, err := s.db.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("usage store: create table: %w", err)
	}

	// Create indexes for common query patterns
	indexes := []string{
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_usage_requested_at_id ON %s(requested_at DESC, id DESC)", table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_usage_api_model ON %s(api_key, model)", table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_usage_failed ON %s(failed) WHERE failed = 1", table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_usage_failed_requested_source ON %s(requested_at DESC, source) WHERE failed = 1", table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_usage_source_requested_id ON %s(source, requested_at DESC, id DESC)", table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_usage_source_model_requested_id ON %s(source, model, requested_at DESC, id DESC)", table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_usage_source_norm_requested ON %s((COALESCE(NULLIF(source, ''), 'unknown')), requested_at DESC)", table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_usage_source_norm_model_requested ON %s((COALESCE(NULLIF(source, ''), 'unknown')), model, requested_at DESC)", table),
	}
	for _, idx := range indexes {
		if _, err := s.db.ExecContext(ctx, idx); err != nil {
			return fmt.Errorf("usage store: create index: %w", err)
		}
	}
	legacyIndexes := []string{
		fmt.Sprintf("DROP INDEX IF EXISTS %s", s.fullIndexName("idx_usage_requested_at")),
		fmt.Sprintf("DROP INDEX IF EXISTS %s", s.fullIndexName("idx_usage_api_key")),
	}
	for _, dropStmt := range legacyIndexes {
		if _, err := s.db.ExecContext(ctx, dropStmt); err != nil {
			return fmt.Errorf("usage store: drop legacy index: %w", err)
		}
	}

	// Migration: add method/path columns for existing databases
	pgMigrations := []string{
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS method TEXT NOT NULL DEFAULT ''", table),
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS path TEXT NOT NULL DEFAULT ''", table),
	}
	for _, m := range pgMigrations {
		if _, err := s.db.ExecContext(ctx, m); err != nil {
			return fmt.Errorf("usage store: migration: %w", err)
		}
	}
	postMigrationIndexes := []string{
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_usage_method_requested_at ON %s(method, requested_at DESC)", table),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_usage_path_requested_at ON %s(path, requested_at DESC)", table),
	}
	for _, idx := range postMigrationIndexes {
		if _, err := s.db.ExecContext(ctx, idx); err != nil {
			return fmt.Errorf("usage store: create post migration index: %w", err)
		}
	}

	return nil
}

func (s *pgUsageStore) Insert(ctx context.Context, record UsageRecord) error {
	table := s.fullTableName("usage_records")
	failed := 0
	if record.Failed {
		failed = 1
	}
	query := fmt.Sprintf(`
		INSERT INTO %s (api_key, model, source, auth_index, failed, requested_at,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
			method, path)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, table)
	_, err := s.db.ExecContext(ctx, query,
		record.APIKey,
		record.Model,
		record.Source,
		record.AuthIndex,
		failed,
		record.RequestedAt,
		record.InputTokens,
		record.OutputTokens,
		record.ReasoningTokens,
		record.CachedTokens,
		record.TotalTokens,
		record.Method,
		record.Path,
	)
	if err != nil {
		return fmt.Errorf("usage store: insert record: %w", err)
	}
	return nil
}

func (s *pgUsageStore) InsertBatch(ctx context.Context, records []UsageRecord) (added, skipped int64, err error) {
	if len(records) == 0 {
		return 0, 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("usage store: begin tx: %w", err)
	}
	defer tx.Rollback()

	table := s.fullTableName("usage_records")
	query := fmt.Sprintf(`
		INSERT INTO %s (api_key, model, source, auth_index, failed, requested_at,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
			method, path)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, table)

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return 0, 0, fmt.Errorf("usage store: prepare stmt: %w", err)
	}
	defer stmt.Close()

	for _, record := range records {
		failed := 0
		if record.Failed {
			failed = 1
		}
		_, execErr := stmt.ExecContext(ctx,
			record.APIKey,
			record.Model,
			record.Source,
			record.AuthIndex,
			failed,
			record.RequestedAt,
			record.InputTokens,
			record.OutputTokens,
			record.ReasoningTokens,
			record.CachedTokens,
			record.TotalTokens,
			record.Method,
			record.Path,
		)
		if execErr != nil {
			skipped++
			continue
		}
		added++
	}

	if err = tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("usage store: commit tx: %w", err)
	}
	return added, skipped, nil
}

func (s *pgUsageStore) ListRecordsAfterID(ctx context.Context, afterID int64, limit int) ([]UsageRecord, int64, error) {
	if limit <= 0 {
		limit = defaultMirrorSyncBatchSize
	}
	if limit > 50000 {
		limit = 50000
	}
	if afterID < 0 {
		afterID = 0
	}

	table := s.fullTableName("usage_records")
	query := fmt.Sprintf(`
		SELECT id, api_key, model, source, auth_index, failed, requested_at,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
			method, path
		FROM %s
		WHERE id > $1
		ORDER BY id ASC
		LIMIT $2
	`, table)

	rows, err := s.db.QueryContext(ctx, query, afterID, limit)
	if err != nil {
		return nil, afterID, fmt.Errorf("usage store: list records after id: %w", err)
	}
	defer rows.Close()

	records := make([]UsageRecord, 0, limit)
	lastID := afterID
	for rows.Next() {
		var (
			id     int64
			failed int
			record UsageRecord
		)
		if err = rows.Scan(
			&id,
			&record.APIKey,
			&record.Model,
			&record.Source,
			&record.AuthIndex,
			&failed,
			&record.RequestedAt,
			&record.InputTokens,
			&record.OutputTokens,
			&record.ReasoningTokens,
			&record.CachedTokens,
			&record.TotalTokens,
			&record.Method,
			&record.Path,
		); err != nil {
			return nil, afterID, fmt.Errorf("usage store: scan list records after id: %w", err)
		}
		record.Failed = failed != 0
		records = append(records, record)
		lastID = id
	}
	if err = rows.Err(); err != nil {
		return nil, afterID, fmt.Errorf("usage store: iterate list records after id: %w", err)
	}

	return records, lastID, nil
}

func (s *pgUsageStore) GetAggregatedStats(ctx context.Context) (AggregatedStats, error) {
	stats := AggregatedStats{
		APIs:           make(map[string]APIStats),
		RequestsByDay:  make(map[string]int64),
		RequestsByHour: make(map[string]int64),
		TokensByDay:    make(map[string]int64),
		TokensByHour:   make(map[string]int64),
	}
	table := s.fullTableName("usage_records")

	// Total stats
	queryTotal := fmt.Sprintf(`
		SELECT COUNT(*),
			SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END),
			COALESCE(SUM(total_tokens), 0)
		FROM %s
	`, table)
	if err := s.db.QueryRowContext(ctx, queryTotal).Scan(
		&stats.TotalRequests, &stats.SuccessCount, &stats.FailureCount, &stats.TotalTokens,
	); err != nil && err != sql.ErrNoRows {
		return stats, fmt.Errorf("usage store: query total stats: %w", err)
	}

	// By API key
	queryAPI := fmt.Sprintf(`
		SELECT api_key, COUNT(*), COALESCE(SUM(total_tokens), 0)
		FROM %s GROUP BY api_key
	`, table)
	rows, err := s.db.QueryContext(ctx, queryAPI)
	if err != nil {
		return stats, fmt.Errorf("usage store: query api stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var as APIStats
		if err = rows.Scan(&key, &as.TotalRequests, &as.TotalTokens); err != nil {
			return stats, fmt.Errorf("usage store: scan api stats: %w", err)
		}
		as.Models = make(map[string]ModelStats)
		stats.APIs[key] = as
	}

	// By API key + Model
	queryModel := fmt.Sprintf(`
		SELECT api_key, model, COUNT(*), COALESCE(SUM(total_tokens), 0)
		FROM %s GROUP BY api_key, model
	`, table)
	rows, err = s.db.QueryContext(ctx, queryModel)
	if err != nil {
		return stats, fmt.Errorf("usage store: query model stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var apiKey, model string
		var ms ModelStats
		if err = rows.Scan(&apiKey, &model, &ms.TotalRequests, &ms.TotalTokens); err != nil {
			return stats, fmt.Errorf("usage store: scan model stats: %w", err)
		}
		if api, ok := stats.APIs[apiKey]; ok {
			api.Models[model] = ms
			stats.APIs[apiKey] = api
		}
	}

	// By day
	queryDay := fmt.Sprintf(`
		SELECT TO_CHAR(requested_at, 'YYYY-MM-DD'), COUNT(*), COALESCE(SUM(total_tokens), 0)
		FROM %s GROUP BY TO_CHAR(requested_at, 'YYYY-MM-DD')
	`, table)
	rows, err = s.db.QueryContext(ctx, queryDay)
	if err != nil {
		return stats, fmt.Errorf("usage store: query day stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var count, tokens int64
		if err = rows.Scan(&day, &count, &tokens); err != nil {
			return stats, fmt.Errorf("usage store: scan day stats: %w", err)
		}
		stats.RequestsByDay[day] = count
		stats.TokensByDay[day] = tokens
	}

	// By hour
	queryHour := fmt.Sprintf(`
		SELECT TO_CHAR(requested_at, 'HH24'), COUNT(*), COALESCE(SUM(total_tokens), 0)
		FROM %s GROUP BY TO_CHAR(requested_at, 'HH24')
	`, table)
	rows, err = s.db.QueryContext(ctx, queryHour)
	if err != nil {
		return stats, fmt.Errorf("usage store: query hour stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hour string
		var count, tokens int64
		if err = rows.Scan(&hour, &count, &tokens); err != nil {
			return stats, fmt.Errorf("usage store: scan hour stats: %w", err)
		}
		stats.RequestsByHour[hour] = count
		stats.TokensByHour[hour] = tokens
	}

	// DetailCount only — full Details are available via GetDetails (paginated).
	stats.DetailCount = stats.TotalRequests

	return stats, nil
}

func (s *pgUsageStore) GetDetails(ctx context.Context, offset, limit int) ([]DetailRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}

	table := s.fullTableName("usage_records")
	query := fmt.Sprintf(`
		SELECT api_key, model, source, auth_index, failed, requested_at,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens
		FROM %s ORDER BY requested_at DESC
		LIMIT $1 OFFSET $2
	`, table)

	rows, err := s.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("usage store: query details: %w", err)
	}
	defer rows.Close()

	var details []DetailRecord
	for rows.Next() {
		var detail DetailRecord
		var failed int
		if err = rows.Scan(
			&detail.APIKey, &detail.Model, &detail.Source, &detail.AuthIndex,
			&failed, &detail.RequestedAt,
			&detail.InputTokens, &detail.OutputTokens, &detail.ReasoningTokens,
			&detail.CachedTokens, &detail.TotalTokens,
		); err != nil {
			return nil, fmt.Errorf("usage store: scan detail: %w", err)
		}
		detail.Failed = (failed != 0)
		details = append(details, detail)
	}

	return details, nil
}

func (s *pgUsageStore) DeleteOldRecords(ctx context.Context, retentionDays int) (deleted int64, err error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	table := s.fullTableName("usage_records")
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	query := fmt.Sprintf("DELETE FROM %s WHERE requested_at < $1", table)
	result, err := s.db.ExecContext(ctx, query, cutoff)
	if err != nil {
		return 0, fmt.Errorf("usage store: delete old records: %w", err)
	}
	deleted, _ = result.RowsAffected()
	return deleted, nil
}

func (s *pgUsageStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// sqliteUsageStore implements UsageStore using SQLite.
type sqliteUsageStore struct {
	db   *sql.DB
	path string
}

func newSQLiteUsageStore(authDir string) (*sqliteUsageStore, error) {
	if authDir == "" {
		cwd, _ := os.Getwd()
		authDir = cwd
	}
	return newSQLiteUsageStoreAtPath(filepath.Join(authDir, defaultLocalUsageFileName))
}

func newSQLiteUsageStoreAtPath(dbPath string) (*sqliteUsageStore, error) {
	cleanPath := filepath.Clean(strings.TrimSpace(dbPath))
	if cleanPath == "" || cleanPath == "." {
		return nil, fmt.Errorf("usage store: sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return nil, fmt.Errorf("usage store: create sqlite dir: %w", err)
	}
	db, err := sql.Open("sqlite", cleanPath)
	if err != nil {
		return nil, fmt.Errorf("usage store: open sqlite: %w", err)
	}
	// Enable WAL mode for better concurrent access
	if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("usage store: enable WAL: %w", err)
	}
	// Read performance PRAGMAs: 64MB cache, 256MB mmap, temp tables in memory
	for _, pragma := range []string{
		"PRAGMA cache_size=-64000",
		"PRAGMA mmap_size=268435456",
		"PRAGMA temp_store=MEMORY",
	} {
		if _, err = db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("usage store: set pragma: %w", err)
		}
	}
	store := &sqliteUsageStore{db: db, path: cleanPath}
	if err = store.EnsureSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *sqliteUsageStore) EnsureSchema(ctx context.Context) error {
	createTable := `
		CREATE TABLE IF NOT EXISTS usage_records (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			api_key          TEXT NOT NULL,
			model            TEXT NOT NULL,
			source           TEXT,
			auth_index       TEXT,
			failed           INTEGER NOT NULL DEFAULT 0,
			requested_at     INTEGER NOT NULL,
			input_tokens     INTEGER NOT NULL DEFAULT 0,
			output_tokens    INTEGER NOT NULL DEFAULT 0,
			reasoning_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens    INTEGER NOT NULL DEFAULT 0,
			total_tokens     INTEGER NOT NULL DEFAULT 0
		)
	`
	if _, err := s.db.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("usage store: create table: %w", err)
	}

	// Create indexes for common query patterns
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_usage_requested_at_id ON usage_records(requested_at DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_usage_api_model ON usage_records(api_key, model)",
		"CREATE INDEX IF NOT EXISTS idx_usage_failed ON usage_records(failed)",
		"CREATE INDEX IF NOT EXISTS idx_usage_failed_requested_source ON usage_records(requested_at DESC, source) WHERE failed = 1",
		"CREATE INDEX IF NOT EXISTS idx_usage_source_requested ON usage_records(source, requested_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_usage_source_model_requested ON usage_records(source, model, requested_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_usage_source_norm_requested ON usage_records((COALESCE(NULLIF(source, ''), 'unknown')), requested_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_usage_source_norm_model_requested ON usage_records((COALESCE(NULLIF(source, ''), 'unknown')), model, requested_at DESC)",
	}
	for _, idx := range indexes {
		if _, err := s.db.ExecContext(ctx, idx); err != nil {
			return fmt.Errorf("usage store: create index: %w", err)
		}
	}
	legacyIndexes := []string{
		"DROP INDEX IF EXISTS idx_usage_requested_at",
		"DROP INDEX IF EXISTS idx_usage_api_key",
	}
	for _, dropStmt := range legacyIndexes {
		if _, err := s.db.ExecContext(ctx, dropStmt); err != nil {
			return fmt.Errorf("usage store: drop legacy index: %w", err)
		}
	}

	// Migration: add method/path columns for existing databases
	migrations := []string{
		"ALTER TABLE usage_records ADD COLUMN method TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE usage_records ADD COLUMN path TEXT NOT NULL DEFAULT ''",
	}
	for _, m := range migrations {
		_, _ = s.db.ExecContext(ctx, m) // ignore "duplicate column" errors
	}
	postMigrationIndexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_usage_method_requested_at ON usage_records(method, requested_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_usage_path_requested_at ON usage_records(path, requested_at DESC)",
	}
	for _, idx := range postMigrationIndexes {
		if _, err := s.db.ExecContext(ctx, idx); err != nil {
			return fmt.Errorf("usage store: create post migration index: %w", err)
		}
	}

	return nil
}

func (s *sqliteUsageStore) Reset(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("usage store: sqlite store not initialized")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("usage store: begin reset tx: %w", err)
	}
	defer tx.Rollback()

	if _, err = tx.ExecContext(ctx, "DELETE FROM usage_records"); err != nil {
		return fmt.Errorf("usage store: reset records: %w", err)
	}
	_, _ = tx.ExecContext(ctx, "DELETE FROM sqlite_sequence WHERE name = 'usage_records'")

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("usage store: commit reset tx: %w", err)
	}
	return nil
}

func (s *sqliteUsageStore) Insert(ctx context.Context, record UsageRecord) error {
	failed := 0
	if record.Failed {
		failed = 1
	}
	query := `
		INSERT INTO usage_records (api_key, model, source, auth_index, failed, requested_at,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
			method, path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(ctx, query,
		record.APIKey,
		record.Model,
		record.Source,
		record.AuthIndex,
		failed,
		record.RequestedAt.Unix(),
		record.InputTokens,
		record.OutputTokens,
		record.ReasoningTokens,
		record.CachedTokens,
		record.TotalTokens,
		record.Method,
		record.Path,
	)
	if err != nil {
		return fmt.Errorf("usage store: insert record: %w", err)
	}
	return nil
}

func (s *sqliteUsageStore) InsertBatch(ctx context.Context, records []UsageRecord) (added, skipped int64, err error) {
	if len(records) == 0 {
		return 0, 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("usage store: begin tx: %w", err)
	}
	defer tx.Rollback()

	query := `
		INSERT INTO usage_records (api_key, model, source, auth_index, failed, requested_at,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
			method, path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return 0, 0, fmt.Errorf("usage store: prepare stmt: %w", err)
	}
	defer stmt.Close()

	for _, record := range records {
		failed := 0
		if record.Failed {
			failed = 1
		}
		_, execErr := stmt.ExecContext(ctx,
			record.APIKey,
			record.Model,
			record.Source,
			record.AuthIndex,
			failed,
			record.RequestedAt.Unix(),
			record.InputTokens,
			record.OutputTokens,
			record.ReasoningTokens,
			record.CachedTokens,
			record.TotalTokens,
			record.Method,
			record.Path,
		)
		if execErr != nil {
			skipped++
			continue
		}
		added++
	}

	if err = tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("usage store: commit tx: %w", err)
	}
	return added, skipped, nil
}

func (s *sqliteUsageStore) GetAggregatedStats(ctx context.Context) (AggregatedStats, error) {
	stats := AggregatedStats{
		APIs:           make(map[string]APIStats),
		RequestsByDay:  make(map[string]int64),
		RequestsByHour: make(map[string]int64),
		TokensByDay:    make(map[string]int64),
		TokensByHour:   make(map[string]int64),
	}

	// Total stats
	queryTotal := `
		SELECT COUNT(*),
			SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END),
			COALESCE(SUM(total_tokens), 0)
		FROM usage_records
	`
	if err := s.db.QueryRowContext(ctx, queryTotal).Scan(
		&stats.TotalRequests, &stats.SuccessCount, &stats.FailureCount, &stats.TotalTokens,
	); err != nil && err != sql.ErrNoRows {
		return stats, fmt.Errorf("usage store: query total stats: %w", err)
	}

	// By API key
	queryAPI := `
		SELECT api_key, COUNT(*), COALESCE(SUM(total_tokens), 0)
		FROM usage_records GROUP BY api_key
	`
	rows, err := s.db.QueryContext(ctx, queryAPI)
	if err != nil {
		return stats, fmt.Errorf("usage store: query api stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var as APIStats
		if err = rows.Scan(&key, &as.TotalRequests, &as.TotalTokens); err != nil {
			return stats, fmt.Errorf("usage store: scan api stats: %w", err)
		}
		as.Models = make(map[string]ModelStats)
		stats.APIs[key] = as
	}

	// By API key + Model
	queryModel := `
		SELECT api_key, model, COUNT(*), COALESCE(SUM(total_tokens), 0)
		FROM usage_records GROUP BY api_key, model
	`
	rows, err = s.db.QueryContext(ctx, queryModel)
	if err != nil {
		return stats, fmt.Errorf("usage store: query model stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var apiKey, model string
		var ms ModelStats
		if err = rows.Scan(&apiKey, &model, &ms.TotalRequests, &ms.TotalTokens); err != nil {
			return stats, fmt.Errorf("usage store: scan model stats: %w", err)
		}
		if api, ok := stats.APIs[apiKey]; ok {
			api.Models[model] = ms
			stats.APIs[apiKey] = api
		}
	}

	// By day (SQLite: convert unix timestamp to date)
	queryDay := `
		SELECT DATE(requested_at, 'unixepoch', 'localtime'), COUNT(*), COALESCE(SUM(total_tokens), 0)
		FROM usage_records GROUP BY DATE(requested_at, 'unixepoch', 'localtime')
	`
	rows, err = s.db.QueryContext(ctx, queryDay)
	if err != nil {
		return stats, fmt.Errorf("usage store: query day stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var count, tokens int64
		if err = rows.Scan(&day, &count, &tokens); err != nil {
			return stats, fmt.Errorf("usage store: scan day stats: %w", err)
		}
		stats.RequestsByDay[day] = count
		stats.TokensByDay[day] = tokens
	}

	// By hour (SQLite: convert unix timestamp to hour)
	queryHour := `
		SELECT strftime('%H', requested_at, 'unixepoch', 'localtime'), COUNT(*), COALESCE(SUM(total_tokens), 0)
		FROM usage_records GROUP BY strftime('%H', requested_at, 'unixepoch', 'localtime')
	`
	rows, err = s.db.QueryContext(ctx, queryHour)
	if err != nil {
		return stats, fmt.Errorf("usage store: query hour stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hour string
		var count, tokens int64
		if err = rows.Scan(&hour, &count, &tokens); err != nil {
			return stats, fmt.Errorf("usage store: scan hour stats: %w", err)
		}
		stats.RequestsByHour[hour] = count
		stats.TokensByHour[hour] = tokens
	}

	// DetailCount only — full Details are available via GetDetails (paginated).
	stats.DetailCount = stats.TotalRequests

	return stats, nil
}

func (s *sqliteUsageStore) GetDetails(ctx context.Context, offset, limit int) ([]DetailRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT api_key, model, source, auth_index, failed, requested_at,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens
		FROM usage_records ORDER BY requested_at DESC
		LIMIT ? OFFSET ?
	`

	rows, err := s.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("usage store: query details: %w", err)
	}
	defer rows.Close()

	var details []DetailRecord
	for rows.Next() {
		var detail DetailRecord
		var failed int
		var unixTime int64
		if err = rows.Scan(
			&detail.APIKey, &detail.Model, &detail.Source, &detail.AuthIndex,
			&failed, &unixTime,
			&detail.InputTokens, &detail.OutputTokens, &detail.ReasoningTokens,
			&detail.CachedTokens, &detail.TotalTokens,
		); err != nil {
			return nil, fmt.Errorf("usage store: scan detail: %w", err)
		}
		detail.Failed = (failed != 0)
		detail.RequestedAt = time.Unix(unixTime, 0)
		details = append(details, detail)
	}

	return details, nil
}

func (s *sqliteUsageStore) DeleteOldRecords(ctx context.Context, retentionDays int) (deleted int64, err error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
	query := "DELETE FROM usage_records WHERE requested_at < ?"
	result, err := s.db.ExecContext(ctx, query, cutoff)
	if err != nil {
		return 0, fmt.Errorf("usage store: delete old records: %w", err)
	}
	deleted, _ = result.RowsAffected()
	return deleted, nil
}

func (s *sqliteUsageStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func quoteIdentifier(identifier string) string {
	replaced := strings.ReplaceAll(identifier, "\"", "\"\"")
	return "\"" + replaced + "\""
}
