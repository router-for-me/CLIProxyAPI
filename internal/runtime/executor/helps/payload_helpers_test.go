package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"

	// Register provider appliers via init() so ApplyThinking works.
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/openai"
)

// ---------------------------------------------------------------------------
// Bug #1: ApplyThinking re-adds reasoning_effort after filter removes it
// ---------------------------------------------------------------------------
// When a model has a thinking suffix (e.g., "grok-3(medium)"), ApplyThinking
// extracts the thinking config from the suffix and re-adds "reasoning_effort"
// to the body — even though the payload filter just removed it.
// The filter should have final authority; once a parameter is removed,
// subsequent pipeline stages must not re-inject it.

func TestFilterReasoningEffort_NotReaddedBySuffixThinking(t *testing.T) {
	// Arrange: config filter removes reasoning_effort for grok-* models.
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "grok-*"},
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}

	// Payload that already has reasoning_effort (as it would after translation).
	payload := []byte(`{"model":"grok-3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`)

	// Correct pipeline order: ApplyThinking runs FIRST (may add/modify thinking
	// params from suffix), then ApplyPayloadConfigWithRoot runs LAST (filter has
	// final authority to remove parameters).

	// Step 1: ApplyThinking with a suffix model "grok-3(medium)".
	// The model is unknown to the registry, so it's treated as user-defined.
	// This may set/keep reasoning_effort based on the suffix.
	afterThinking, err := thinking.ApplyThinking(payload, "grok-3(medium)", "openai", "openai", "openrouter")
	if err != nil {
		t.Fatalf("ApplyThinking returned error: %v", err)
	}
	// Confirm reasoning_effort is present after thinking (suffix sets it).
	if !gjson.GetBytes(afterThinking, "reasoning_effort").Exists() {
		t.Fatalf("ApplyThinking should set reasoning_effort from suffix, got: %s", string(afterThinking))
	}

	// Step 2: Apply payload filter — should remove reasoning_effort, overriding
	// whatever ApplyThinking set. The filter has final authority.
	result := ApplyPayloadConfigWithRoot(cfg, "grok-3", "openai", "", afterThinking, payload, "grok-3(medium)")

	if gjson.GetBytes(result, "reasoning_effort").Exists() {
		t.Fatalf("BUG #1: filter should have final authority to remove reasoning_effort,\n"+
			"but it still exists after filtering.\n"+
			"After ApplyThinking: %s\n"+
			"After filter: %s",
			string(afterThinking), string(result))
	}
}

