package metrics

import (
	"context"
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
)

// SQLiteMetricsStore is the concrete implementation of the MetricsStore interface
// that uses SQLite as the underlying persistent storage.
// It is designed to be lightweight, file-based, and suitable for single-process applications
// such as the CLI proxy.
type SQLiteMetricsStore struct {
	db *sql.DB // Underlying SQLite database connection
}

// NewMetricsStore creates and initializes a new MetricsStore backed by SQLite.
// It opens (or creates) a database file at the given path, applies the required schema,
// and creates necessary indexes for efficient querying.
//
// The function name is kept as NewMetricsStore to match existing calls in main.go.
// On success, it returns a ready-to-use store instance. On failure, it ensures
// any partially opened connection is closed before returning the error.
func NewMetricsStore(dbPath string) (*SQLiteMetricsStore, error) {
	// Open SQLite connection (creates the file if it doesn't exist)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	// Define the database schema: table for usage metrics and performance indexes
	schema := `
		CREATE TABLE IF NOT EXISTS usage_metrics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL,           -- When the request completed
			model TEXT NOT NULL,                   -- LLM model name (e.g., "gpt-4")
			prompt_tokens INTEGER,                 -- Tokens in the input prompt
			completion_tokens INTEGER,             -- Tokens in the generated response
			total_tokens INTEGER,                  -- prompt_tokens + completion_tokens
			request_id TEXT,                       -- Unique request identifier (for tracing)
			status TEXT,                           -- Outcome (e.g., HTTP status or "success")
			latency_ms INTEGER,                    -- Request duration in milliseconds
			api_key_hash TEXT,                     -- SHA-256 hash of API key (anonymized)
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		-- Index on timestamp for efficient time-range queries
		CREATE INDEX IF NOT EXISTS idx_timestamp ON usage_metrics(timestamp);

		-- Index on model for fast per-model aggregations and filtering
		CREATE INDEX IF NOT EXISTS idx_model ON usage_metrics(model);

		-- Index on api_key_hash for potential per-user filtering (if enabled)
		CREATE INDEX IF NOT EXISTS idx_api_key ON usage_metrics(api_key_hash);
	`

	// Execute schema creation (idempotent due to "IF NOT EXISTS")
	if _, err := db.Exec(schema); err != nil {
		// Clean up the connection if schema setup fails
		db.Close()
		return nil, err
	}

	return &SQLiteMetricsStore{db: db}, nil
}

// RecordUsage persists a single usage metric into the SQLite database.
// It performs an INSERT with all relevant fields from the UsageMetric struct.
// The operation is context-aware and supports cancellation/timeout.
func (s *SQLiteMetricsStore) RecordUsage(ctx context.Context, metric UsageMetric) error {
	query := `
		INSERT INTO usage_metrics 
		(timestamp, model, prompt_tokens, completion_tokens, total_tokens, 
		 request_id, status, latency_ms, api_key_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	// Execute the insert with parameters in the correct order
	_, err := s.db.ExecContext(ctx, query,
		metric.Timestamp,
		metric.Model,
		metric.PromptTokens,
		metric.CompletionTokens,
		metric.TotalTokens,
		metric.RequestID,
		metric.Status,
		metric.LatencyMs,
		metric.APIKeyHash,
	)
	return err
}

// Close gracefully shuts down the SQLite connection and releases associated resources.
// Should be called (typically via defer) when the application exits or the store is no longer needed.
func (s *SQLiteMetricsStore) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}