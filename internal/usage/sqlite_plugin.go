// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// This file implements a SQLite-based persistence plugin for usage statistics.
package usage

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	log "github.com/sirupsen/logrus"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// SQLitePluginEnvKey is the environment variable name for the SQLite database path.
// If set, the SQLite plugin will be automatically enabled and registered.
const SQLitePluginEnvKey = "USAGE_SQLITE_PATH"

var sqlitePluginInstance *SQLitePlugin

func init() {
	dbPath := os.Getenv(SQLitePluginEnvKey)
	if dbPath == "" {
		return
	}
	plugin, err := NewSQLitePlugin(dbPath)
	if err != nil {
		log.Errorf("usage: failed to initialize SQLite plugin: %v", err)
		return
	}
	sqlitePluginInstance = plugin
	coreusage.RegisterPlugin(plugin)
	log.Infof("usage: SQLite plugin registered with database at %s", dbPath)

	// Restore historical data from SQLite to in-memory statistics on startup
	if err := plugin.RestoreToMemory(GetRequestStatistics()); err != nil {
		log.Errorf("usage: failed to restore statistics from SQLite: %v", err)
	}
}

// GetSQLitePlugin returns the global SQLite plugin instance, or nil if not enabled.
func GetSQLitePlugin() *SQLitePlugin {
	return sqlitePluginInstance
}

// SQLitePlugin persists usage records to a SQLite database.
// It implements coreusage.Plugin to receive usage records emitted by the runtime.
type SQLitePlugin struct {
	db   *sql.DB
	mu   sync.Mutex
	path string

	// insertStmt is a prepared statement for inserting usage records
	insertStmt *sql.Stmt
}

// NewSQLitePlugin creates a new SQLite plugin with the given database path.
// It initializes the database schema if it doesn't exist.
//
// Parameters:
//   - dbPath: Path to the SQLite database file
//
// Returns:
//   - *SQLitePlugin: A new SQLite plugin instance
//   - error: An error if the database could not be opened or initialized
func NewSQLitePlugin(dbPath string) (*SQLitePlugin, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}

	// Set connection pool settings for better concurrency
	db.SetMaxOpenConns(1) // SQLite performs best with single writer
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0) // Keep connection open

	plugin := &SQLitePlugin{
		db:   db,
		path: dbPath,
	}

	if err := plugin.initSchema(); err != nil {
		db.Close()
		return nil, err
	}

	if err := plugin.prepareStatements(); err != nil {
		db.Close()
		return nil, err
	}

	return plugin, nil
}

// initSchema creates the usage_records table and indexes if they don't exist.
func (p *SQLitePlugin) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS usage_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		provider TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		api_key TEXT NOT NULL DEFAULT '',
		auth_id TEXT NOT NULL DEFAULT '',
		auth_index TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		requested_at DATETIME NOT NULL,
		failed INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		reasoning_tokens INTEGER NOT NULL DEFAULT 0,
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_usage_requested_at ON usage_records(requested_at);
	CREATE INDEX IF NOT EXISTS idx_usage_api_key ON usage_records(api_key);
	CREATE INDEX IF NOT EXISTS idx_usage_model ON usage_records(model);
	CREATE INDEX IF NOT EXISTS idx_usage_provider ON usage_records(provider);
	`
	_, err := p.db.Exec(schema)
	return err
}

// prepareStatements prepares SQL statements for reuse.
func (p *SQLitePlugin) prepareStatements() error {
	var err error
	p.insertStmt, err = p.db.Prepare(`
		INSERT INTO usage_records (
			provider, model, api_key, auth_id, auth_index, source,
			requested_at, failed,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	return err
}