// TestFilterReasoningEffort_NoSuffix_NotReadded verifies the correct pipeline
// order (thinking → filter) also works for models without a thinking suffix.
// This is a regression guard.
func TestFilterReasoningEffort_NoSuffix_NotReadded(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "grok-3"},
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}

	payload := []byte(`{"model":"grok-3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)

	// Step 1: ApplyThinking (no suffix — passthrough for unknown model).
	afterThinking, err := thinking.ApplyThinking(payload, "grok-3", "openai", "openai", "openrouter")
	if err != nil {
		t.Fatalf("ApplyThinking returned error: %v", err)
	}

	// Step 2: Filter removes reasoning_effort.
	result := ApplyPayloadConfigWithRoot(cfg, "grok-3", "openai", "", afterThinking, payload, "grok-3")
	if gjson.GetBytes(result, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed by filter: %s", string(result))
	}
}

// ---------------------------------------------------------------------------
// Bug #2: Protocol mismatch — "openai" filter rules don't match "openai-response"
// ---------------------------------------------------------------------------
// The OpenAI Responses API path uses protocol "openai-response" internally,
// but users can only configure "openai" (the only documented protocol value).
// Filter rules with protocol: "openai" silently fail for Responses API requests.

func TestFilterProtocol_OpenAIResponseShouldMatchOpenAIRule(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "grok-*", Protocol: "openai"},
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}

	payload := []byte(`{"model":"grok-3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)

	// Simulate the Responses API path where protocol is "openai-response".
	filtered := ApplyPayloadConfigWithRoot(cfg, "grok-3", "openai-response", "", payload, payload, "grok-3")

	// BUG: The filter rule specifies protocol: "openai", but the actual protocol
	// is "openai-response". The rule doesn't match, so reasoning_effort is NOT removed.
	if gjson.GetBytes(filtered, "reasoning_effort").Exists() {
		t.Fatalf("BUG #2: filter rule with protocol 'openai' should also match 'openai-response',\n"+
			"but reasoning_effort was not removed: %s", string(filtered))
	}
}

func TestFilterProtocol_ExactOpenAIStillWorks(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "grok-*", Protocol: "openai"},
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}

	payload := []byte(`{"model":"grok-3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)

	// Chat Completions path uses "openai" — should match.
	filtered := ApplyPayloadConfigWithRoot(cfg, "grok-3", "openai", "", payload, payload, "grok-3")
	if gjson.GetBytes(filtered, "reasoning_effort").Exists() {
		t.Fatalf("filter with protocol 'openai' should match 'openai' protocol: %s", string(filtered))
	}
}

func TestFilterProtocol_EmptyProtocolMatchesBoth(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "grok-*"}, // no protocol restriction
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}

	payload := []byte(`{"model":"grok-3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)

	// Without protocol constraint, should match both "openai" and "openai-response".
	for _, proto := range []string{"openai", "openai-response"} {
		filtered := ApplyPayloadConfigWithRoot(cfg, "grok-3", proto, "", payload, payload, "grok-3")
		if gjson.GetBytes(filtered, "reasoning_effort").Exists() {
			t.Fatalf("filter without protocol should match %q, but reasoning_effort was not removed: %s",
				proto, string(filtered))
		}
	}
}

// ---------------------------------------------------------------------------
// Bug #3: Case-sensitive model matching
// ---------------------------------------------------------------------------
// matchModelPattern uses byte-level comparison, so "grok-*" does NOT match
// "Grok-3". Model names are case-inconsistent across providers, and users
// reasonably expect case-insensitive matching.

func TestFilterModelMatch_CaseInsensitive(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "grok-*"}, // lowercase pattern
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}

	payload := []byte(`{"model":"Grok-3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)

	// The model name is "Grok-3" (uppercase G), but the pattern is "grok-*" (lowercase).
	filtered := ApplyPayloadConfigWithRoot(cfg, "Grok-3", "openai", "", payload, payload, "Grok-3")

	// BUG: matchModelPattern is case-sensitive, so "grok-*" doesn't match "Grok-3".
	if gjson.GetBytes(filtered, "reasoning_effort").Exists() {
		t.Fatalf("BUG #3: filter pattern 'grok-*' should match 'Grok-3' case-insensitively,\n"+
			"but reasoning_effort was not removed: %s", string(filtered))
	}
}

func TestFilterModelMatch_ExactCaseWorks(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "grok-*"},
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}

	payload := []byte(`{"model":"grok-3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)

	// Same case — should work today as a regression guard.
	filtered := ApplyPayloadConfigWithRoot(cfg, "grok-3", "openai", "", payload, payload, "grok-3")
	if gjson.GetBytes(filtered, "reasoning_effort").Exists() {
		t.Fatalf("filter with matching case should remove reasoning_effort: %s", string(filtered))
	}
}

func TestMatchModelPattern_CaseInsensitiveWildcard(t *testing.T) {
	tests := []struct {
		pattern string
		model   string
		want    bool
	}{
		{"grok-*", "Grok-3", true},
		{"Grok-*", "grok-3", true},
		{"GROK-*", "grok-3", true},
		{"*-PRO", "gemini-2.5-pro", true},
		{"Claude-*", "claude-sonnet-4-5", true},
		// Exact matches with different case
		{"grok-3", "Grok-3", true},
		{"GPT-5", "gpt-5", true},
		// Already-matching cases should continue to work
		{"grok-*", "grok-3", true},
		{"*", "anything", true},
		{"exact-match", "exact-match", true},
		// Non-matches should still not match
		{"grok-*", "llama-3", false},
		{"gpt-*", "grok-3", false},
	}

	for _, tt := range tests {
		got := matchModelPattern(tt.pattern, tt.model)
		if got != tt.want {
			t.Errorf("matchModelPattern(%q, %q) = %v, want %v", tt.pattern, tt.model, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Baseline tests for existing payload filtering (should pass today)
// ---------------------------------------------------------------------------

func TestApplyPayloadConfig_FilterRemovesParameter(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gemini-2.5-pro", Protocol: "gemini"},
					},
					Params: []string{
						"generationConfig.thinkingConfig.thinkingBudget",
						"generationConfig.responseJsonSchema",
					},
				},
			},
		},
	}

	payload := []byte(`{"model":"gemini-2.5-pro","generationConfig":{"thinkingConfig":{"thinkingBudget":32768},"responseJsonSchema":{"type":"object"},"temperature":0.5}}`)

	filtered := ApplyPayloadConfigWithRoot(cfg, "gemini-2.5-pro", "gemini", "", payload, payload, "gemini-2.5-pro")

	if gjson.GetBytes(filtered, "generationConfig.thinkingConfig.thinkingBudget").Exists() {
		t.Fatalf("thinkingBudget should be removed: %s", string(filtered))
	}
	if gjson.GetBytes(filtered, "generationConfig.responseJsonSchema").Exists() {
		t.Fatalf("responseJsonSchema should be removed: %s", string(filtered))
	}
	// temperature should be preserved
	if !gjson.GetBytes(filtered, "generationConfig.temperature").Exists() {
		t.Fatalf("temperature should be preserved: %s", string(filtered))
	}
}

