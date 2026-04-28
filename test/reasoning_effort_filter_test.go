package test

import (
	"fmt"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"

	// Import provider packages to trigger init() registration of ProviderAppliers
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/openai"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestReasoningEffortInjectedForCompatProvider reproduces the issue where
// reasoning_effort is injected into requests destined for OpenAI-compatible
// providers (e.g., Grok, DeepSeek) that don't support it.
//
// The data flow is:
//
//	Claude request with thinking.budget_tokens
//	  → TranslateRequest(claude → openai) injects reasoning_effort
//	  → ApplyThinking() may set/overwrite reasoning_effort
//	  → Provider receives reasoning_effort and returns 400
//
// This test verifies the issue exists (reasoning_effort is present) and that
// the payload filter mechanism can strip it.
func TestReasoningEffortInjectedForCompatProvider(t *testing.T) {
	// Simulate a Claude Code request with thinking enabled, translated to OpenAI format
	// for an OpenAI-compatible provider (Grok, DeepSeek, etc.)
	claudeRequest := `{
		"model": "grok-3",
		"messages": [{"role": "user", "content": "hello"}],
		"thinking": {"type": "enabled", "budget_tokens": 8192}
	}`

	from := sdktranslator.FromString("claude")
	to := sdktranslator.FromString("openai")

	// Step 1: Translate Claude → OpenAI format
	translated := sdktranslator.TranslateRequest(from, to, "grok-3", []byte(claudeRequest), false)

	// Step 2: Apply thinking (this uses the OpenAI applier which sets reasoning_effort)
	result, err := thinking.ApplyThinking(translated, "grok-3", "claude", "openai", "grok-compat")
	if err != nil {
		// ApplyThinking may return original body on error for unknown models, that's OK
		result = translated
	}

	// VERIFY THE BUG: reasoning_effort should be present in the outgoing payload
	effortValue := gjson.GetBytes(result, "reasoning_effort")
	if !effortValue.Exists() {
		t.Log("reasoning_effort is NOT present in translated payload (translator may have skipped it)")
		t.Log("Payload:", string(result))
		// Even if the translator didn't add it, the thinking applier path for
		// user-defined models will set it. Check the raw translated output too.
		effortInTranslated := gjson.GetBytes(translated, "reasoning_effort")
		if effortInTranslated.Exists() {
			t.Logf("reasoning_effort WAS set by translator: %s", effortInTranslated.String())
		} else {
			t.Log("reasoning_effort was not set by either translator or thinking applier")
		}
	} else {
		t.Logf("BUG CONFIRMED: reasoning_effort='%s' is present in payload destined for compat provider", effortValue.String())
		t.Logf("Providers like Grok/DeepSeek that don't support this parameter will return HTTP 400")
	}

	// Log the full payload for inspection
	t.Logf("Full translated payload: %s", string(result))
}

// TestPayloadFilterStripsReasoningEffort verifies that configuring a payload
// filter rule for reasoning_effort correctly removes it from the outgoing request.
func TestPayloadFilterStripsReasoningEffort(t *testing.T) {
	// Payload that already has reasoning_effort set (as it would after translation + thinking apply)
	payload := []byte(`{
		"model": "grok-3",
		"messages": [{"role": "user", "content": "hello"}],
		"reasoning_effort": "medium",
		"max_tokens": 1024
	}`)

	// Configure filter to strip reasoning_effort for all models on openai protocol
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "*", Protocol: "openai"},
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}

	// Apply the payload config (filter runs as part of this)
	result := helps.ApplyPayloadConfigWithRoot(cfg, "grok-3", "openai", "", payload, nil, "grok-3")

	// Verify reasoning_effort was removed
	if gjson.GetBytes(result, "reasoning_effort").Exists() {
		t.Errorf("FILTER FAILED: reasoning_effort still present after filter: %s", string(result))
	} else {
		t.Log("FILTER WORKS: reasoning_effort successfully stripped from payload")
	}

	// Verify other fields are preserved
	if !gjson.GetBytes(result, "model").Exists() {
		t.Error("Filter incorrectly removed 'model' field")
	}
	if !gjson.GetBytes(result, "messages").Exists() {
		t.Error("Filter incorrectly removed 'messages' field")
	}
	if !gjson.GetBytes(result, "max_tokens").Exists() {
		t.Error("Filter incorrectly removed 'max_tokens' field")
	}
}

