<<<<<<< HEAD
// Package registry — Pareto frontier router for optimal model selection.
//
// Ported from thegent/routing/pareto_router.py.
// Implements: hard constraints filter -> Pareto frontier (non-dominated set) -> lexicographic selection.
=======
// Package registry provides model definitions and lookup helpers for various AI providers.
// pareto_router.go implements the Pareto frontier routing algorithm.
//
// Algorithm (ported from thegent/src/thegent/routing/pareto_router.py):
//  1. Seed candidates from the quality-proxy table (model ID → cost/quality/latency).
//  2. Filter models that violate any hard constraint (cost, latency, quality).
//  3. Build Pareto frontier: remove dominated models.
//  4. Select best from frontier by quality/cost ratio (highest ratio wins;
//     zero-cost models get +Inf ratio and are implicitly best).
>>>>>>> ci-compile-fix
package registry

import (
	"context"
	"fmt"
<<<<<<< HEAD
	"sort"
)

// qualityProxy maps model IDs to rough quality scores (0-1).
// Aligned with thegent QUALITY_PROXY.
=======
	"math"
	"strings"
)

// qualityProxy maps known model IDs to their quality scores in [0,1].
// Sourced from thegent pareto_router.py QUALITY_PROXY table.
>>>>>>> ci-compile-fix
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

<<<<<<< HEAD
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
=======
// costPer1kProxy maps model IDs to estimated cost per 1k tokens (USD).
// These are rough estimates used for Pareto ranking.
var costPer1kProxy = map[string]float64{
	"claude-opus-4.6":               0.015,
	"claude-opus-4.6-1m":            0.015,
	"claude-sonnet-4.6":             0.003,
	"claude-haiku-4.5":              0.00025,
	"gpt-5.3-codex-high":            0.020,
	"gpt-5.3-codex":                 0.010,
	"claude-4.5-opus-high-thinking": 0.025,
	"claude-4.5-opus-high":          0.015,
	"claude-4.5-sonnet-thinking":    0.005,
	"claude-4-sonnet":               0.003,
	"gpt-4o":                        0.005,
	"gpt-5.1-codex":                 0.008,
	"gemini-3-flash":                0.00015,
	"gemini-3.1-pro":                0.007,
	"gemini-2.5-flash":              0.0001,
	"gemini-2.0-flash":              0.0001,
	"glm-5":                         0.001,
	"minimax-m2.5":                  0.001,
	"deepseek-v3.2":                 0.0005,
	"composer-1.5":                  0.002,
	"composer-1":                    0.001,
	"roo-default":                   0.0,
	"kilo-default":                  0.0,
}

// latencyMsProxy maps model IDs to estimated p50 latency in milliseconds.
var latencyMsProxy = map[string]int{
	"claude-opus-4.6":               4000,
	"claude-opus-4.6-1m":            5000,
	"claude-sonnet-4.6":             2000,
	"claude-haiku-4.5":              800,
	"gpt-5.3-codex-high":            6000,
	"gpt-5.3-codex":                 3000,
	"claude-4.5-opus-high-thinking": 8000,
	"claude-4.5-opus-high":          5000,
	"claude-4.5-sonnet-thinking":    4000,
	"claude-4-sonnet":               2500,
	"gpt-4o":                        2000,
	"gpt-5.1-codex":                 3000,
	"gemini-3-flash":                600,
	"gemini-3.1-pro":                3000,
	"gemini-2.5-flash":              500,
	"gemini-2.0-flash":              400,
	"glm-5":                         1500,
	"minimax-m2.5":                  1200,
	"deepseek-v3.2":                 1000,
	"composer-1.5":                  2000,
	"composer-1":                    1500,
	"roo-default":                   1000,
	"kilo-default":                  1000,
}

// inferProviderFromModelID derives the provider name from a model ID.
func inferProvider(modelID string) string {
	lower := strings.ToLower(modelID)
	switch {
	case strings.HasPrefix(lower, "claude"):
		return "claude"
	case strings.HasPrefix(lower, "gpt") || strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3"):
		return "openai"
	case strings.HasPrefix(lower, "gemini"):
		return "gemini"
	case strings.HasPrefix(lower, "deepseek"):
		return "deepseek"
	case strings.HasPrefix(lower, "glm"):
		return "glm"
	case strings.HasPrefix(lower, "minimax"):
		return "minimax"
	case strings.HasPrefix(lower, "composer"):
		return "composer"
	case strings.HasPrefix(lower, "roo"):
		return "roo"
	case strings.HasPrefix(lower, "kilo"):
		return "kilo"
	default:
		return "unknown"
	}
}

// ParetoRouter selects the Pareto-optimal model for a given RoutingRequest.
>>>>>>> ci-compile-fix
type ParetoRouter struct{}

