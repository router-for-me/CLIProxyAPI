package registry

import (
	"context"
	"testing"
)

func TestParetoRoutingSelectsOptimalModelGivenConstraints(t *testing.T) {
	router := NewParetoRouter()

	req := &RoutingRequest{
		TaskComplexity:  "NORMAL",
		MaxCostPerCall:  0.01,
		MaxLatencyMs:    5000,
		MinQualityScore: 0.75,
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