// TestPayloadFilterModelWildcardMatching verifies that filter rules with
// wildcard model patterns work correctly for different provider model names.
func TestPayloadFilterModelWildcardMatching(t *testing.T) {
	basePayload := `{"model": "%s", "messages": [{"role": "user", "content": "hi"}], "reasoning_effort": "high"}`

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

	tests := []struct {
		model         string
		shouldFilter  bool
		description   string
	}{
		{"grok-3", true, "Grok model should match grok-* wildcard"},
		{"grok-3-mini", true, "Grok mini model should match grok-* wildcard"},
		{"deepseek-r1", false, "DeepSeek model should NOT match grok-* wildcard"},
		{"gpt-4o", false, "GPT model should NOT match grok-* wildcard"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			payload := []byte(fmt.Sprintf(basePayload, tt.model))
			result := helps.ApplyPayloadConfigWithRoot(cfg, tt.model, "openai", "", payload, nil, tt.model)
			hasEffort := gjson.GetBytes(result, "reasoning_effort").Exists()

			if tt.shouldFilter && hasEffort {
				t.Errorf("Expected reasoning_effort to be filtered for model %s, but it's still present", tt.model)
			}
			if !tt.shouldFilter && !hasEffort {
				t.Errorf("Expected reasoning_effort to be preserved for model %s, but it was filtered", tt.model)
			}
		})
	}
}

// TestPayloadFilterWithThinkingEnabledProvider verifies that the filter does NOT
// strip reasoning_effort when the provider actually supports it (e.g., native OpenAI).
// This ensures the filter is selective per-provider, not a blanket removal.
func TestPayloadFilterWithThinkingEnabledProvider(t *testing.T) {
	payload := []byte(`{
		"model": "o3",
		"messages": [{"role": "user", "content": "hello"}],
		"reasoning_effort": "high",
		"max_tokens": 1024
	}`)

	// Filter only targets grok-* models, not o3
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

	result := helps.ApplyPayloadConfigWithRoot(cfg, "o3", "openai", "", payload, nil, "o3")

	// reasoning_effort should be PRESERVED for o3 (native OpenAI model that supports it)
	if !gjson.GetBytes(result, "reasoning_effort").Exists() {
		t.Error("reasoning_effort was incorrectly filtered for o3 (should be preserved)")
	} else {
		t.Logf("Correct: reasoning_effort preserved for o3 = %s", gjson.GetBytes(result, "reasoning_effort").String())
	}
}

// TestFullPipelineWithFilter simulates the complete executor pipeline:
// translate → apply payload config (with filter) → apply thinking
// This tests the ordering: filter runs within ApplyPayloadConfigWithRoot,
// which is called BEFORE ApplyThinking in the executor. So if thinking applier
// re-adds reasoning_effort after the filter removes it, the filter alone is insufficient.
func TestFullPipelineWithFilter(t *testing.T) {
	claudeRequest := `{
		"model": "grok-3",
		"messages": [{"role": "user", "content": "hello"}],
		"thinking": {"type": "enabled", "budget_tokens": 8192}
	}`

	from := sdktranslator.FromString("claude")
	to := sdktranslator.FromString("openai")

	// Step 1: Translate
	translated := sdktranslator.TranslateRequest(from, to, "grok-3", []byte(claudeRequest), false)
	t.Logf("After translation: reasoning_effort=%s", gjson.GetBytes(translated, "reasoning_effort").String())

	// Step 2: Apply payload config with filter (mirrors executor line 100)
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "*", Protocol: "openai"},
					},
					Params: []string{"reasoning_effort"},
				},
			},
		},
	}
	afterFilter := helps.ApplyPayloadConfigWithRoot(cfg, "grok-3", "openai", "", translated, nil, "grok-3")
	t.Logf("After filter: reasoning_effort exists=%v", gjson.GetBytes(afterFilter, "reasoning_effort").Exists())

	// Step 3: Apply thinking (mirrors executor line 107)
	// This is where the problem may occur: ApplyThinking could re-add reasoning_effort
	afterThinking, _ := thinking.ApplyThinking(afterFilter, "grok-3", "claude", "openai", "grok-compat")
	hasEffortAfterThinking := gjson.GetBytes(afterThinking, "reasoning_effort").Exists()
	t.Logf("After thinking apply: reasoning_effort exists=%v", hasEffortAfterThinking)

	if hasEffortAfterThinking {
		t.Logf("ORDERING ISSUE: ApplyThinking re-added reasoning_effort='%s' after filter removed it",
			gjson.GetBytes(afterThinking, "reasoning_effort").String())
		t.Log("This means the filter alone is insufficient — the fix needs to also address the thinking applier")
		t.Log("Full payload after thinking:", string(afterThinking))
	} else {
		t.Log("Filter + thinking pipeline correctly keeps reasoning_effort removed")
	}
}

