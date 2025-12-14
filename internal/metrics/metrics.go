// internal/metrics/metrics.go

package metrics

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
)

// UsageMetric represents a single API call record containing token usage,
// latency, and additional metadata for analytics and monitoring purposes.
type UsageMetric struct {
	Timestamp        time.Time // Timestamp when the request completed (UTC recommended)
	Model            string    // Name of the LLM model used (e.g., "gpt-4", "gpt-3.5-turbo")
	PromptTokens     int       // Number of tokens in the input prompt
	CompletionTokens int       // Number of tokens generated in the completion/response
	TotalTokens      int       // Total tokens used (PromptTokens + CompletionTokens)
	RequestID        string    // Unique identifier for the request (for tracing/debugging)
	Status           string    // Request outcome, typically HTTP status code (e.g., "200", "500") or custom label
	LatencyMs        int64     // Request latency in milliseconds
	APIKeyHash       string    // SHA-256 hash of the API key (for anonymized per-key tracking)
}

// MetricsStore defines the interface for persisting and querying usage metrics.
// It is used by request handlers to record data and by aggregators/dashboard endpoints
// to retrieve summarized statistics.
type MetricsStore interface {
	// RecordUsage stores a single usage metric.
	// Should be safe for concurrent calls.
	RecordUsage(ctx context.Context, metric UsageMetric) error

	// Close releases any resources (e.g., database connections) held by the store.
	Close() error

	// GetTotals returns aggregated totals (tokens, requests, etc.) based on the provided query filters.
	GetTotals(ctx context.Context, q MetricsQuery) (*Totals, error)

	// GetByModel returns per-model statistics (token usage, request count, average latency) with optional filters.
	GetByModel(ctx context.Context, q MetricsQuery) ([]ModelStats, error)

	// GetTimeSeries returns time-bucketed aggregates (e.g., hourly/daily) for requests and tokens.
	// bucketHours defines the size of each bucket in hours (e.g., 1 for hourly, 24 for daily).
	GetTimeSeries(ctx context.Context, q MetricsQuery, bucketHours int) ([]TimeSeriesBucket, error)
}

// ====================================================================
// Global store mechanism (used in main.go and throughout the application)
// ====================================================================

// globalMetricsStore holds the currently active MetricsStore instance.
// It allows any package to access the store without explicit dependency injection.
var globalMetricsStore MetricsStore

// GetGlobalMetricsStore returns the current global MetricsStore instance.
// Returns nil if no store has been initialized yet.
func GetGlobalMetricsStore() MetricsStore {
	return globalMetricsStore
}

// SetGlobalMetricsStore sets the global MetricsStore instance.
// Typically called once during application startup.
// Logs a debug message upon successful initialization.
func SetGlobalMetricsStore(store MetricsStore) {
	globalMetricsStore = store
	log.Debug("Global Metrics Store initialized.")
}