func TestApplyPayloadConfig_FilterWithWildcard(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-*"},
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}

	payload := []byte(`{"model":"gpt-5","reasoning_effort":"high","messages":[]}`)

	filtered := ApplyPayloadConfigWithRoot(cfg, "gpt-5", "openai", "", payload, payload, "gpt-5")
	if gjson.GetBytes(filtered, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed for gpt-* pattern: %s", string(filtered))
	}
}

func TestApplyPayloadConfig_FilterProtocolMismatchSkips(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-*", Protocol: "gemini"}, // wrong protocol
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}

	payload := []byte(`{"model":"gpt-5","reasoning_effort":"high","messages":[]}`)

	// Protocol is "openai", rule targets "gemini" — should NOT filter.
	filtered := ApplyPayloadConfigWithRoot(cfg, "gpt-5", "openai", "", payload, payload, "gpt-5")
	if !gjson.GetBytes(filtered, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should NOT be removed when protocol doesn't match: %s", string(filtered))
	}
}

func TestApplyPayloadConfig_FilterNoMatchingModel(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-*"},
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}

	payload := []byte(`{"model":"grok-3","reasoning_effort":"high","messages":[]}`)

	// Model "grok-3" doesn't match pattern "gpt-*" — should NOT filter.
	filtered := ApplyPayloadConfigWithRoot(cfg, "grok-3", "openai", "", payload, payload, "grok-3")
	if !gjson.GetBytes(filtered, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should NOT be removed for non-matching model: %s", string(filtered))
	}
}

func TestApplyPayloadConfig_DefaultSetsOnlyWhenMissing(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gemini-*"},
					},
					Params: map[string]any{
						"temperature": 0.7,
					},
				},
			},
		},
	}

	// Payload already has temperature — default should NOT overwrite.
	payload := []byte(`{"model":"gemini-2.5-pro","temperature":0.5}`)
	filtered := ApplyPayloadConfigWithRoot(cfg, "gemini-2.5-pro", "gemini", "", payload, payload, "gemini-2.5-pro")
	if gjson.GetBytes(filtered, "temperature").Float() != 0.5 {
		t.Fatalf("default should not overwrite existing temperature: %s", string(filtered))
	}

	// Payload without temperature — default should set it.
	payload2 := []byte(`{"model":"gemini-2.5-pro"}`)
	filtered2 := ApplyPayloadConfigWithRoot(cfg, "gemini-2.5-pro", "gemini", "", payload2, payload2, "gemini-2.5-pro")
	if gjson.GetBytes(filtered2, "temperature").Float() != 0.7 {
		t.Fatalf("default should set temperature when missing: %s", string(filtered2))
	}
}

