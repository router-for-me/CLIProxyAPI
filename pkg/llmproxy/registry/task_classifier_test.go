package registry

import (
	"context"
	"testing"
)

func TestTaskClassifierCategorizesFast(t *testing.T) {
	tc := NewTaskClassifier()
	req := &TaskClassificationRequest{TokensIn: 250, TokensOut: 100}
	category, err := tc.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if category != "FAST" {
		t.Errorf("expected FAST, got %s", category)
	}
}

func TestTaskClassifierCategorizesNormal(t *testing.T) {
	tc := NewTaskClassifier()
	req := &TaskClassificationRequest{TokensIn: 2500, TokensOut: 500}
	category, err := tc.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if category != "NORMAL" {
		t.Errorf("expected NORMAL, got %s", category)
	}
}

func TestTaskClassifierCategorizesComplex(t *testing.T) {
	tc := NewTaskClassifier()
	req := &TaskClassificationRequest{TokensIn: 25000, TokensOut: 5000}
	category, err := tc.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if category != "COMPLEX" {
		t.Errorf("expected COMPLEX, got %s", category)
	}
}

func TestTaskClassifierCategorizesHighComplex(t *testing.T) {
	tc := NewTaskClassifier()
	req := &TaskClassificationRequest{TokensIn: 100000}
	category, err := tc.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if category != "HIGH_COMPLEX" {
		t.Errorf("expected HIGH_COMPLEX, got %s", category)
	}
}

func TestTaskClassifierBoundaries(t *testing.T) {
	tc := NewTaskClassifier()
	tests := []struct {
		tokensIn  int
		tokensOut int
		expected  string
	}{
		{499, 0, "FAST"},
		{500, 0, "NORMAL"},
		{4999, 0, "NORMAL"},
		{5000, 0, "COMPLEX"},
		{49999, 0, "COMPLEX"},
		{50000, 0, "HIGH_COMPLEX"},
	}

	for _, tt := range tests {
		req := &TaskClassificationRequest{TokensIn: tt.tokensIn, TokensOut: tt.tokensOut}
		got, err := tc.Classify(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != tt.expected {
			t.Errorf("Classify(tokens=%d) = %s, want %s", tt.tokensIn+tt.tokensOut, got, tt.expected)
		}
	}
}
