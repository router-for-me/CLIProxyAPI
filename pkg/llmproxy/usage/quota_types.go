// Package usage — quota enforcement types.
package usage

// QuotaLimit defines daily usage limits.
type QuotaLimit struct {
	MaxTokensPerDay float64 `json:"max_tokens_per_day"`
	MaxCostPerDay   float64 `json:"max_cost_per_day"`
}

// UsageRecord tracks accumulated usage.
type UsageRecord struct {
	TokensUsed float64 `json:"tokens_used"`
	CostUsed   float64 `json:"cost_used"`
}
