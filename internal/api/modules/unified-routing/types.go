// Package unifiedrouting provides a unified routing system that allows
// defining custom model aliases with multi-layer failover pipelines.
package unifiedrouting

import (
	"time"
)

// ================== Configuration Types ==================

// Settings holds the global settings for unified routing.
type Settings struct {
	Enabled            bool `json:"enabled" yaml:"enabled"`
	HideOriginalModels bool `json:"hide_original_models" yaml:"hide-original-models"`
}

// HealthCheckConfig holds the health check configuration.
type HealthCheckConfig struct {
	DefaultCooldownSeconds   int `json:"default_cooldown_seconds" yaml:"default-cooldown-seconds"`
	CheckIntervalSeconds     int `json:"check_interval_seconds" yaml:"check-interval-seconds"`
	CheckTimeoutSeconds      int `json:"check_timeout_seconds" yaml:"check-timeout-seconds"`
	MaxConsecutiveFailures   int `json:"max_consecutive_failures" yaml:"max-consecutive-failures"`
}

// DefaultHealthCheckConfig returns the default health check configuration.
func DefaultHealthCheckConfig() HealthCheckConfig {
	return HealthCheckConfig{
		DefaultCooldownSeconds:   60,
		CheckIntervalSeconds:     30,
		CheckTimeoutSeconds:      10,
		MaxConsecutiveFailures:   3,
	}
}

// Route represents a routing configuration (persistent entity).
type Route struct {
	ID          string    `json:"id" yaml:"id"`
	Name        string    `json:"name" yaml:"name"`
	Description string    `json:"description,omitempty" yaml:"description,omitempty"`
	Enabled     bool      `json:"enabled" yaml:"enabled"`
	CreatedAt   time.Time `json:"created_at" yaml:"-"`
	UpdatedAt   time.Time `json:"updated_at" yaml:"-"`
}

// Pipeline represents the routing pipeline configuration (value object).
type Pipeline struct {
	RouteID string  `json:"route_id" yaml:"-"`
	Layers  []Layer `json:"layers" yaml:"layers"`
}

// Layer represents a layer in the pipeline (value object).
type Layer struct {
	Level           int          `json:"level" yaml:"level"`
	Strategy        LoadStrategy `json:"strategy" yaml:"strategy"`
	CooldownSeconds int          `json:"cooldown_seconds" yaml:"cooldown-seconds"`
	Targets         []Target     `json:"targets" yaml:"targets"`
}

// Target represents a target in a layer (value object).
type Target struct {
	ID           string `json:"id" yaml:"id"`
	CredentialID string `json:"credential_id" yaml:"credential-id"`
	Model        string `json:"model" yaml:"model"`
	Weight       int    `json:"weight,omitempty" yaml:"weight,omitempty"`
	Enabled      bool   `json:"enabled" yaml:"enabled"`
}

// LoadStrategy defines the load balancing strategy.
type LoadStrategy string

const (
	StrategyRoundRobin     LoadStrategy = "round-robin"
	StrategyWeightedRound  LoadStrategy = "weighted-round-robin"
	StrategyLeastConn      LoadStrategy = "least-connections"
	StrategyRandom         LoadStrategy = "random"
	StrategyFirstAvailable LoadStrategy = "first-available"
)

// ================== Runtime State Types ==================

