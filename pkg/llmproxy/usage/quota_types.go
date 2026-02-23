// Package usage provides provider-level metrics for OpenRouter-style routing.
// quota_types.go defines types for quota enforcement.
package usage

// QuotaLimit specifies daily usage caps.
type QuotaLimit struct {
	// MaxTokensPerDay is the daily token limit. 0 means uncapped.
	MaxTokensPerDay float64
	// MaxCostPerDay is the daily cost cap in USD. 0 means uncapped.
	MaxCostPerDay float64
}

// Usage records observed resource consumption.
type Usage struct {
	TokensUsed float64
	CostUsed   float64
}

// QuotaCheckRequest carries an estimated token/cost projection for a pending request.
type QuotaCheckRequest struct {
	EstimatedTokens float64
	EstimatedCost   float64
}
