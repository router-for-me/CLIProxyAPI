package metrics

import (
	"context"
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
)

// SQLiteMetricsStore является конкретной реализацией MetricsStore для SQLite.
type SQLiteMetricsStore struct {
	db *sql.DB
}

// NewMetricsStore создает новый экземпляр MetricsStore, инициализируя подключение к SQLite.
// Имя функции изменено на NewMetricsStore, чтобы соответствовать вызову в main.go.
func NewMetricsStore(dbPath string) (*SQLiteMetricsStore, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	// Создание таблицы
	schema := `
	CREATE TABLE IF NOT EXISTS usage_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp DATETIME NOT NULL,
		model TEXT NOT NULL,
		prompt_tokens INTEGER,
		completion_tokens INTEGER,
		total_tokens INTEGER,
		request_id TEXT,
		status TEXT,
		latency_ms INTEGER,
		api_key_hash TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_timestamp ON usage_metrics(timestamp);
	CREATE INDEX IF NOT EXISTS idx_model ON usage_metrics(model);
	CREATE INDEX IF NOT EXISTS idx_api_key ON usage_metrics(api_key_hash);
	`

	if _, err := db.Exec(schema); err != nil {
		db.Close() // Закрываем соединение при ошибке создания схемы
		return nil, err
	}

	return &SQLiteMetricsStore{db: db}, nil
}

// RecordUsage сохраняет метрику в базу данных.
func (s *SQLiteMetricsStore) RecordUsage(ctx context.Context, metric UsageMetric) error {
	query := `
		INSERT INTO usage_metrics 
		(timestamp, model, prompt_tokens, completion_tokens, total_tokens, 
		 request_id, status, latency_ms, api_key_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
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

// Close закрывает соединение с базой данных.
func (s *SQLiteMetricsStore) Close() error {
	return s.db.Close()
}