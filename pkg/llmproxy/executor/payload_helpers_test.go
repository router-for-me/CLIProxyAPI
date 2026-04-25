package executor

import (
	"encoding/json"
	"testing"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPayloadModelRulesMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rules    []config.PayloadModelRule
		protocol string
		models   []string
		want     bool
	}{
		{
			name:     "empty rules returns false",
			rules:    nil,
			protocol: "gemini",
			models:   []string{"gemini-2.0-flash"},
			want:     false,
		},
		{
			name:     "empty models with conditional rule returns false",
			rules:    []config.PayloadModelRule{{Name: "gemini-*", Protocol: "gemini"}},
			protocol: "gemini",
			models:   []string{},
			want:     false,
		},
		{
			name:     "unconditional rule matches empty models",
			rules:    []config.PayloadModelRule{{Name: "", Protocol: ""}},
			protocol: "gemini",
			models:   []string{},
			want:     true,
		},
		{
			name:     "unconditional rule with protocol matches empty models",
			rules:    []config.PayloadModelRule{{Name: "", Protocol: "gemini"}},
			protocol: "gemini",
			models:   []string{},
			want:     true,
		},
		{
			name:     "unconditional rule with wrong protocol returns false",
			rules:    []config.PayloadModelRule{{Name: "", Protocol: "openai"}},
			protocol: "gemini",
			models:   []string{},
			want:     false,
		},
		{
			name:     "conditional rule matches model",
			rules:    []config.PayloadModelRule{{Name: "gemini-*", Protocol: ""}},
			protocol: "gemini",
			models:   []string{"gemini-2.0-flash"},
			want:     true,
		},
		{
			name:     "conditional rule does not match wrong model",
			rules:    []config.PayloadModelRule{{Name: "gpt-*", Protocol: ""}},
			protocol: "gemini",
			models:   []string{"gemini-2.0-flash"},
			want:     false,
		},
		{
			name:     "protocol mismatch returns false",
			rules:    []config.PayloadModelRule{{Name: "gemini-*", Protocol: "openai"}},
			protocol: "gemini",
			models:   []string{"gemini-2.0-flash"},
			want:     false,
		},
		{
			name:     "wildcard name matches any model",
			rules:    []config.PayloadModelRule{{Name: "*", Protocol: ""}},
			protocol: "gemini",
			models:   []string{"any-model"},
			want:     true,
		},
		{
			name:     "mixed rules - one unconditional one conditional",
			rules:    []config.PayloadModelRule{{Name: "", Protocol: ""}, {Name: "gpt-*", Protocol: ""}},
			protocol: "gemini",
			models:   []string{},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := payloadModelRulesMatch(tt.rules, tt.protocol, tt.models)
			assert.Equal(t, tt.want, got, "payloadModelRulesMatch(%+v, %q, %+v)", tt.rules, tt.protocol, tt.models)
		})
	}
}

func TestPayloadModelCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		model          string
		requestedModel string
		want           []string
	}{
		{
			name:           "both empty returns nil",
			model:          "",
			requestedModel: "",
			want:           nil,
		},
		{
			name:           "only model returns model",
			model:          "gemini-2.0-flash",
			requestedModel: "",
			want:           []string{"gemini-2.0-flash"},
		},
		{
			name:           "only requestedModel returns base and alias",
			model:          "",
			requestedModel: "gemini-2.0-flash+thinking",
			want:           []string{"gemini-2.0-flash", "gemini-2.0-flash+thinking"},
		},
		{
			name:           "both model and requestedModel returns both",
			model:          "gemini-2.0-flash",
			requestedModel: "gemini-pro",
			want:           []string{"gemini-2.0-flash", "gemini-pro"},
		},
		{
			name:           "duplicate model deduplicated",
			model:          "gemini-2.0-flash",
			requestedModel: "gemini-2.0-flash",
			want:           []string{"gemini-2.0-flash"},
		},
		{
			name:           "whitespace trimmed",
			model:          "  gemini-2.0-flash  ",
			requestedModel: "  gemini-pro  ",
			want:           []string{"gemini-2.0-flash", "gemini-pro"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := payloadModelCandidates(tt.model, tt.requestedModel)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestApplyPayloadConfigWithRoot_UnconditionalRules(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{
				{
					// Unconditional rule - no models specified
					Models: []config.PayloadModelRule{},
					Params: map[string]any{"maxTokens": 1000},
				},
			},
			Override: []config.PayloadRule{
				{
					// Conditional rule - specific model
					Models: []config.PayloadModelRule{
						{Name: "gemini-*", Protocol: ""},
					},
					Params: map[string]any{"temperature": 0.7},
				},
			},
		},
	}

	payload := []byte(`{"model":"gemini-2.0-flash","maxTokens":500}`)
	original := []byte(`{"model":"gemini-2.0-flash","maxTokens":200}`)

	result := applyPayloadConfigWithRoot(cfg, "gemini-2.0-flash", "", "", payload, original, "")

	var got map[string]any
	err := json.Unmarshal(result, &got)
	require.NoError(t, err)

	// Unconditional default rule should set maxTokens (1000 > 200 but source has it)
	assert.Equal(t, float64(200), got["maxTokens"], "should keep original maxTokens")

	// Conditional override rule should apply temperature
	assert.Equal(t, 0.7, got["temperature"], "conditional override should apply")
}

func TestApplyPayloadConfigWithRoot_ProtocolMatching(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "", Protocol: "gemini"}, // Protocol-specific unconditional
					},
					Params: map[string]any{"safetySettings": "BLOCK_NONE"},
				},
				{
					Models: []config.PayloadModelRule{
						{Name: "", Protocol: "responses"}, // Different protocol
					},
					Params: map[string]any{"maxOutputTokens": 4096},
				},
			},
		},
	}

	t.Run("gemini protocol applies", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"model":"gemini-2.0-flash"}`)
		result := applyPayloadConfigWithRoot(cfg, "gemini-2.0-flash", "gemini", "", payload, nil, "")
		var got map[string]any
		err := json.Unmarshal(result, &got)
		require.NoError(t, err)
		assert.Equal(t, "BLOCK_NONE", got["safetySettings"])
	})

	t.Run("responses protocol applies", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"model":"responses-model"}`)
		result := applyPayloadConfigWithRoot(cfg, "responses-model", "responses", "", payload, nil, "")
		var got map[string]any
		err := json.Unmarshal(result, &got)
		require.NoError(t, err)
		assert.Equal(t, float64(4096), got["maxOutputTokens"])
	})

	t.Run("mismatched protocol does not apply", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"model":"openai-model"}`)
		result := applyPayloadConfigWithRoot(cfg, "openai-model", "openai", "", payload, nil, "")
		var got map[string]any
		err := json.Unmarshal(result, &got)
		require.NoError(t, err)
		_, hasSafety := got["safetySettings"]
		_, hasMaxOutput := got["maxOutputTokens"]
		assert.False(t, hasSafety || hasMaxOutput, "no rules should apply for mismatched protocol")
	})
}

func TestApplyPayloadConfigWithRoot_RequestedModelAlias(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{
				{
					// Rule targeting the alias
					Models: []config.PayloadModelRule{
						{Name: "gemini-pro", Protocol: ""},
					},
					Params: map[string]any{"thinkingBudget": 10000},
				},
			},
		},
	}

	payload := []byte(`{"model":"gemini-2.0-pro"}`)
	// Original request had the alias "gemini-pro"
	original := []byte(`{"model":"gemini-pro"}`)

	// Upstream model is "gemini-2.0-pro", but original was "gemini-pro"
	result := applyPayloadConfigWithRoot(cfg, "gemini-2.0-pro", "", "", payload, original, "gemini-pro")

	var got map[string]any
	err := json.Unmarshal(result, &got)
	require.NoError(t, err)

	// Rule targeting "gemini-pro" should apply because requestedModel is "gemini-pro"
	assert.Equal(t, float64(10000), got["thinkingBudget"], "rule should match via requestedModel alias")
}

func TestApplyPayloadConfigWithRoot_SplitCounts(t *testing.T) {
	t.Parallel()

	// This test verifies that conditional and unconditional rules are tracked separately
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{
				{
					// Unconditional rule
					Models: []config.PayloadModelRule{},
					Params: map[string]any{"defaultUnconditional": "value1"},
				},
				{
					// Conditional rule
					Models: []config.PayloadModelRule{
						{Name: "gemini-*", Protocol: ""},
					},
					Params: map[string]any{"defaultConditional": "value2"},
				},
			},
			Override: []config.PayloadRule{
				{
					// Unconditional rule
					Models: []config.PayloadModelRule{},
					Params: map[string]any{"overrideUnconditional": "value3"},
				},
				{
					// Conditional rule
					Models: []config.PayloadModelRule{
						{Name: "claude-*", Protocol: ""},
					},
					Params: map[string]any{"overrideConditional": "value4"},
				},
			},
			Filter: []config.PayloadFilterRule{
				{
					// Unconditional filter
					Models: []config.PayloadModelRule{},
					Params: []string{"filterUnconditional"},
				},
				{
					// Conditional filter
					Models: []config.PayloadModelRule{
						{Name: "gemini-*", Protocol: ""},
					},
					Params: []string{"filterConditional"},
				},
			},
		},
	}

	payload := []byte(`{
		"model":"gemini-2.0-flash",
		"defaultUnconditional":"orig1",
		"defaultConditional":"orig2",
		"overrideUnconditional":"orig3",
		"overrideConditional":"orig4",
		"filterUnconditional":"keep",
		"filterConditional":"keep"
	}`)
	original := []byte(`{"model":"gemini-2.0-flash"}`)

	result := applyPayloadConfigWithRoot(cfg, "gemini-2.0-flash", "", "", payload, original, "")

	var got map[string]any
	err := json.Unmarshal(result, &got)
	require.NoError(t, err)

	// Default rules: conditional gemini-* applies, unconditional applies
	assert.Equal(t, "value1", got["defaultUnconditional"])
	assert.Equal(t, "value2", got["defaultConditional"])

	// Override rules: conditional claude-* does NOT apply, unconditional applies
	assert.Equal(t, "value3", got["overrideUnconditional"])
	assert.Equal(t, "orig4", got["overrideConditional"])

	// Filter rules: both conditional and unconditional paths applied correctly
	_, hasFilterCond := got["filterConditional"]
	_, hasFilterUncond := got["filterUnconditional"]
	assert.False(t, hasFilterCond, "conditional filter should have removed the field")
	assert.True(t, hasFilterUncond, "unconditional filter should have removed the field")
}
