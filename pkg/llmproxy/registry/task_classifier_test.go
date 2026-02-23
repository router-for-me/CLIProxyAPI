package registry

import (
	"context"
	"testing"

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
}

func TestTaskClassifierCategorizesNormal(t *testing.T) {
	tc := NewTaskClassifier()

	req := &TaskClassificationRequest{
		TokensIn:  2500,
		TokensOut: 500,
	}

	category, err := tc.Classify(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "NORMAL", category)
}

func TestTaskClassifierCategorizesComplex(t *testing.T) {
	tc := NewTaskClassifier()

	req := &TaskClassificationRequest{
		TokensIn:  25000,
		TokensOut: 5000,
	}

	category, err := tc.Classify(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "COMPLEX", category)
}

func TestTaskClassifierCategorizesHighComplex(t *testing.T) {
	tc := NewTaskClassifier()

	req := &TaskClassificationRequest{
		TokensIn: 100000,
	}

	category, err := tc.Classify(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "HIGH_COMPLEX", category)
}

func TestTaskClassifierBoundaries(t *testing.T) {
	tc := NewTaskClassifier()
	ctx := context.Background()

	cases := []struct {
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

	for _, tc2 := range cases {
		got, err := tc.Classify(ctx, &TaskClassificationRequest{
			TokensIn:  tc2.tokensIn,
			TokensOut: tc2.tokensOut,
		})
		require.NoError(t, err)
		assert.Equal(t, tc2.expected, got, "tokensIn=%d tokensOut=%d", tc2.tokensIn, tc2.tokensOut)
	}
}
