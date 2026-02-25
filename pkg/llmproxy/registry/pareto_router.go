// Package registry provides model definitions and lookup helpers for various AI providers.
// pareto_router.go implements the Pareto frontier routing algorithm.
//
// Algorithm (ported from thegent/src/thegent/routing/pareto_router.py):
//  1. Seed candidates from the quality-proxy table (model ID → cost/quality/latency).
//  2. Filter models that violate any hard constraint (cost, latency, quality).
//  3. Build Pareto frontier: remove dominated models.
//  4. Select best from frontier by quality/cost ratio (highest ratio wins;
//     zero-cost models get +Inf ratio and are implicitly best).
package registry

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/benchmarks"
)

// qualityProxy maps known model IDs to their quality scores in [0,1].
// Sourced from thegent pareto_router.py QUALITY_PROXY table.
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
type ParetoRouter struct {
	// benchmarkStore provides dynamic benchmark data with fallback
	benchmarkStore *benchmarks.UnifiedBenchmarkStore
}

// NewParetoRouter returns a new ParetoRouter with benchmarks integration.
func NewParetoRouter() *ParetoRouter {
	return &ParetoRouter{
		benchmarkStore: benchmarks.NewFallbackOnlyStore(),
	}
}

// NewParetoRouterWithBenchmarks returns a ParetoRouter with dynamic benchmarks.
// Pass nil for primary to use fallback-only mode.
func NewParetoRouterWithBenchmarks(primary benchmarks.BenchmarkProvider) *ParetoRouter {
	var store *benchmarks.UnifiedBenchmarkStore
	if primary != nil {
		store = benchmarks.NewUnifiedStore(primary)
	} else {
		store = benchmarks.NewFallbackOnlyStore()
	}
	return &ParetoRouter{
		benchmarkStore: store,
	}
}

// SelectModel applies hard constraints, builds the Pareto frontier, and returns
// the best candidate by quality/cost ratio.
func (p *ParetoRouter) SelectModel(_ context.Context, req *RoutingRequest) (*RoutingCandidate, error) {
	allCandidates := p.buildCandidates(req)

	feasible := filterByConstraints(allCandidates, req)
	if len(feasible) == 0 {
		return nil, fmt.Errorf("no models satisfy constraints (cost<=%.4f, latency<=%dms, quality>=%.2f)",
			req.MaxCostPerCall, req.MaxLatencyMs, req.MinQualityScore)
	}

	frontier := computeParetoFrontier(feasible)
	return selectFromCandidates(frontier), nil
}

// buildCandidates constructs RoutingCandidates from benchmark store.
// Falls back to hardcoded maps if benchmark store unavailable.
func (p *ParetoRouter) buildCandidates(req *RoutingRequest) []*RoutingCandidate {
	candidates := make([]*RoutingCandidate, 0, len(qualityProxy))
	
	for modelID, quality := range qualityProxy {
		// Try dynamic benchmarks first, fallback to hardcoded
		var costPer1k float64
		var latencyMs int
		var ok bool
		
		if p.benchmarkStore != nil {
			if c, found := p.benchmarkStore.GetCost(modelID); found {
				costPer1k = c
			} else {
				costPer1k = costPer1kProxy[modelID]
			}
			if l, found := p.benchmarkStore.GetLatency(modelID); found {
				latencyMs = l
			} else {
				latencyMs, ok = latencyMsProxy[modelID]
				if !ok {
					latencyMs = 2000
				}
			}
		} else {
			// Fallback to hardcoded maps
			costPer1k = costPer1kProxy[modelID]
			var ok bool
			latencyMs, ok = latencyMsProxy[modelID]
			if !ok {
				latencyMs = 2000
			}
		}
		
		estimatedCost := costPer1k * 1.0 // Scale to per-call
		
		candidates = append(candidates, &RoutingCandidate{
			ModelID:            modelID,
			Provider:           inferProvider(modelID),
			EstimatedCost:      estimatedCost,
			EstimatedLatencyMs: latencyMs,
			QualityScore:       quality,
		})
	}
	return candidates
}

// filterByConstraints returns only candidates that satisfy all hard constraints.
func filterByConstraints(candidates []*RoutingCandidate, req *RoutingRequest) []*RoutingCandidate {
	out := make([]*RoutingCandidate, 0, len(candidates))
	for _, c := range candidates {
		if req.MaxCostPerCall > 0 && c.EstimatedCost > req.MaxCostPerCall {
			continue
		}
		if req.MaxLatencyMs > 0 && c.EstimatedLatencyMs > req.MaxLatencyMs {
			continue
		}
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
}

func ratio(c *RoutingCandidate) float64 {
	if c.EstimatedCost == 0 {
		return math.Inf(1)
	}
	return c.QualityScore / c.EstimatedCost
}