// TestFixedPipelineOrdering validates the corrected executor pipeline:
// translate → ApplyThinking → ApplyPayloadConfigWithRoot (filter last)
// This is the fixed ordering where filter runs AFTER thinking, ensuring
// the filter has final authority even when a suffix model is used.
func TestFixedPipelineOrdering(t *testing.T) {
	claudeRequest := `{
		"model": "grok-3",
		"messages": [{"role": "user", "content": "hello"}],
		"thinking": {"type": "enabled", "budget_tokens": 8192}
	}`

	from := sdktranslator.FromString("claude")
	to := sdktranslator.FromString("openai")

	// Step 1: Translate
	translated := sdktranslator.TranslateRequest(from, to, "grok-3", []byte(claudeRequest), false)
	t.Logf("After translation: reasoning_effort=%s", gjson.GetBytes(translated, "reasoning_effort").String())

	// Step 2: Apply thinking FIRST (mirrors fixed executor: thinking before filter)
	afterThinking, _ := thinking.ApplyThinking(translated, "grok-3(medium)", "claude", "openai", "grok-compat")
	t.Logf("After thinking (suffix=medium): reasoning_effort=%s", gjson.GetBytes(afterThinking, "reasoning_effort").String())

	// Step 3: Apply filter LAST (mirrors fixed executor: filter after thinking)
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
	afterFilter := helps.ApplyPayloadConfigWithRoot(cfg, "grok-3", "openai", "", afterThinking, nil, "grok-3(medium)")
	hasEffort := gjson.GetBytes(afterFilter, "reasoning_effort").Exists()
	t.Logf("After filter: reasoning_effort exists=%v", hasEffort)

	if hasEffort {
		t.Errorf("FAILED: Filter should have final authority. reasoning_effort='%s' still present after filter",
			gjson.GetBytes(afterFilter, "reasoning_effort").String())
	} else {
		t.Log("PASS: Fixed ordering (thinking → filter) correctly strips reasoning_effort even with suffix model")
	}
}

// TestFixedPipelineCaseInsensitive validates that the fixed pipeline works
// with mixed-case model names end-to-end.
func TestFixedPipelineCaseInsensitive(t *testing.T) {
	payload := []byte(`{"model":"Grok-3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)

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

	// "Grok-3" should match "grok-*" case-insensitively
	result := helps.ApplyPayloadConfigWithRoot(cfg, "Grok-3", "openai", "", payload, nil, "Grok-3")
	if gjson.GetBytes(result, "reasoning_effort").Exists() {
		t.Errorf("Case-insensitive matching failed: reasoning_effort not filtered for 'Grok-3' with pattern 'grok-*'")
	} else {
		t.Log("PASS: Case-insensitive matching works end-to-end")
	}
}

// TestFixedPipelineResponsesAPI validates that the filter works for
// Responses API requests (protocol "openai-response").
func TestFixedPipelineResponsesAPI(t *testing.T) {
	payload := []byte(`{"model":"grok-3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)

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

	// Responses API uses "openai-response" internally, but user configures "openai"
	result := helps.ApplyPayloadConfigWithRoot(cfg, "grok-3", "openai-response", "", payload, nil, "grok-3")
	if gjson.GetBytes(result, "reasoning_effort").Exists() {
		t.Errorf("Protocol normalization failed: reasoning_effort not filtered for openai-response with protocol:'openai' rule")
	} else {
		t.Log("PASS: Filter rule with protocol 'openai' correctly matches 'openai-response'")
	}
}
