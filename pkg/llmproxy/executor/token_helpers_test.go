package executor

import (
	"testing"
)

func TestTokenizerForModel(t *testing.T) {
	cases := []struct {
		model   string
		wantAdj float64
	}{
		{"gpt-4", 1.0},
		{"claude-3-sonnet", 1.1},
		{"kiro-model", 1.1},
		{"amazonq-model", 1.1},
		{"gpt-3.5-turbo", 1.0},
		{"o1-preview", 1.0},
		{"unknown", 1.0},
	}
	for _, tc := range cases {
		tw, err := tokenizerForModel(tc.model)
		if err != nil {
			t.Errorf("tokenizerForModel(%q) error: %v", tc.model, err)
			continue
		}
		if tw.AdjustmentFactor != tc.wantAdj {
			t.Errorf("tokenizerForModel(%q) adjustment = %v, want %v", tc.model, tw.AdjustmentFactor, tc.wantAdj)
		}
	}
}

func TestCountOpenAIChatTokens(t *testing.T) {
	tw, _ := tokenizerForModel("gpt-4o")
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	count, err := countOpenAIChatTokens(tw, payload)
	if err != nil {
		t.Errorf("countOpenAIChatTokens failed: %v", err)
	}
	if count <= 0 {
		t.Errorf("expected positive token count, got %d", count)
	}
}

func TestCountClaudeChatTokens(t *testing.T) {
	tw, _ := tokenizerForModel("claude-3")
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}],"system":"be helpful"}`)
	count, err := countClaudeChatTokens(tw, payload)
	if err != nil {
		t.Errorf("countClaudeChatTokens failed: %v", err)
	}
	if count <= 0 {
		t.Errorf("expected positive token count, got %d", count)
	}
}

func TestEstimateImageTokens(t *testing.T) {
	cases := []struct {
		w, h float64
		want int
	}{
		{0, 0, 1000},
		{100, 100, 85},     // 10000/750 = 13.3 -> min 85
		{1000, 1000, 1333}, // 1000000/750 = 1333
		{2000, 2000, 1590}, // max 1590
	}
	for _, tc := range cases {
		got := estimateImageTokens(tc.w, tc.h)
		if got != tc.want {
			t.Errorf("estimateImageTokens(%v, %v) = %d, want %d", tc.w, tc.h, got, tc.want)
		}
	}
}
<<<<<<< HEAD
=======

func TestIsGPT5FamilyModel(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"gpt-5":           true,
		"gpt-5.1":         true,
		"gpt-5.3-codex":   true,
		"gpt-5-pro":       true,
		"gpt-4o":          false,
		"claude-sonnet-4": false,
	}
	for model, want := range cases {
		if got := isGPT5FamilyModel(model); got != want {
			t.Fatalf("isGPT5FamilyModel(%q) = %v, want %v", model, got, want)
		}
	}
}
>>>>>>> archive/pr-234-head-20260223
