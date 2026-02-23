package registry

import (
	"context"
	"testing"
<<<<<<< HEAD
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
=======

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// @trace FR-ROUTING-004

func TestTaskClassifierCategorizesFast(t *testing.T) {
	tc := NewTaskClassifier()

	req := &TaskClassificationRequest{
		TokensIn:  250,
		TokensOut: 100,
		Metadata:  map[string]string{"category": "quick_lookup"},
	}

	category, err := tc.Classify(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "FAST", category)
>>>>>>> ci-compile-fix
}

func TestTaskClassifierCategorizesNormal(t *testing.T) {
	tc := NewTaskClassifier()
<<<<<<< HEAD
	req := &TaskClassificationRequest{TokensIn: 2500, TokensOut: 500}
	category, err := tc.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if category != "NORMAL" {
		t.Errorf("expected NORMAL, got %s", category)
	}
=======

	req := &TaskClassificationRequest{
		TokensIn:  2500,
		TokensOut: 500,
	}

	category, err := tc.Classify(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "NORMAL", category)
>>>>>>> ci-compile-fix
}

func TestTaskClassifierCategorizesComplex(t *testing.T) {
	tc := NewTaskClassifier()
<<<<<<< HEAD
	req := &TaskClassificationRequest{TokensIn: 25000, TokensOut: 5000}
	category, err := tc.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if category != "COMPLEX" {
		t.Errorf("expected COMPLEX, got %s", category)
	}
=======

	req := &TaskClassificationRequest{
		TokensIn:  25000,
		TokensOut: 5000,
	}

	category, err := tc.Classify(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "COMPLEX", category)
>>>>>>> ci-compile-fix
}

func TestTaskClassifierCategorizesHighComplex(t *testing.T) {
	tc := NewTaskClassifier()
<<<<<<< HEAD
	req := &TaskClassificationRequest{TokensIn: 100000}
	category, err := tc.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if category != "HIGH_COMPLEX" {
		t.Errorf("expected HIGH_COMPLEX, got %s", category)
	}
=======

	req := &TaskClassificationRequest{
		TokensIn: 100000,
	}

	category, err := tc.Classify(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "HIGH_COMPLEX", category)
>>>>>>> ci-compile-fix
}

func TestTaskClassifierBoundaries(t *testing.T) {
	tc := NewTaskClassifier()
<<<<<<< HEAD
	tests := []struct {
=======
	ctx := context.Background()

	cases := []struct {
>>>>>>> ci-compile-fix
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

<<<<<<< HEAD
	for _, tt := range tests {
		req := &TaskClassificationRequest{TokensIn: tt.tokensIn, TokensOut: tt.tokensOut}
		got, err := tc.Classify(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != tt.expected {
			t.Errorf("Classify(tokens=%d) = %s, want %s", tt.tokensIn+tt.tokensOut, got, tt.expected)
		}
=======
	for _, tc2 := range cases {
		got, err := tc.Classify(ctx, &TaskClassificationRequest{
			TokensIn:  tc2.tokensIn,
			TokensOut: tc2.tokensOut,
		})
		require.NoError(t, err)
		assert.Equal(t, tc2.expected, got, "tokensIn=%d tokensOut=%d", tc2.tokensIn, tc2.tokensOut)
>>>>>>> ci-compile-fix
	}
}