func TestApplyPayloadConfig_OverrideAlwaysOverwrites(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-*", Protocol: "codex"},
					},
					Params: map[string]any{
						"reasoning.effort": "high",
					},
				},
			},
		},
	}

	payload := []byte(`{"model":"gpt-5","reasoning":{"effort":"low"}}`)
	filtered := ApplyPayloadConfigWithRoot(cfg, "gpt-5", "codex", "", payload, payload, "gpt-5")
	if gjson.GetBytes(filtered, "reasoning.effort").String() != "high" {
		t.Fatalf("override should overwrite reasoning.effort to 'high': %s", string(filtered))
	}
}

func TestApplyPayloadConfig_EmptyConfigReturnsUnchanged(t *testing.T) {
	cfg := &config.Config{}
	payload := []byte(`{"model":"grok-3","reasoning_effort":"high"}`)

	result := ApplyPayloadConfigWithRoot(cfg, "grok-3", "openai", "", payload, payload, "grok-3")
	if string(result) != string(payload) {
		t.Fatalf("empty config should return unchanged payload.\ngot: %s\nwant: %s", string(result), string(payload))
	}
}

func TestApplyPayloadConfig_NilConfigReturnsUnchanged(t *testing.T) {
	payload := []byte(`{"model":"grok-3","reasoning_effort":"high"}`)
	result := ApplyPayloadConfigWithRoot(nil, "grok-3", "openai", "", payload, payload, "grok-3")
	if string(result) != string(payload) {
		t.Fatalf("nil config should return unchanged payload")
	}
}

func TestApplyPayloadConfig_RootPath(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gemini-*"},
					},
					Params: []string{"generationConfig.thinkingConfig"},
				},
			},
		},
	}

	// Gemini CLI wraps payload under "request" root.
	payload := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192},"temperature":0.5}}}`)
	filtered := ApplyPayloadConfigWithRoot(cfg, "gemini-2.5-pro", "gemini-cli", "request", payload, payload, "gemini-2.5-pro")
	if gjson.GetBytes(filtered, "request.generationConfig.thinkingConfig").Exists() {
		t.Fatalf("thinkingConfig should be removed under root path: %s", string(filtered))
	}
	if !gjson.GetBytes(filtered, "request.generationConfig.temperature").Exists() {
		t.Fatalf("temperature should be preserved under root path: %s", string(filtered))
	}
}

// ---------------------------------------------------------------------------
// Test for payloadModelCandidates (model + requestedModel used in matching)
// ---------------------------------------------------------------------------

func TestPayloadModelCandidates_BothProvided(t *testing.T) {
	candidates := payloadModelCandidates("grok-3", "grok-3(high)")
	if len(candidates) == 0 {
		t.Fatalf("expected candidates, got none")
	}
	found := map[string]bool{}
	for _, c := range candidates {
		found[c] = true
	}
	if !found["grok-3"] {
		t.Fatalf("expected 'grok-3' in candidates: %v", candidates)
	}
	// The suffix version should also be present when HasSuffix is true.
	if !found["grok-3(high)"] {
		t.Fatalf("expected 'grok-3(high)' in candidates: %v", candidates)
	}
}

func TestPayloadModelCandidates_Empty(t *testing.T) {
	candidates := payloadModelCandidates("", "")
	if len(candidates) != 0 {
		t.Fatalf("expected no candidates for empty models, got: %v", candidates)
	}
}

// Ensure the test imports the registry package to satisfy the compiler.
var _ = registry.LookupModelInfo
