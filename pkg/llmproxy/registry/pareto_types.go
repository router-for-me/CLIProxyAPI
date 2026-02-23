// Package registry — Pareto routing types for optimal model selection.
package registry

// RoutingRequest describes constraints for Pareto-optimal model selection.
type RoutingRequest struct {
	TaskComplexity  string            `json:"taskComplexity"`
	MaxCostPerCall  float64           `json:"maxCostPerCall"`
	MaxLatencyMs    int               `json:"maxLatencyMs"`
	MinQualityScore float64           `json:"minQualityScore"`
	TaskMetadata    map[string]string `json:"taskMetadata,omitempty"`
}

// RoutingCandidate is a model that passed constraint filtering and may be on the Pareto frontier.
type RoutingCandidate struct {
	ModelID            string  `json:"model_id"`
	EstimatedCost      float64 `json:"estimated_cost"`
	EstimatedLatencyMs int     `json:"estimated_latency_ms"`
	QualityScore       float64 `json:"quality_score"`
	Provider           string  `json:"provider"`
	CostPer1k          float64 `json:"cost_per_1k"`
}