// HandleUsage implements coreusage.Plugin.
// It persists the usage record to the SQLite database.
//
// Parameters:
//   - ctx: The context for the usage record
//   - record: The usage record to persist
func (p *SQLitePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || p.db == nil {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	failed := 0
	if record.Failed {
		failed = 1
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	_, err := p.insertStmt.Exec(
		record.Provider,
		record.Model,
		record.APIKey,
		record.AuthID,
		record.AuthIndex,
		record.Source,
		timestamp.UTC(),
		failed,
		record.Detail.InputTokens,
		record.Detail.OutputTokens,
		record.Detail.ReasoningTokens,
		record.Detail.CachedTokens,
		record.Detail.TotalTokens,
	)
	if err != nil {
		log.Errorf("usage: failed to insert record to SQLite: %v", err)
	}
}

// RestoreToMemory loads all historical records from SQLite and merges them
// into the in-memory RequestStatistics store.
//
// Parameters:
//   - stats: The RequestStatistics instance to restore data into
//
// Returns:
//   - error: An error if the restoration failed
func (p *SQLitePlugin) RestoreToMemory(stats *RequestStatistics) error {
	if p == nil || p.db == nil || stats == nil {
		return nil
	}

	rows, err := p.db.Query(`
		SELECT 
			provider, model, api_key, auth_id, auth_index, source,
			requested_at, failed,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens
		FROM usage_records
		ORDER BY requested_at ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var count int64
	for rows.Next() {
		var (
			provider, model, apiKey, authID, authIndex, source string
			requestedAt                                        time.Time
			failed                                             int
			inputTokens, outputTokens, reasoningTokens         int64
			cachedTokens, totalTokens                          int64
		)

		if err := rows.Scan(
			&provider, &model, &apiKey, &authID, &authIndex, &source,
			&requestedAt, &failed,
			&inputTokens, &outputTokens, &reasoningTokens, &cachedTokens, &totalTokens,
		); err != nil {
			log.Errorf("usage: failed to scan row from SQLite: %v", err)
			continue
		}

		// Reconstruct the record and feed it to the statistics store
		record := coreusage.Record{
			Provider:    provider,
			Model:       model,
			APIKey:      apiKey,
			AuthID:      authID,
			AuthIndex:   authIndex,
			Source:      source,
			RequestedAt: requestedAt,
			Failed:      failed != 0,
			Detail: coreusage.Detail{
				InputTokens:     inputTokens,
				OutputTokens:    outputTokens,
				ReasoningTokens: reasoningTokens,
				CachedTokens:    cachedTokens,
				TotalTokens:     totalTokens,
			},
		}

		// Use a background context since we're restoring, not serving a request
		stats.Record(context.Background(), record)
		count++
	}

	if err := rows.Err(); err != nil {
		return err
	}

	log.Infof("usage: restored %d records from SQLite to memory", count)
	return nil
}

// Close closes the database connection and releases resources.
func (p *SQLitePlugin) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.insertStmt != nil {
		p.insertStmt.Close()
	}
	if p.db != nil {
		return p.db.Close()
	}
	return nil
}

// RecordCount returns the total number of records in the database.
// This is primarily useful for testing and diagnostics.
func (p *SQLitePlugin) RecordCount() (int64, error) {
	if p == nil || p.db == nil {
		return 0, nil
	}
	var count int64
	err := p.db.QueryRow("SELECT COUNT(*) FROM usage_records").Scan(&count)
	return count, err
}

// ImportSnapshot persists all records from a StatisticsSnapshot to SQLite.
// This should be called when importing usage data via the management API
// to ensure imported records survive restarts.
func (p *SQLitePlugin) ImportSnapshot(snapshot StatisticsSnapshot) (added int64, err error) {
	if p == nil || p.db == nil {
		return 0, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	tx, err := p.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	stmt, err := tx.Prepare(`
		INSERT INTO usage_records (
			provider, model, api_key, auth_id, auth_index, source,
			requested_at, failed,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for apiKey, apiSnapshot := range snapshot.APIs {
		for model, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				failed := 0
				if detail.Failed {
					failed = 1
				}
				timestamp := detail.Timestamp
				if timestamp.IsZero() {
					timestamp = time.Now()
				}

				_, err = stmt.Exec(
					"",        // provider not stored in snapshot
					model,
					apiKey,
					"",        // auth_id not stored in snapshot
					detail.AuthIndex,
					detail.Source,
					timestamp.UTC(),
					failed,
					detail.Tokens.InputTokens,
					detail.Tokens.OutputTokens,
					detail.Tokens.ReasoningTokens,
					detail.Tokens.CachedTokens,
					detail.Tokens.TotalTokens,
				)
				if err != nil {
					return added, err
				}
				added++
			}
		}
	}

	err = tx.Commit()
	if err != nil {
		return 0, err
	}

	log.Infof("usage: imported %d records to SQLite", added)
	return added, nil
}
