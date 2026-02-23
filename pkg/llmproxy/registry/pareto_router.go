// Package registry — Pareto frontier router for optimal model selection.
//
// Ported from thegent/routing/pareto_router.py.
// Implements: hard constraints filter -> Pareto frontier (non-dominated set) -> lexicographic selection.
package registry

import (
	"context"
	"fmt"
	"sort"
)

// qualityProxy maps model IDs to rough quality scores (0-1).
// Aligned with thegent QUALITY_PROXY.
var qualityProxy = map[string]float64{
	"claude-opus-4.6":               0.95,
	"claude-opus-4.6-1m":            0.96,
	"claude-sonnet-4.6":             0.88,
	"claude-haiku-4.5":              0.75,
	"gpt-5.3-codex-high":            0.92,
	"gpt-5.3-codex":                 0.82,
	"claude-4.5-opus-high-thinking": 0.94,
	"claude-4.5-opus-high":          0.92,
	"claude-4.5-sonnet-thinking":    0.85,
	"claude-4-sonnet":               0.80,
	"gpt-4o":                        0.85,
	"gpt-5.1-codex":                 0.80,
	"gemini-3-flash":                0.78,
	"gemini-3.1-pro":                0.90,
	"gemini-2.5-flash":              0.76,
	"gemini-2.0-flash":              0.72,
	"glm-5":                         0.78,
	"minimax-m2.5":                  0.75,
	"deepseek-v3.2":                 0.80,
	"composer-1.5":                  0.82,
	"composer-1":                    0.78,
	"roo-default":                   0.70,
	"kilo-default":                  0.70,
}

// costPer1kDefaults maps model IDs to estimated cost per 1k tokens (USD).
var costPer1kDefaults = map[string]float64{
	"claude-opus-4.6":    0.075,
	"claude-opus-4.6-1m": 0.075,
	"claude-sonnet-4.6":  0.015,
	"claude-haiku-4.5":   0.005,
	"gpt-5.3-codex-high": 0.050,
	"gpt-5.3-codex":      0.025,
	"gpt-4o":             0.025,
	"gpt-5.1-codex":      0.020,
	"gemini-3-flash":     0.003,
	"gemini-3.1-pro":     0.035,
	"gemini-2.5-flash":   0.003,
	"gemini-2.0-flash":   0.002,
	"glm-5":              0.008,
	"minimax-m2.5":       0.004,
	"deepseek-v3.2":      0.005,
	"roo-default":        0.003,
	"kilo-default":       0.003,
}

// latencyMsDefaults maps model IDs to estimated latency in ms.
var latencyMsDefaults = map[string]int{
	"claude-opus-4.6":    8000,
	"claude-opus-4.6-1m": 10000,
	"claude-sonnet-4.6":  3000,
	"claude-haiku-4.5":   1000,
	"gpt-5.3-codex-high": 5000,
	"gpt-5.3-codex":      3000,
	"gpt-4o":             2500,
	"gpt-5.1-codex":      2500,
	"gemini-3-flash":     800,
	"gemini-3.1-pro":     4000,
	"gemini-2.5-flash":   700,
	"gemini-2.0-flash":   600,
	"glm-5":              2000,
	"minimax-m2.5":       1500,
	"deepseek-v3.2":      2000,
	"roo-default":        1500,
	"kilo-default":       1500,
}

// ParetoRouter selects the Pareto-optimal model given hard constraints.
type ParetoRouter struct{}

// NewParetoRouter returns a new ParetoRouter.
func NewParetoRouter() *ParetoRouter {
	return &ParetoRouter{}
}

// SelectModel returns the Pareto-optimal model for the given constraints.
func (p *ParetoRouter) SelectModel(_ context.Context, req *RoutingRequest) (*RoutingCandidate, error) {
	candidates := buildCandidates()

	feasible := filterByConstraints(candidates, req)
	if len(feasible) == 0 {
		return nil, fmt.Errorf("no models satisfy constraints: maxCost=%.4f maxLatency=%d minQuality=%.2f",
			req.MaxCostPerCall, req.MaxLatencyMs, req.MinQualityScore)
	}

	frontier := computeParetoFrontier(feasible)
	if len(frontier) == 0 {
		return nil, fmt.Errorf("empty Pareto frontier (should not happen)")
	}

	return bestFromFrontier(frontier), nil
}