// NewParetoRouter returns a new ParetoRouter.
func NewParetoRouter() *ParetoRouter {
	return &ParetoRouter{}
}

<<<<<<< HEAD
// SelectModel returns the Pareto-optimal model for the given constraints.
func (p *ParetoRouter) SelectModel(_ context.Context, req *RoutingRequest) (*RoutingCandidate, error) {
	candidates := buildCandidates()

	feasible := filterByConstraints(candidates, req)
	if len(feasible) == 0 {
		return nil, fmt.Errorf("no models satisfy constraints: maxCost=%.4f maxLatency=%d minQuality=%.2f",
=======
// SelectModel applies hard constraints, builds the Pareto frontier, and returns
// the best candidate by quality/cost ratio.
func (p *ParetoRouter) SelectModel(_ context.Context, req *RoutingRequest) (*RoutingCandidate, error) {
	allCandidates := buildCandidates(req)

	feasible := filterByConstraints(allCandidates, req)
	if len(feasible) == 0 {
		return nil, fmt.Errorf("no models satisfy constraints (cost<=%.4f, latency<=%dms, quality>=%.2f)",
>>>>>>> ci-compile-fix
			req.MaxCostPerCall, req.MaxLatencyMs, req.MinQualityScore)
	}

	frontier := computeParetoFrontier(feasible)
<<<<<<< HEAD
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
=======
	return selectFromCandidates(frontier), nil
}

// buildCandidates constructs RoutingCandidates from the quality/cost proxy tables.
// Estimated cost is scaled from per-1k-tokens to per-call assuming ~1000 tokens avg.
func buildCandidates(_ *RoutingRequest) []*RoutingCandidate {
	candidates := make([]*RoutingCandidate, 0, len(qualityProxy))
	for modelID, quality := range qualityProxy {
		costPer1k := costPer1kProxy[modelID]
		// Estimate per-call cost at 1000 token average.
		estimatedCost := costPer1k * 1.0
		latencyMs, ok := latencyMsProxy[modelID]
		if !ok {
			latencyMs = 2000
		}
		candidates = append(candidates, &RoutingCandidate{
			ModelID:            modelID,
			Provider:           inferProvider(modelID),
			EstimatedCost:      estimatedCost,
			EstimatedLatencyMs: latencyMs,
			QualityScore:       quality,
>>>>>>> ci-compile-fix
		})
	}
	return candidates
}

<<<<<<< HEAD
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
=======
// filterByConstraints returns only candidates that satisfy all hard constraints.
func filterByConstraints(candidates []*RoutingCandidate, req *RoutingRequest) []*RoutingCandidate {
	out := make([]*RoutingCandidate, 0, len(candidates))
>>>>>>> ci-compile-fix
	for _, c := range candidates {
		if req.MaxCostPerCall > 0 && c.EstimatedCost > req.MaxCostPerCall {
			continue
		}
		if req.MaxLatencyMs > 0 && c.EstimatedLatencyMs > req.MaxLatencyMs {
			continue
		}
<<<<<<< HEAD
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
=======
		if c.QualityScore < req.MinQualityScore {
			continue
		}
		out = append(out, c)
	}
	return out
}

// computeParetoFrontier removes dominated candidates and returns the Pareto-optimal set.
// A candidate c is dominated if another candidate d has:
//   - EstimatedCost <= c.EstimatedCost AND
//   - EstimatedLatencyMs <= c.EstimatedLatencyMs AND
//   - QualityScore >= c.QualityScore AND
//   - at least one strictly better on one axis.
func computeParetoFrontier(candidates []*RoutingCandidate) []*RoutingCandidate {
	frontier := make([]*RoutingCandidate, 0, len(candidates))
>>>>>>> ci-compile-fix
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

<<<<<<< HEAD
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
=======
// selectFromCandidates returns the candidate with the highest quality/cost ratio.
// Zero-cost candidates are implicitly +Inf ratio (best).
// Falls back to highest quality score when frontier is empty.
func selectFromCandidates(frontier []*RoutingCandidate) *RoutingCandidate {
	if len(frontier) == 0 {
		return nil
	}
	best := frontier[0]
	bestRatio := ratio(best)
	for _, c := range frontier[1:] {
		r := ratio(c)
		if r > bestRatio {
			bestRatio = r
			best = c
		}
	}
	return best
>>>>>>> ci-compile-fix
}

func ratio(c *RoutingCandidate) float64 {
	if c.EstimatedCost == 0 {
<<<<<<< HEAD
		return 1e18 // effectively infinite
=======
		return math.Inf(1)
>>>>>>> ci-compile-fix
	}
	return c.QualityScore / c.EstimatedCost
}
