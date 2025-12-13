package metrics

import (
	"context"
	"database/sql"
	"time"
)

type Totals struct {
	TotalTokens   int64 `json:"total_tokens"`
	TotalRequests int64 `json:"total_requests"`
}

type ModelStats struct {
	Model        string `json:"model"`
	Tokens       int64  `json:"tokens"`
	Requests     int64  `json:"requests"`
	AvgLatencyMs int64  `json:"avg_latency_ms"`
}

type TimeSeriesBucket struct {
	BucketStart time.Time `json:"bucket_start"`
	Tokens      int64     `json:"tokens"`
	Requests    int64     `json:"requests"`
}

type MetricsQuery struct {
	From   *time.Time
	To     *time.Time
	Model  *string
	APIKey *string
}

// GetTotals возвращает общую статистику.
func (s *SQLiteMetricsStore) GetTotals(ctx context.Context, q MetricsQuery) (*Totals, error) {
	query := `
		SELECT 
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			COUNT(*) as total_requests
		FROM usage_metrics
		WHERE 1=1
	`
	args := []interface{}{}

	if q.From != nil {
		query += " AND timestamp >= ?"
		args = append(args, *q.From)
	}
	if q.To != nil {
		query += " AND timestamp <= ?"
		args = append(args, *q.To)
	}
	if q.Model != nil {
		query += " AND model = ?"
		args = append(args, *q.Model)
	}
	if q.APIKey != nil {
		query += " AND api_key_hash = ?"
		args = append(args, *q.APIKey)
	}

	var totals Totals
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&totals.TotalTokens,
		&totals.TotalRequests,
	)
	if err == sql.ErrNoRows {
		return &Totals{}, nil
	}
	return &totals, err
}

// GetByModel возвращает статистику по моделям.
func (s *SQLiteMetricsStore) GetByModel(ctx context.Context, q MetricsQuery) ([]ModelStats, error) {
	query := `
		SELECT 
			model,
			SUM(total_tokens) as tokens,
			COUNT(*) as requests,
			COALESCE(AVG(latency_ms), 0) as avg_latency
		FROM usage_metrics
		WHERE 1=1
	`
	args := []interface{}{}

	if q.From != nil {
		query += " AND timestamp >= ?"
		args = append(args, *q.From)
	}
	if q.To != nil {
		query += " AND timestamp <= ?"
		args = append(args, *q.To)
	}

	query += " GROUP BY model ORDER BY tokens DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []ModelStats
	for rows.Next() {
		var s ModelStats
		if err := rows.Scan(&s.Model, &s.Tokens, &s.Requests, &s.AvgLatencyMs); err != nil {
			return nil, err
		}
		stats = append(stats, s)
	}
	return stats, rows.Err()
}

// GetTimeSeries возвращает данные для графиков.
func (s *SQLiteMetricsStore) GetTimeSeries(ctx context.Context, q MetricsQuery, bucketHours int) ([]TimeSeriesBucket, error) {
	query := `
		SELECT 
			strftime('%Y-%m-%d %H:00:00', timestamp) as bucket_start,
			SUM(total_tokens) as tokens,
			COUNT(*) as requests
		FROM usage_metrics
		WHERE 1=1
	`
	args := []interface{}{}

	if q.From != nil {
		query += " AND timestamp >= ?"
		args = append(args, *q.From)
	}
	if q.To != nil {
		query += " AND timestamp <= ?"
		args = append(args, *q.To)
	}
	if q.Model != nil {
		query += " AND model = ?"
		args = append(args, *q.Model)
	}

	query += " GROUP BY bucket_start ORDER BY bucket_start ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var buckets []TimeSeriesBucket
	for rows.Next() {
		var b TimeSeriesBucket
		var timeStr string
		if err := rows.Scan(&timeStr, &b.Tokens, &b.Requests); err != nil {
			return nil, err
		}
		// Парсинг времени должен соответствовать формату strftime (без T и Z, так как SQLite хранит строки)
		b.BucketStart, err = time.Parse("2006-01-02 15:04:05", timeStr)
		if err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}
	return buckets, rows.Err()
}