// SelectFromCandidates selects the Pareto-optimal candidate from a provided list.
func (p *ParetoRouter) SelectFromCandidates(candidates []*RoutingCandidate) (*RoutingCandidate, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("candidates must be non-empty")
	}

	frontier := computeParetoFrontier(candidates)
	if len(frontier) == 0 {
		return nil, fmt.Errorf("empty Pareto frontier")
	}

	return bestFromFrontier(frontier), nil
}

func buildCandidates() []*RoutingCandidate {
	var candidates []*RoutingCandidate
	for modelID, quality := range qualityProxy {
		cost, hasCost := costPer1kDefaults[modelID]
		if !hasCost {
			cost = 0.01 // default
		}
		latency, hasLatency := latencyMsDefaults[modelID]
		if !hasLatency {
			latency = 3000 // default
		}

		// Estimate cost per call assuming ~1k tokens
		estimatedCost := cost

		candidates = append(candidates, &RoutingCandidate{
			ModelID:            modelID,
			EstimatedCost:      estimatedCost,
			EstimatedLatencyMs: latency,
			QualityScore:       quality,
			Provider:           inferProvider(modelID),
			CostPer1k:          cost,
		})
	}
	return candidates
}

func inferProvider(modelID string) string {
	switch {
	case len(modelID) >= 6 && modelID[:6] == "claude":
		return "claude"
	case len(modelID) >= 3 && modelID[:3] == "gpt":
		return "openai"
	case len(modelID) >= 6 && modelID[:6] == "gemini":
		return "gemini"
	case len(modelID) >= 3 && modelID[:3] == "glm":
		return "glm"
	case len(modelID) >= 7 && modelID[:7] == "minimax":
		return "minimax"
	case len(modelID) >= 8 && modelID[:8] == "deepseek":
		return "deepseek"
	case len(modelID) >= 8 && modelID[:8] == "composer":
		return "composer"
	case len(modelID) >= 3 && modelID[:3] == "roo":
		return "roo"
	case len(modelID) >= 4 && modelID[:4] == "kilo":
		return "kilo"
	default:
		return "unknown"
	}
}

func filterByConstraints(candidates []*RoutingCandidate, req *RoutingRequest) []*RoutingCandidate {
	var result []*RoutingCandidate
	for _, c := range candidates {
		if req.MaxCostPerCall > 0 && c.EstimatedCost > req.MaxCostPerCall {
			continue
		}
		if req.MaxLatencyMs > 0 && c.EstimatedLatencyMs > req.MaxLatencyMs {
			continue
		}
		if req.MinQualityScore > 0 && c.QualityScore < req.MinQualityScore {
			continue
		}
		result = append(result, c)
	}
	return result
}

// isDominated returns true if b dominates a (b is at least as good on all axes and strictly better on one).
func isDominated(a, b *RoutingCandidate) bool {
	costOK := b.EstimatedCost <= a.EstimatedCost
	qualityOK := b.QualityScore >= a.QualityScore
	strictlyBetter := b.EstimatedCost < a.EstimatedCost || b.QualityScore > a.QualityScore
	return costOK && qualityOK && strictlyBetter
}

func computeParetoFrontier(candidates []*RoutingCandidate) []*RoutingCandidate {
	var frontier []*RoutingCandidate
	for _, c := range candidates {
		dominated := false
		for _, other := range candidates {
			if other == c {
				continue
			}
			if isDominated(c, other) {
				dominated = true
				break
			}
		}
		if !dominated {
			frontier = append(frontier, c)
		}
	}
	return frontier
}

// bestFromFrontier selects candidate with highest quality/cost ratio.
// Falls back to highest quality when cost is zero.
func bestFromFrontier(frontier []*RoutingCandidate) *RoutingCandidate {
	allZero := true
	for _, c := range frontier {
		if c.EstimatedCost > 0 {
			allZero = false
			break
		}
	}

	if allZero {
		sort.Slice(frontier, func(i, j int) bool {
			return frontier[i].QualityScore > frontier[j].QualityScore
		})
		return frontier[0]
	}

	// quality/cost ratio; zero-cost = infinite ratio (best)
	sort.Slice(frontier, func(i, j int) bool {
		ri := ratio(frontier[i])
		rj := ratio(frontier[j])
		return ri > rj
	})
	return frontier[0]
}

func ratio(c *RoutingCandidate) float64 {
	if c.EstimatedCost == 0 {
		return 1e18 // effectively infinite
	}
	return c.QualityScore / c.EstimatedCost
}
