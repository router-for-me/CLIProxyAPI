package metrics

import (
	"encoding/json"
	"net/http"
	"time"
)

// MetricsServer is responsible for exposing HTTP endpoints related to metrics.
// It uses the MetricsStore interface to retrieve data, making it independent
// of the underlying storage implementation (e.g., SQLite, in-memory, etc.).
type MetricsServer struct {
	store MetricsStore
}

// NewMetricsServer creates a new MetricsServer instance with the provided store.
// The store is typically injected during application startup (e.g., in main.go).
func NewMetricsServer(store MetricsStore) *MetricsServer {
	return &MetricsServer{store: store}
}

// HandleHealth responds to health-check requests.
// It returns a simple JSON payload indicating the service is running.
// Useful for load balancers, Kubernetes liveness/readiness probes, etc.
func (ms *MetricsServer) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Encode a minimal health response: {"ok": true}
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// MetricsResponse defines the structure of the JSON response returned by the /metrics endpoint.
// It bundles overall totals, per-model statistics, and hourly time-series data
// for convenient consumption by the frontend dashboard.
type MetricsResponse struct {
	Totals     *Totals            `json:"totals"`     // Aggregated totals (requests, tokens, etc.)
	ByModel    []ModelStats       `json:"by_model"`   // Breakdown of usage and latency per model
	TimeSeries []TimeSeriesBucket `json:"timeseries"` // Hourly buckets for the requested time range
}

// HandleMetrics serves the main metrics API endpoint (e.g., /_qs/metrics).
// It supports optional query parameters to filter results:
//   - from: RFC3339 timestamp (inclusive start of time range)
//   - to:   RFC3339 timestamp (exclusive end of time range)
//   - model: filter results to a specific model name
//
// The endpoint returns a comprehensive JSON payload used by the web UI dashboard.
func (ms *MetricsServer) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Start with an empty query (no filters)
	query := MetricsQuery{}

	// Optional: Parse 'from' parameter for time-range filtering
	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			query.From = &t
		}
		// Note: Invalid values are silently ignored to keep the API forgiving
	}

	// Optional: Parse 'to' parameter for time-range filtering
	if toStr := r.URL.Query().Get("to"); toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			query.To = &t
		}
	}

	// Optional: Filter by a specific model
	if model := r.URL.Query().Get("model"); model != "" {
		query.Model = &model
	}

	// Retrieve aggregated totals based on the query
	totals, err := ms.store.GetTotals(ctx, query)
	if err != nil {
		http.Error(w, "failed to retrieve totals: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Retrieve per-model statistics
	byModel, err := ms.store.GetByModel(ctx, query)
	if err != nil {
		http.Error(w, "failed to retrieve model stats: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Retrieve hourly time-series data (fixed 1-hour buckets)
	// The bucket size is currently hardcoded to 1 hour as it matches the dashboard needs.
	timeSeries, err := ms.store.GetTimeSeries(ctx, query, 1)
	if err != nil {
		http.Error(w, "failed to retrieve time series: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Assemble the full response object
	response := MetricsResponse{
		Totals:     totals,
		ByModel:    byModel,
		TimeSeries: timeSeries,
	}

	// Return JSON response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		// If encoding fails, the connection may already be closed, but log if possible
		// (In production, proper logging would be added here)
	}
}