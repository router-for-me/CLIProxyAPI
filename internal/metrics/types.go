// Package metrics provides request metrics collection, storage, and retrieval
// for the KorProxy monitoring system.
package metrics

import (
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

// RequestRecord represents a single API request metric.
type RequestRecord struct {
	Timestamp   time.Time           `json:"timestamp"`
	Provider    string              `json:"provider"`
	Model       string              `json:"model"`
	Profile     string              `json:"profile"`
	RequestType routing.RequestType `json:"request_type"`
	LatencyMs   int64               `json:"latency_ms"`
	ErrorType   string              `json:"error_type,omitempty"`
	Success     bool                `json:"success"`
}

// DailyMetrics stores aggregated metrics for a single day.
type DailyMetrics struct {
	Date         string                       `json:"date"`
	Requests     []RequestRecord              `json:"requests"`
	ByProvider   map[string]*ProviderStats    `json:"by_provider"`
	ByType       map[string]*TypeStats        `json:"by_type"`
	ByProfile    map[string]*ProfileStats     `json:"by_profile"`
	TotalCount   int64                        `json:"total_count"`
	FailureCount int64                        `json:"failure_count"`
	TotalLatency int64                        `json:"total_latency_ms"`
	Histogram    *LatencyHistogram            `json:"histogram"`
}

// ProviderStats contains per-provider statistics.
type ProviderStats struct {
	Requests  int64             `json:"requests"`
	Failures  int64             `json:"failures"`
	Histogram *LatencyHistogram `json:"histogram"`
}

// TypeStats contains per-request-type statistics.
type TypeStats struct {
	Requests int64 `json:"requests"`
	Failures int64 `json:"failures"`
}

// ProfileStats contains per-profile statistics.
type ProfileStats struct {
	Requests int64 `json:"requests"`
}

// MetricsResponse is the API response format for the metrics endpoint.
type MetricsResponse struct {
	Period     Period                    `json:"period"`
	Summary    Summary                   `json:"summary"`
	ByProvider map[string]ProviderSummary `json:"by_provider"`
	ByType     map[string]TypeSummary     `json:"by_type"`
	ByProfile  map[string]ProfileSummary  `json:"by_profile"`
	Daily      []DailySummary            `json:"daily"`
}

// Period defines the time range for metrics.
type Period struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Summary contains aggregate statistics.
type Summary struct {
	TotalRequests int64   `json:"total_requests"`
	TotalFailures int64   `json:"total_failures"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
}

// ProviderSummary contains per-provider summary for the response.
type ProviderSummary struct {
	Requests int64   `json:"requests"`
	Failures int64   `json:"failures"`
	P50Ms    float64 `json:"p50_ms"`
	P90Ms    float64 `json:"p90_ms"`
	P99Ms    float64 `json:"p99_ms"`
}

// TypeSummary contains per-request-type summary for the response.
type TypeSummary struct {
	Requests int64 `json:"requests"`
	Failures int64 `json:"failures"`
}

// ProfileSummary contains per-profile summary for the response.
type ProfileSummary struct {
	Requests int64 `json:"requests"`
}

// DailySummary contains per-day summary for the response.
type DailySummary struct {
	Date         string  `json:"date"`
	Requests     int64   `json:"requests"`
	Failures     int64   `json:"failures"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// Granularity defines the time granularity for aggregation.
type Granularity string

const (
	GranularityHourly Granularity = "hourly"
	GranularityDaily  Granularity = "daily"
)

// NewDailyMetrics creates a new DailyMetrics instance for the given date.
func NewDailyMetrics(date string) *DailyMetrics {
	return &DailyMetrics{
		Date:       date,
		Requests:   make([]RequestRecord, 0),
		ByProvider: make(map[string]*ProviderStats),
		ByType:     make(map[string]*TypeStats),
		ByProfile:  make(map[string]*ProfileStats),
		Histogram:  NewLatencyHistogram(),
	}
}
