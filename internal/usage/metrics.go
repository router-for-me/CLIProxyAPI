// Package usage provides provider-level metrics for OpenRouter-style routing.
package usage

import (
	"strings"
)

// ProviderMetrics holds per-provider metrics for routing decisions.
type ProviderMetrics struct {
	RequestCount  int64   `json:"request_count"`
	SuccessCount  int64   `json:"success_count"`
	FailureCount  int64   `json:"failure_count"`
	TotalTokens   int64   `json:"total_tokens"`
	SuccessRate   float64 `json:"success_rate"`
	CostPer1kIn   float64 `json:"cost_per_1k_input,omitempty"`
	CostPer1kOut  float64 `json:"cost_per_1k_output,omitempty"`
	LatencyP50Ms  int     `json:"latency_p50_ms,omitempty"`
	LatencyP95Ms  int     `json:"latency_p95_ms,omitempty"`
}

// Known providers for routing (thegent model→provider mapping).
var knownProviders = map[string]struct{}{
	"nim": {}, "kilo": {}, "minimax": {}, "glm": {}, "openrouter": {},
	"antigravity": {}, "claude": {}, "codex": {}, "gemini": {}, "roo": {},
}

// Fallback cost per 1k tokens (USD) when no usage data. Align with thegent _GLM_OFFER_COST.
var fallbackCostPer1k = map[string]float64{
	"nim": 0.22, "kilo": 0.28, "minimax": 0.36, "glm": 0.80, "openrouter": 0.30,
}

// GetProviderMetrics returns per-provider metrics from the usage snapshot.
// Used by thegent for OpenRouter-style routing (cheapest, fastest, cost_quality).
func GetProviderMetrics() map[string]ProviderMetrics {
	snap := GetRequestStatistics().Snapshot()
	result := make(map[string]ProviderMetrics)
	for apiKey, apiSnap := range snap.APIs {
		provider := strings.ToLower(strings.TrimSpace(apiKey))
		if _, ok := knownProviders[provider]; !ok {
			continue
		}
		failures := int64(0)
		for _, m := range apiSnap.Models {
			for _, d := range m.Details {
				if d.Failed {
					failures++
				}
			}
		}
		success := apiSnap.TotalRequests - failures
		if success < 0 {
			success = 0
		}
		sr := 1.0
		if apiSnap.TotalRequests > 0 {
			sr = float64(success) / float64(apiSnap.TotalRequests)
		}
		cost := fallbackCostPer1k[provider]
		if cost == 0 {
			cost = 0.5
		}
		result[provider] = ProviderMetrics{
			RequestCount:  apiSnap.TotalRequests,
			SuccessCount:  success,
			FailureCount:  failures,
			TotalTokens:   apiSnap.TotalTokens,
			SuccessRate:   sr,
			CostPer1kIn:   cost / 2,
			CostPer1kOut:  cost,
		}
	}
	return result
}
