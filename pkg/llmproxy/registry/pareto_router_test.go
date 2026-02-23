package registry

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParetoRoutingSelectsOptimalModelGivenConstraints verifies the primary integration
// path: given hard constraints, SelectModel returns a candidate on the Pareto frontier
// that satisfies every constraint.
// @trace FR-ROUTING-001
func TestParetoRoutingSelectsOptimalModelGivenConstraints(t *testing.T) {
	paretoRouter := NewParetoRouter()

	req := &RoutingRequest{
		TaskComplexity:  "NORMAL",
		MaxCostPerCall:  0.01,
		MaxLatencyMs:    5000,
		MinQualityScore: 0.75,
		TaskMetadata: map[string]string{
			"category":  "code_analysis",
			"tokens_in": "2500",
		},
	}

	selected, err := paretoRouter.SelectModel(context.Background(), req)

	assert.NoError(t, err)
	require.NotNil(t, selected)
	assert.LessOrEqual(t, selected.EstimatedCost, req.MaxCostPerCall)
	assert.LessOrEqual(t, selected.EstimatedLatencyMs, req.MaxLatencyMs)
	assert.GreaterOrEqual(t, selected.QualityScore, req.MinQualityScore)
	assert.NotEmpty(t, selected.ModelID)
	assert.NotEmpty(t, selected.Provider)
}

// TestParetoRoutingRejectsImpossibleConstraints verifies that an error is returned when
// no model can satisfy the combined constraints.
// @trace FR-ROUTING-002
func TestParetoRoutingRejectsImpossibleConstraints(t *testing.T) {
	paretoRouter := NewParetoRouter()

	req := &RoutingRequest{
		MaxCostPerCall:  0.000001, // Impossibly cheap
		MaxLatencyMs:    1,        // Impossibly fast
		MinQualityScore: 0.99,     // Impossibly high
	}

	selected, err := paretoRouter.SelectModel(context.Background(), req)

	assert.Error(t, err)
	assert.Nil(t, selected)
}

// TestParetoFrontierRemovesDominatedCandidates verifies the core Pareto algorithm:
// a candidate dominated on all axes is excluded from the frontier.
// @trace FR-ROUTING-003
func TestParetoFrontierRemovesDominatedCandidates(t *testing.T) {
	// cheap + fast + good dominates expensive + slow + bad.
	dominated := &RoutingCandidate{
		ModelID:            "bad-model",
		EstimatedCost:      0.05,
		EstimatedLatencyMs: 10000,
		QualityScore:       0.60,
	}
	dominator := &RoutingCandidate{
		ModelID:            "good-model",
		EstimatedCost:      0.01,
		EstimatedLatencyMs: 1000,
		QualityScore:       0.90,
	}

	frontier := computeParetoFrontier([]*RoutingCandidate{dominated, dominator})

	assert.Len(t, frontier, 1)
	assert.Equal(t, "good-model", frontier[0].ModelID)
}

// TestParetoFrontierKeepsNonDominatedSet verifies that two candidates where neither
// dominates the other both appear on the frontier.
// @trace FR-ROUTING-003
func TestParetoFrontierKeepsNonDominatedSet(t *testing.T) {
	// cheap+fast but lower quality vs expensive+slow but higher quality — no dominance.
	fast := &RoutingCandidate{
		ModelID:            "fast-cheap",
		EstimatedCost:      0.001,
		EstimatedLatencyMs: 400,
		QualityScore:       0.72,
	}
	smart := &RoutingCandidate{
		ModelID:            "smart-expensive",
		EstimatedCost:      0.015,
		EstimatedLatencyMs: 4000,
		QualityScore:       0.95,
	}

	frontier := computeParetoFrontier([]*RoutingCandidate{fast, smart})

	assert.Len(t, frontier, 2)
}

// TestSelectFromCandidatesPrefersHighRatio verifies that selectFromCandidates picks
// the candidate with the best quality/cost ratio.
// @trace FR-ROUTING-001
func TestSelectFromCandidatesPrefersHighRatio(t *testing.T) {
	lowRatio := &RoutingCandidate{
		ModelID:       "pricey",
		EstimatedCost: 0.10,
		QualityScore:  0.80, // ratio = 8
	}
	highRatio := &RoutingCandidate{
		ModelID:       "efficient",
		EstimatedCost: 0.01,
		QualityScore:  0.80, // ratio = 80
	}

	winner := selectFromCandidates([]*RoutingCandidate{lowRatio, highRatio})
	assert.Equal(t, "efficient", winner.ModelID)
}

// TestSelectFromCandidatesEmpty verifies nil is returned on empty frontier.
func TestSelectFromCandidatesEmpty(t *testing.T) {
	result := selectFromCandidates([]*RoutingCandidate{})
	assert.Nil(t, result)
}

// TestIsDominated verifies the dominance predicate.
// @trace FR-ROUTING-003
func TestIsDominated(t *testing.T) {
	base := &RoutingCandidate{EstimatedCost: 0.05, EstimatedLatencyMs: 5000, QualityScore: 0.70}
	better := &RoutingCandidate{EstimatedCost: 0.01, EstimatedLatencyMs: 1000, QualityScore: 0.90}
	equal := &RoutingCandidate{EstimatedCost: 0.05, EstimatedLatencyMs: 5000, QualityScore: 0.70}

	assert.True(t, isDominated(base, better), "better should dominate base")
	assert.False(t, isDominated(base, equal), "equal should not dominate base")
	assert.False(t, isDominated(better, base), "base should not dominate better")
}

// TestInferProvider verifies provider inference from model IDs.
func TestInferProvider(t *testing.T) {
	cases := []struct {
		model    string
		expected string
	}{
		{"claude-sonnet-4.6", "claude"},
		{"gpt-4o", "openai"},
		{"gemini-3-flash", "gemini"},
		{"deepseek-v3.2", "deepseek"},
		{"roo-default", "roo"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, inferProvider(tc.model), "model=%s", tc.model)
	}
}

// TestRatioZeroCost verifies that zero-cost models get +Inf ratio.
func TestRatioZeroCost(t *testing.T) {
	c := &RoutingCandidate{EstimatedCost: 0, QualityScore: 0.70}
	assert.True(t, math.IsInf(ratio(c), 1))
}
