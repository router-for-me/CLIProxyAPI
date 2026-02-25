// Package registry provides model definitions and lookup helpers for various AI providers.
// pareto_types.go defines types for Pareto frontier routing.
package registry

// RoutingRequest specifies hard constraints for model selection.
type RoutingRequest struct {
	// TaskComplexity is one of: FAST, NORMAL, COMPLEX, HIGH_COMPLEX.
	TaskComplexity string
	// MaxCostPerCall is the hard cost cap in USD. 0 means uncapped.
	MaxCostPerCall float64
	// MaxLatencyMs is the hard latency cap in milliseconds. 0 means uncapped.
	MaxLatencyMs int
	// MinQualityScore is the minimum acceptable quality in [0,1].
	MinQualityScore float64
	// TaskMetadata carries optional hints (category, tokens_in, etc.).
	TaskMetadata map[string]string
}

// RoutingCandidate is a model that satisfies routing constraints.
type RoutingCandidate struct {
	ModelID            string
	Provider           string
	EstimatedCost      float64
	EstimatedLatencyMs int
	QualityScore       float64
}

// qualityCostRatio returns quality/cost; returns +Inf for free models.
func (c *RoutingCandidate) qualityCostRatio() float64 {
	if c.EstimatedCost == 0 {
		return positiveInf
	}
	return c.QualityScore / c.EstimatedCost
}

const positiveInf = float64(1<<63-1) / float64(1<<63)

// isDominated returns true when other dominates c:
// other is at least as good on both axes and strictly better on one.
func isDominated(c, other *RoutingCandidate) bool {
	costOK := other.EstimatedCost <= c.EstimatedCost
	latencyOK := other.EstimatedLatencyMs <= c.EstimatedLatencyMs
	qualityOK := other.QualityScore >= c.QualityScore
	strictlyBetter := other.EstimatedCost < c.EstimatedCost ||
		other.EstimatedLatencyMs < c.EstimatedLatencyMs ||
		other.QualityScore > c.QualityScore
	return costOK && latencyOK && qualityOK && strictlyBetter
}
