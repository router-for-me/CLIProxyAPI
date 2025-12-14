package metrics

import (
	"context"
	"database/sql"
	"time"
)

// Totals represents aggregated overall usage statistics across all recorded requests.
type Totals struct {
	TotalTokens   int64 `json:"total_tokens"`   // Sum of all tokens (prompt + completion) used
	TotalRequests int64 `json:"total_requests"` // Total number of API requests recorded
}

// ModelStats contains per-model aggregated metrics, used for breakdown views in the dashboard.
type ModelStats struct {
	Model        string `json:"model"`         // Name of the model (e.g., "gpt-4", "gpt-3.5-turbo")
	Tokens       int64  `json:"tokens"`        // Total tokens consumed using this model
	Requests     int64  `json:"requests"`      // Number of requests made with this model
	AvgLatencyMs int64  `json:"avg_latency_ms"` // Average request latency in milliseconds (rounded via AVG)
}

// TimeSeriesBucket represents aggregated data for a single time bucket (typically hourly).
// Used by the dashboard to render the usage-over-time line chart.
type TimeSeriesBucket struct {
	BucketStart time.Time `json:"bucket_start"` // Start time of the bucket (hour-aligned)
	Tokens      int64     `json:"tokens"`       // Total tokens used in this bucket
	Requests    int64     `json:"requests"`     // Number of requests in this bucket
}

// MetricsQuery defines optional filters for all metric retrieval methods.
// All fields are pointers to allow selective application of filters.
type MetricsQuery struct {
	From   *time.Time // Inclusive start of time range (UTC recommended)
	To     *time.Time // Inclusive end of time range (UTC recommended)
	Model  *string    // Filter to a specific model name
	APIKey *string    // Filter by anonymized API key hash (for per-user stats, if enabled)
}

// GetTotals returns overall aggregated totals based on the provided query filters.
// Returns zero values if no rows match (rather than an error).
func (s *SQLiteMetricsStore) GetTotals(ctx context.Context, q MetricsQuery) (*Totals, error) {
	// Base query selects sum of tokens and count of requests
	query := `
		SELECT 
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COUNT(*) AS total_requests
		FROM usage_metrics
		WHERE 1=1
	`
	args := []interface{}{}

	// Dynamically append filters as needed
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
	// Query a single row and scan into Totals struct
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&totals.TotalTokens,
		&totals.TotalRequests,
	)
	if err == sql.ErrNoRows {
		// No rows found → return empty (zeroed) totals instead of error
		return &Totals{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &totals, nil
}

// GetByModel returns usage statistics grouped by model, ordered by total token usage descending.
// Time range filtering is supported; model-specific filtering is intentionally omitted
// (as the purpose is to compare across models).
func (s *SQLiteMetricsStore) GetByModel(ctx context.Context, q MetricsQuery) ([]ModelStats, error) {
	query := `
		SELECT 
			model,
			SUM(total_tokens) AS tokens,
			COUNT(*) AS requests,
			COALESCE(AVG(latency_ms), 0) AS avg_latency
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

	// Group by model and sort by token usage (most expensive first)
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

	// Check for errors encountered during iteration
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return stats, nil
}

// GetTimeSeries returns hourly aggregated metrics for time-series visualization.
// Buckets are aligned to full hours using SQLite's strftime function.
// The bucketHours parameter is currently fixed to 1 in callers, but kept for future flexibility.
func (s *SQLiteMetricsStore) GetTimeSeries(ctx context.Context, q MetricsQuery, bucketHours int) ([]TimeSeriesBucket, error) {
	// Use strftime to truncate timestamp to the start of each hour
	// Format: "YYYY-MM-DD HH:00:00"
	query := `
		SELECT 
			strftime('%Y-%m-%d %H:00:00', timestamp) AS bucket_start,
			SUM(total_tokens) AS tokens,
			COUNT(*) AS requests
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

	// Group and order by the formatted hour string
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

		// Parse the SQLite-formatted string back into a time.Time (local timezone assumed unless UTC stored)
		// Format matches strftime output: "2006-01-02 15:00:00"
		parsedTime, err := time.Parse("2006-01-02 15:04:05", timeStr)
		if err != nil {
			return nil, err
		}
		b.BucketStart = parsedTime
		buckets = append(buckets, b)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return buckets, nil
}