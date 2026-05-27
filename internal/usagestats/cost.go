package usagestats

import (
	"fmt"
	"strings"
)

// PriceMatcher looks up manual pricing for a given provider+model pair.
type PriceMatcher struct {
	// prices maps "provider|model" (both lowercased, trimmed) to ModelPrice.
	prices map[string]ModelPrice
}

// NewPriceMatcher builds a price lookup table from the given manual prices.
// Provider and model are normalized to lowercase for matching.
func NewPriceMatcher(prices []ModelPrice) *PriceMatcher {
	m := make(map[string]ModelPrice, len(prices))
	for _, p := range prices {
		key := priceKey(p.Provider, p.Model)
		m[key] = p
	}
	return &PriceMatcher{prices: m}
}

// Match returns the price entry for the given provider+model pair.
// Returns (price, true) if found, or (ModelPrice{}, false) if no match.
// Matching is case-insensitive and trims whitespace.
func (pm *PriceMatcher) Match(provider, model string) (ModelPrice, bool) {
	if pm == nil {
		return ModelPrice{}, false
	}
	key := priceKey(provider, model)
	p, ok := pm.prices[key]
	return p, ok
}

// CalculateCost computes cost micros for the given token counts using the
// provided price. Returns (inputCostMicros, outputCostMicros, totalCostMicros).
// One micro-dollar = 1e-6 USD.
// The calculation uses: cost_micros = round(tokens * cost_per_token * 1e6).
func CalculateCost(price ModelPrice, inputTokens, outputTokens int64) (inputCostMicros, outputCostMicros, totalCostMicros int64) {
	inputCostMicros = toMicros(price.InputCostPerToken, inputTokens)
	outputCostMicros = toMicros(price.OutputCostPerToken, outputTokens)
	totalCostMicros = inputCostMicros + outputCostMicros
	return
}

// MicrosToUSD converts integer micro-dollars to a float USD value.
func MicrosToUSD(micros int64) float64 {
	return float64(micros) / 1e6
}

func priceKey(provider, model string) string {
	return strings.TrimSpace(strings.ToLower(provider)) + "|" + strings.TrimSpace(strings.ToLower(model))
}

func toMicros(costPerToken float64, tokens int64) int64 {
	// cost_per_token * tokens * 1e6, rounded to nearest integer
	return int64(float64(tokens)*costPerToken*1e6 + 0.5)
}

// String returns a human-readable description of the price matcher entries.
func (pm *PriceMatcher) String() string {
	if pm == nil || len(pm.prices) == 0 {
		return "PriceMatcher(empty)"
	}
	return fmt.Sprintf("PriceMatcher(%d entries)", len(pm.prices))
}