// TargetState represents the runtime state of a target (in-memory entity).
type TargetState struct {
	TargetID            string        `json:"target_id"`
	Status              TargetStatus  `json:"status"`
	ConsecutiveFailures int           `json:"consecutive_failures"`
	CooldownEndsAt      *time.Time    `json:"cooldown_ends_at,omitempty"`
	LastSuccessAt       *time.Time    `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time    `json:"last_failure_at,omitempty"`
	LastFailureReason   string        `json:"last_failure_reason,omitempty"`
	ActiveConnections   int64         `json:"active_connections"`
	TotalRequests       int64         `json:"total_requests"`
	SuccessfulRequests  int64         `json:"successful_requests"`
}

// TargetStatus defines the status of a target.
// Simplified to only two states:
// - healthy: target is available (default state)
// - cooling: target is in cooldown after failure
type TargetStatus string

const (
	StatusHealthy TargetStatus = "healthy"
	StatusCooling TargetStatus = "cooling"
)

// RouteState represents the runtime state of a route.
type RouteState struct {
	RouteID      string        `json:"route_id"`
	RouteName    string        `json:"route_name"`
	Status       string        `json:"status"` // "healthy", "degraded", "unhealthy"
	ActiveLayer  int           `json:"active_layer"`
	LayerStates  []LayerState  `json:"layers"`
}

// LayerState represents the runtime state of a layer.
type LayerState struct {
	Level        int            `json:"level"`
	Status       string         `json:"status"` // "active", "standby", "exhausted"
	TargetStates []*TargetState `json:"targets"`
}

// StateOverview represents the overall state overview.
type StateOverview struct {
	UnifiedRoutingEnabled bool         `json:"unified_routing_enabled"`
	HideOriginalModels    bool         `json:"hide_original_models"`
	TotalRoutes           int          `json:"total_routes"`
	HealthyRoutes         int          `json:"healthy_routes"`
	DegradedRoutes        int          `json:"degraded_routes"`
	UnhealthyRoutes       int          `json:"unhealthy_routes"`
	Routes                []RouteState `json:"routes,omitempty"`
}

// ================== Monitoring Types ==================

// RequestTrace represents a request trace record.
type RequestTrace struct {
	TraceID        string         `json:"trace_id"`
	RouteID        string         `json:"route_id"`
	RouteName      string         `json:"route_name"`
	Timestamp      time.Time      `json:"timestamp"`
	Status         TraceStatus    `json:"status"`
	TotalLatencyMs int64          `json:"total_latency_ms"`
	Attempts       []AttemptTrace `json:"attempts"`
}

// TraceStatus defines the status of a trace.
type TraceStatus string

const (
	TraceStatusSuccess  TraceStatus = "success"
	TraceStatusRetry    TraceStatus = "retry"
	TraceStatusFallback TraceStatus = "fallback"
	TraceStatusFailed   TraceStatus = "failed"
)

// AttemptTrace represents a single attempt within a trace.
type AttemptTrace struct {
	Attempt      int           `json:"attempt"`
	Layer        int           `json:"layer"`
	TargetID     string        `json:"target_id"`
	CredentialID string        `json:"credential_id"`
	Model        string        `json:"model"`
	Status       AttemptStatus `json:"status"`
	LatencyMs    int64         `json:"latency_ms,omitempty"`
	Error        string        `json:"error,omitempty"`
}

// AttemptStatus defines the status of an attempt.
type AttemptStatus string

const (
	AttemptStatusSuccess AttemptStatus = "success"
	AttemptStatusFailed  AttemptStatus = "failed"
	AttemptStatusSkipped AttemptStatus = "skipped"
)

// RoutingEvent represents a routing event.
type RoutingEvent struct {
	ID        string            `json:"id"`
	Type      RoutingEventType  `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	RouteID   string            `json:"route_id"`
	TargetID  string            `json:"target_id,omitempty"`
	Details   map[string]any    `json:"details,omitempty"`
}

// RoutingEventType defines the type of routing event.
type RoutingEventType string

const (
	EventTargetFailed    RoutingEventType = "target_failed"
	EventTargetRecovered RoutingEventType = "target_recovered"
	EventLayerFallback   RoutingEventType = "layer_fallback"
	EventCooldownStarted RoutingEventType = "cooldown_started"
	EventCooldownEnded   RoutingEventType = "cooldown_ended"
)

// ================== Statistics Types ==================

// AggregatedStats represents aggregated statistics.
type AggregatedStats struct {
	Period               string                 `json:"period"`
	TotalRequests        int64                  `json:"total_requests"`
	SuccessfulRequests   int64                  `json:"successful_requests"`
	FailedRequests       int64                  `json:"failed_requests"`
	SuccessRate          float64                `json:"success_rate"`
	AvgLatencyMs         int64                  `json:"avg_latency_ms"`
	P95LatencyMs         int64                  `json:"p95_latency_ms"`
	P99LatencyMs         int64                  `json:"p99_latency_ms"`
	LayerDistribution    []LayerDistribution    `json:"layer_distribution,omitempty"`
	TargetDistribution   []TargetDistribution   `json:"target_distribution,omitempty"`
	AttemptsDistribution []AttemptsDistribution `json:"attempts_distribution,omitempty"`
}

