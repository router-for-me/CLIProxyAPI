package registry

import (
	"context"
<<<<<<< HEAD
	"testing"
)

func TestParetoRoutingSelectsOptimalModelGivenConstraints(t *testing.T) {
	router := NewParetoRouter()
=======
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
>>>>>>> ci-compile-fix

	req := &RoutingRequest{
		TaskComplexity:  "NORMAL",
		MaxCostPerCall:  0.01,
		MaxLatencyMs:    5000,
		MinQualityScore: 0.75,
<<<<<<< HEAD
	}

	selected, err := router.SelectModel(context.Background(), req)
	if err != nil {
		t.Fatalf("SelectModel returned error: %v", err)
	}
	if selected == nil {
		t.Fatal("SelectModel returned nil")
	}

	if selected.EstimatedCost > req.MaxCostPerCall {
		t.Errorf("cost %.4f exceeds max %.4f", selected.EstimatedCost, req.MaxCostPerCall)
	}
	if selected.EstimatedLatencyMs > req.MaxLatencyMs {
		t.Errorf("latency %d exceeds max %d", selected.EstimatedLatencyMs, req.MaxLatencyMs)
	}
	if selected.QualityScore < req.MinQualityScore {
		t.Errorf("quality %.2f below min %.2f", selected.QualityScore, req.MinQualityScore)
	}
	if selected.ModelID == "" {
		t.Error("model_id is empty")
	}
	if selected.Provider == "" {
		t.Error("provider is empty")
	}
}

func TestParetoRoutingRejectsImpossibleConstraints(t *testing.T) {
	router := NewParetoRouter()

	req := &RoutingRequest{
		MaxCostPerCall:  0.0001, // impossibly cheap
		MaxLatencyMs:    100,    // impossibly fast
		MinQualityScore: 0.99,   // impossibly high quality
	}

	_, err := router.SelectModel(context.Background(), req)
	if err == nil {
		t.Error("expected error for impossible constraints, got nil")
	}
}

func TestParetoFrontierRemovesDominatedCandidates(t *testing.T) {
	candidates := []*RoutingCandidate{
		{ModelID: "cheap-good", EstimatedCost: 0.001, QualityScore: 0.9},
		{ModelID: "expensive-bad", EstimatedCost: 0.01, QualityScore: 0.7},
		{ModelID: "cheap-bad", EstimatedCost: 0.001, QualityScore: 0.7},
	}

	frontier := computeParetoFrontier(candidates)

	// "expensive-bad" is dominated by "cheap-good" (lower cost, higher quality)
	// "cheap-bad" is dominated by "cheap-good" (same cost, higher quality)
	if len(frontier) != 1 {
		t.Errorf("expected 1 frontier member, got %d", len(frontier))
		for _, c := range frontier {
			t.Logf("  frontier: %s cost=%.3f quality=%.2f", c.ModelID, c.EstimatedCost, c.QualityScore)
		}
	}
	if len(frontier) > 0 && frontier[0].ModelID != "cheap-good" {
		t.Errorf("expected 'cheap-good' on frontier, got %s", frontier[0].ModelID)
	}
}

func TestParetoFrontierKeepsNonDominatedSet(t *testing.T) {
	candidates := []*RoutingCandidate{
		{ModelID: "cheap-low-q", EstimatedCost: 0.001, QualityScore: 0.7},
		{ModelID: "mid-mid-q", EstimatedCost: 0.01, QualityScore: 0.85},
		{ModelID: "expensive-high-q", EstimatedCost: 0.05, QualityScore: 0.95},
	}

	frontier := computeParetoFrontier(candidates)

	// None dominates the other (tradeoff between cost and quality)
	if len(frontier) != 3 {
		t.Errorf("expected 3 frontier members (none dominated), got %d", len(frontier))
	}
}

func TestSelectFromCandidatesPrefersHighRatio(t *testing.T) {
	router := NewParetoRouter()
	candidates := []*RoutingCandidate{
		{ModelID: "low-ratio", EstimatedCost: 0.05, QualityScore: 0.80},   // ratio=16
		{ModelID: "high-ratio", EstimatedCost: 0.003, QualityScore: 0.78}, // ratio=260
	}

	selected, err := router.SelectFromCandidates(candidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected.ModelID != "high-ratio" {
		t.Errorf("expected high-ratio model, got %s", selected.ModelID)
	}
}

func TestSelectFromCandidatesEmpty(t *testing.T) {
	router := NewParetoRouter()
	_, err := router.SelectFromCandidates(nil)
	if err == nil {
		t.Error("expected error for empty candidates")
	}
}

func TestIsDominated(t *testing.T) {
	a := &RoutingCandidate{EstimatedCost: 0.01, QualityScore: 0.7}
	b := &RoutingCandidate{EstimatedCost: 0.005, QualityScore: 0.8}

	if !isDominated(a, b) {
		t.Error("a should be dominated by b (lower cost, higher quality)")
	}
	if isDominated(b, a) {
		t.Error("b should NOT be dominated by a")
	}
}

func TestInferProvider(t *testing.T) {
	tests := []struct {
		modelID  string
		expected string
	}{
		{"claude-sonnet-4.6", "claude"},
		{"gpt-5.3-codex", "openai"},
		{"gemini-3-flash", "gemini"},
		{"glm-5", "glm"},
		{"deepseek-v3.2", "deepseek"},
		{"unknown-model", "unknown"},
	}

	for _, tc := range tests {
		got := inferProvider(tc.modelID)
		if got != tc.expected {
			t.Errorf("inferProvider(%q) = %q, want %q", tc.modelID, got, tc.expected)
		}
	}
}
=======
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
>>>>>>> ci-compile-fix