// AttemptsDistribution represents the distribution of how many attempts
// were needed for successful requests.
type AttemptsDistribution struct {
	Attempts   int     `json:"attempts"`   // Number of attempts (1, 2, 3, ...)
	Count      int64   `json:"count"`      // Number of requests that succeeded with this many attempts
	Percentage float64 `json:"percentage"` // Percentage of successful requests
}

// LayerDistribution represents the distribution of requests across layers.
type LayerDistribution struct {
	Level      int     `json:"level"`
	Requests   int64   `json:"requests"`
	Percentage float64 `json:"percentage"`
}

// TargetDistribution represents the distribution of requests across targets.
type TargetDistribution struct {
	TargetID     string  `json:"target_id"`
	CredentialID string  `json:"credential_id"`
	Requests     int64   `json:"requests"`
	SuccessRate  float64 `json:"success_rate"`
	AvgLatencyMs int64   `json:"avg_latency_ms"`
}

// ================== Credential Types ==================

// CredentialInfo represents information about a credential.
type CredentialInfo struct {
	ID       string      `json:"id"`
	Provider string      `json:"provider"`
	Type     string      `json:"type"` // "oauth", "api-key"
	Label    string      `json:"label,omitempty"`
	Prefix   string      `json:"prefix,omitempty"`
	BaseURL  string      `json:"base_url,omitempty"`
	APIKey   string      `json:"api_key,omitempty"` // masked
	Status   string      `json:"status"`
	Models   []ModelInfo `json:"models"`
}

// ModelInfo represents information about a model.
type ModelInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

// ================== Health Check Types ==================

// HealthResult represents the result of a health check.
type HealthResult struct {
	TargetID     string    `json:"target_id"`
	CredentialID string    `json:"credential_id"`
	Model        string    `json:"model"`
	Status       string    `json:"status"` // "healthy", "unhealthy"
	LatencyMs    int64     `json:"latency_ms,omitempty"`
	Message      string    `json:"message,omitempty"`
	CheckedAt    time.Time `json:"checked_at"`
}

// ================== Filter Types ==================

// StatsFilter defines the filter for statistics queries.
type StatsFilter struct {
	Period      string    `json:"period"`      // "1h", "24h", "7d", "30d"
	Granularity string    `json:"granularity"` // "minute", "hour", "day"
	StartTime   time.Time `json:"start_time,omitempty"`
	EndTime     time.Time `json:"end_time,omitempty"`
}

// EventFilter defines the filter for event queries.
type EventFilter struct {
	Type    string `json:"type,omitempty"` // "failure", "recovery", "fallback", "all"
	RouteID string `json:"route_id,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Offset  int    `json:"offset,omitempty"`
}

// TraceFilter defines the filter for trace queries.
type TraceFilter struct {
	RouteID string `json:"route_id,omitempty"`
	Status  string `json:"status,omitempty"` // "success", "retry", "fallback", "failed"
	Limit   int    `json:"limit,omitempty"`
	Offset  int    `json:"offset,omitempty"`
}

// HealthHistoryFilter defines the filter for health history queries.
type HealthHistoryFilter struct {
	TargetID string    `json:"target_id,omitempty"`
	Status   string    `json:"status,omitempty"`
	Limit    int       `json:"limit,omitempty"`
	Since    time.Time `json:"since,omitempty"`
}

// ================== Export/Import Types ==================

// ExportData represents the data for export/import.
type ExportData struct {
	Version    string            `json:"version"`
	ExportedAt time.Time         `json:"exported_at"`
	Config     ExportedConfig    `json:"config"`
}

// ExportedConfig represents the exported configuration.
type ExportedConfig struct {
	Settings    Settings          `json:"settings"`
	HealthCheck HealthCheckConfig `json:"health_check"`
	Routes      []RouteWithPipeline `json:"routes"`
}

// RouteWithPipeline combines route and its pipeline for export.
type RouteWithPipeline struct {
	Route    Route    `json:"route"`
	Pipeline Pipeline `json:"pipeline"`
}

// ================== Validation Types ==================

// ValidationError represents a validation error.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}
