package test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// isOpenAICompatModel returns true if the model is configured as an OpenAI-compatible
// model that should have reasoning effort passed through even if not in registry.
// This simulates the allowCompat behavior from OpenAICompatExecutor.
func isOpenAICompatModel(model string) bool {
	return model == "openai-compat"
}

// registerCoreModels loads representative models across providers into the registry
// so NormalizeThinkingBudget and level validation use real ranges.
func registerCoreModels(t *testing.T) func() {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-core-%d", time.Now().UnixNano())
	reg.RegisterClient(uid+"-gemini", "gemini", registry.GetGeminiModels())
	reg.RegisterClient(uid+"-claude", "claude", registry.GetClaudeModels())
	reg.RegisterClient(uid+"-openai", "codex", registry.GetOpenAIModels())
	reg.RegisterClient(uid+"-qwen", "qwen", registry.GetQwenModels())
	// Custom openai-compatible model with forced thinking suffix passthrough.
	// No Thinking field - simulates an external model added via openai-compat
	// where the registry has no knowledge of its thinking capabilities.
	// The allowCompat flag should preserve reasoning effort for such models.
	customOpenAIModels := []*registry.ModelInfo{
		{
			ID:          "openai-compat",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "custom-provider",
			Type:        "openai",
			DisplayName: "OpenAI Compatible Model",
			Description: "OpenAI-compatible model with forced thinking suffix support",
		},
	}
	reg.RegisterClient(uid+"-custom-openai", "codex", customOpenAIModels)
	return func() {
		reg.UnregisterClient(uid + "-gemini")
		reg.UnregisterClient(uid + "-claude")
		reg.UnregisterClient(uid + "-openai")
		reg.UnregisterClient(uid + "-qwen")
		reg.UnregisterClient(uid + "-custom-openai")
	}
}

var (
	thinkingTestModels = []string{
		"gpt-5",           // level-based thinking model
		"gemini-2.5-pro",  // numeric-budget thinking model
		"qwen3-code-plus", // no thinking support
		"openai-compat",   // allowCompat=true (OpenAI-compatible channel)
	}
	thinkingTestFromProtocols = []string{"openai", "claude", "gemini", "openai-response"}
	thinkingTestToProtocols   = []string{"gemini", "claude", "openai", "codex"}

	// Numeric budgets and their level equivalents:
	// -1 -> auto
	// 0 -> none
	// 1..1024 -> low
	// 1025..8192 -> medium
	// 8193..24576 -> high
	// >24576 -> model highest level (right-most in Levels)
	thinkingNumericSamples = []int{-1, 0, 1023, 1025, 8193, 64000}

	// Levels and their numeric equivalents:
	// auto -> -1
	// none -> 0
	// minimal -> 512
	// low -> 1024
	// medium -> 8192
	// high -> 24576
	// xhigh -> 32768
	// invalid -> invalid (no mapping)
	thinkingLevelSamples = []string{"auto", "none", "minimal", "low", "medium", "high", "xhigh", "invalid"}
)

func buildRawPayload(fromProtocol, modelWithSuffix string) []byte {
	switch fromProtocol {
	case "gemini":
		return []byte(fmt.Sprintf(`{"model":"%s","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, modelWithSuffix))
	case "openai-response":
		return []byte(fmt.Sprintf(`{"model":"%s","input":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, modelWithSuffix))
	default: // openai / claude and other chat-style payloads
		return []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, modelWithSuffix))
	}
}

// normalizeCodexPayload mirrors codex_executor's reasoning + streaming tweaks.
func normalizeCodexPayload(body []byte, upstreamModel string, allowCompat bool) ([]byte, error) {
	body = executor.NormalizeThinkingConfig(body, upstreamModel, allowCompat)
	if err := executor.ValidateThinkingConfig(body, upstreamModel); err != nil {
		return body, err
	}
	body, _ = sjson.SetBytes(body, "model", upstreamModel)
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	return body, nil
}

// buildBodyForProtocol runs a minimal request through the same translation and
// thinking pipeline used in executors for the given target protocol.
func buildBodyForProtocol(t *testing.T, fromProtocol, toProtocol, modelWithSuffix string) ([]byte, error) {
	t.Helper()
	normalizedModel, metadata := util.NormalizeThinkingModel(modelWithSuffix)
	upstreamModel := util.ResolveOriginalModel(normalizedModel, metadata)
	raw := buildRawPayload(fromProtocol, modelWithSuffix)
	stream := fromProtocol != toProtocol

	body := sdktranslator.TranslateRequest(
		sdktranslator.FromString(fromProtocol),
		sdktranslator.FromString(toProtocol),
		normalizedModel,
		raw,
		stream,
	)

	var err error
	allowCompat := isOpenAICompatModel(normalizedModel)
	switch toProtocol {
	case "gemini":
		body = executor.ApplyThinkingMetadata(body, metadata, normalizedModel)
		body = util.ApplyDefaultThinkingIfNeeded(normalizedModel, body)
		body = util.NormalizeGeminiThinkingBudget(normalizedModel, body)
		body = util.StripThinkingConfigIfUnsupported(normalizedModel, body)
	case "claude":
		if budget, ok := util.ResolveClaudeThinkingConfig(normalizedModel, metadata); ok {
			body = util.ApplyClaudeThinkingConfig(body, budget)
		}
	case "openai":
		body = executor.ApplyReasoningEffortMetadata(body, metadata, normalizedModel, "reasoning_effort", allowCompat)
		body = executor.NormalizeThinkingConfig(body, upstreamModel, allowCompat)
		err = executor.ValidateThinkingConfig(body, upstreamModel)
	case "codex": // OpenAI responses / codex
		// Codex does not support allowCompat; always use false.
		body = executor.ApplyReasoningEffortMetadata(body, metadata, normalizedModel, "reasoning.effort", false)
		// Mirror CodexExecutor final normalization and model override so tests log the final body.
		body, err = normalizeCodexPayload(body, upstreamModel, false)
	default:
	}

	// Mirror executor behavior: final payload uses the upstream (base) model name.
	if upstreamModel != "" {
		body, _ = sjson.SetBytes(body, "model", upstreamModel)
	}

	// For tests we only keep model + thinking-related fields to avoid noise.
	body = filterThinkingBody(toProtocol, body, upstreamModel, normalizedModel)
	return body, err
}

// filterThinkingBody projects the translated payload down to only model and
// thinking-related fields for the given target protocol.
func filterThinkingBody(toProtocol string, body []byte, upstreamModel, normalizedModel string) []byte {
	if len(body) == 0 {
		return body
	}
	out := []byte(`{}`)

	// Preserve model if present, otherwise fall back to upstream/normalized model.
	if m := gjson.GetBytes(body, "model"); m.Exists() {
		out, _ = sjson.SetBytes(out, "model", m.Value())
	} else if upstreamModel != "" {
		out, _ = sjson.SetBytes(out, "model", upstreamModel)
	} else if normalizedModel != "" {
		out, _ = sjson.SetBytes(out, "model", normalizedModel)
	}

	switch toProtocol {
	case "gemini":
		if tc := gjson.GetBytes(body, "generationConfig.thinkingConfig"); tc.Exists() {
			out, _ = sjson.SetRawBytes(out, "generationConfig.thinkingConfig", []byte(tc.Raw))
		}
	case "claude":
		if tcfg := gjson.GetBytes(body, "thinking"); tcfg.Exists() {
			out, _ = sjson.SetRawBytes(out, "thinking", []byte(tcfg.Raw))
		}
	case "openai":
		if re := gjson.GetBytes(body, "reasoning_effort"); re.Exists() {
			out, _ = sjson.SetBytes(out, "reasoning_effort", re.Value())
		}
	case "codex":
		if re := gjson.GetBytes(body, "reasoning.effort"); re.Exists() {
			out, _ = sjson.SetBytes(out, "reasoning.effort", re.Value())
		}
	}
	return out
}

func TestThinkingConversionsAcrossProtocolsAndModels(t *testing.T) {
	cleanup := registerCoreModels(t)
	defer cleanup()

	type scenario struct {
		name        string
		modelSuffix string
	}

	numericName := func(budget int) string {
		if budget < 0 {
			return "numeric-neg1"
		}
		return fmt.Sprintf("numeric-%d", budget)
	}

	for _, model := range thinkingTestModels {
		_ = registry.GetGlobalRegistry().GetModelInfo(model)

		for _, from := range thinkingTestFromProtocols {
			// Scenario selection follows protocol semantics:
			// - OpenAI-style protocols (openai/openai-response) express thinking as levels.
			// - Claude/Gemini-style protocols express thinking as numeric budgets.
			cases := []scenario{
				{name: "no-suffix", modelSuffix: model},
			}
			if from == "openai" || from == "openai-response" {
				for _, lvl := range thinkingLevelSamples {
					cases = append(cases, scenario{
						name:        "level-" + lvl,
						modelSuffix: fmt.Sprintf("%s(%s)", model, lvl),
					})
				}
			} else { // claude or gemini
				for _, budget := range thinkingNumericSamples {
					budget := budget
					cases = append(cases, scenario{
						name:        numericName(budget),
						modelSuffix: fmt.Sprintf("%s(%d)", model, budget),
					})
				}
			}

			for _, to := range thinkingTestToProtocols {
				if from == to {
					continue
				}
				t.Logf("─────────────────────────────────────────────────────────────────────────────────")
				t.Logf("  %s -> %s | model: %s", from, to, model)
				t.Logf("─────────────────────────────────────────────────────────────────────────────────")
				for _, cs := range cases {
					from := from
					to := to
					cs := cs
					testName := fmt.Sprintf("%s->%s/%s/%s", from, to, model, cs.name)
					t.Run(testName, func(t *testing.T) {
						normalizedModel, metadata := util.NormalizeThinkingModel(cs.modelSuffix)
						expectPresent, expectValue, expectErr := func() (bool, string, bool) {
							switch to {
							case "gemini":
								budget, include, ok := util.ResolveThinkingConfigFromMetadata(normalizedModel, metadata)
								if !ok || !util.ModelSupportsThinking(normalizedModel) {
									return false, "", false
								}
								if include != nil && !*include {
									return false, "", false
								}
								if budget == nil {
									return false, "", false
								}
								norm := util.NormalizeThinkingBudget(normalizedModel, *budget)
								return true, fmt.Sprintf("%d", norm), false
							case "claude":
								if !util.ModelSupportsThinking(normalizedModel) {
									return false, "", false
								}
								budget, ok := util.ResolveClaudeThinkingConfig(normalizedModel, metadata)
								if !ok || budget == nil {
									return false, "", false
								}
								return true, fmt.Sprintf("%d", *budget), false
							case "openai":
								allowCompat := isOpenAICompatModel(normalizedModel)
								if !util.ModelSupportsThinking(normalizedModel) && !allowCompat {
									return false, "", false
								}
								// For allowCompat models, pass through effort directly without validation
								if allowCompat {
									effort, ok := util.ReasoningEffortFromMetadata(metadata)
									if ok && strings.TrimSpace(effort) != "" {
										return true, strings.ToLower(strings.TrimSpace(effort)), false
									}
									// Check numeric budget fallback for allowCompat
									if budget, _, _, matched := util.ThinkingFromMetadata(metadata); matched && budget != nil {
										if mapped, okMap := util.ThinkingBudgetToEffort(normalizedModel, *budget); okMap && mapped != "" {
											return true, mapped, false
										}
									}
									return false, "", false
								}
								if !util.ModelUsesThinkingLevels(normalizedModel) {
									// Non-levels models don't support effort strings in openai
									return false, "", false
								}
								effort, ok := util.ReasoningEffortFromMetadata(metadata)
								if !ok || strings.TrimSpace(effort) == "" {
									if budget, _, _, matched := util.ThinkingFromMetadata(metadata); matched && budget != nil {
										if mapped, okMap := util.ThinkingBudgetToEffort(normalizedModel, *budget); okMap {
											effort = mapped
											ok = true
										}
									}
								}
								if !ok || strings.TrimSpace(effort) == "" {
									return false, "", false
								}
								effort = strings.ToLower(strings.TrimSpace(effort))
								if normalized, okLevel := util.NormalizeReasoningEffortLevel(normalizedModel, effort); okLevel {
									return true, normalized, false
								}
								return false, "", true // validation would fail
							case "codex":
								// Codex does not support allowCompat; require thinking-capable level models.
								if !util.ModelSupportsThinking(normalizedModel) || !util.ModelUsesThinkingLevels(normalizedModel) {
									return false, "", false
								}
								effort, ok := util.ReasoningEffortFromMetadata(metadata)
								if ok && strings.TrimSpace(effort) != "" {
									effort = strings.ToLower(strings.TrimSpace(effort))
									if normalized, okLevel := util.NormalizeReasoningEffortLevel(normalizedModel, effort); okLevel {
										return true, normalized, false
									}
									return false, "", true
								}
								if budget, _, _, matched := util.ThinkingFromMetadata(metadata); matched && budget != nil {
									if mapped, okMap := util.ThinkingBudgetToEffort(normalizedModel, *budget); okMap && mapped != "" {
										mapped = strings.ToLower(strings.TrimSpace(mapped))
										if normalized, okLevel := util.NormalizeReasoningEffortLevel(normalizedModel, mapped); okLevel {
											return true, normalized, false
										}
										return false, "", true
									}
								}
								if from != "openai-response" {
									// Codex translators default reasoning.effort to "medium" when
									// no explicit thinking suffix/metadata is provided.
									return true, "medium", false
								}
								return false, "", false
							default:
								return false, "", false
							}
						}()

						body, err := buildBodyForProtocol(t, from, to, cs.modelSuffix)
						actualPresent, actualValue := func() (bool, string) {
							path := ""
							switch to {
							case "gemini":
								path = "generationConfig.thinkingConfig.thinkingBudget"
							case "claude":
								path = "thinking.budget_tokens"
							case "openai":
								path = "reasoning_effort"
							case "codex":
								path = "reasoning.effort"
							}
							if path == "" {
								return false, ""
							}
							val := gjson.GetBytes(body, path)
							if to == "codex" && !val.Exists() {
								reasoning := gjson.GetBytes(body, "reasoning")
								if reasoning.Exists() {
									val = reasoning.Get("effort")
								}
							}
							if !val.Exists() {
								return false, ""
							}
							if val.Type == gjson.Number {
								return true, fmt.Sprintf("%d", val.Int())
							}
							return true, val.String()
						}()

						t.Logf("from=%s to=%s model=%s suffix=%s present(expect=%v got=%v) value(expect=%s got=%s) err(expect=%v got=%v) body=%s",
							from, to, model, cs.modelSuffix, expectPresent, actualPresent, expectValue, actualValue, expectErr, err != nil, string(body))

						if expectErr {
							if err == nil {
								t.Fatalf("expected validation error but got none, body=%s", string(body))
							}
							return
						}
						if err != nil {
							t.Fatalf("unexpected error: %v body=%s", err, string(body))
						}

						if expectPresent != actualPresent {
							t.Fatalf("presence mismatch: expect %v got %v body=%s", expectPresent, actualPresent, string(body))
						}
						if expectPresent && expectValue != actualValue {
							t.Fatalf("value mismatch: expect %s got %s body=%s", expectValue, actualValue, string(body))
						}
					})
				}
			}
		}
	}
}

// buildRawPayloadWithThinking creates a payload with thinking parameters already in the body.
// This tests the path where thinking comes from the raw payload, not model suffix.
func buildRawPayloadWithThinking(fromProtocol, model string, thinkingParam any) []byte {
	switch fromProtocol {
	case "gemini":
		base := fmt.Sprintf(`{"model":"%s","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, model)
		if budget, ok := thinkingParam.(int); ok {
			base, _ = sjson.Set(base, "generationConfig.thinkingConfig.thinkingBudget", budget)
		}
		return []byte(base)
	case "openai-response":
		base := fmt.Sprintf(`{"model":"%s","input":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, model)
		if effort, ok := thinkingParam.(string); ok && effort != "" {
			base, _ = sjson.Set(base, "reasoning.effort", effort)
		}
		return []byte(base)
	case "openai":
		base := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, model)
		if effort, ok := thinkingParam.(string); ok && effort != "" {
			base, _ = sjson.Set(base, "reasoning_effort", effort)
		}
		return []byte(base)
	case "claude":
		base := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, model)
		if budget, ok := thinkingParam.(int); ok {
			base, _ = sjson.Set(base, "thinking.type", "enabled")
			base, _ = sjson.Set(base, "thinking.budget_tokens", budget)
		}
		return []byte(base)
	default:
		return []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, model))
	}
}

// buildBodyForProtocolWithRawThinking translates payload with raw thinking params.
func buildBodyForProtocolWithRawThinking(t *testing.T, fromProtocol, toProtocol, model string, thinkingParam any) ([]byte, error) {
	t.Helper()
	raw := buildRawPayloadWithThinking(fromProtocol, model, thinkingParam)
	stream := fromProtocol != toProtocol

	body := sdktranslator.TranslateRequest(
		sdktranslator.FromString(fromProtocol),
		sdktranslator.FromString(toProtocol),
		model,
		raw,
		stream,
	)

	var err error
	allowCompat := isOpenAICompatModel(model)
	switch toProtocol {
	case "gemini":
		body = util.ApplyDefaultThinkingIfNeeded(model, body)
		body = util.NormalizeGeminiThinkingBudget(model, body)
		body = util.StripThinkingConfigIfUnsupported(model, body)
	case "claude":
		// For raw payload, Claude thinking is passed through by translator
		// No additional processing needed as thinking is already in body
	case "openai":
		body = executor.NormalizeThinkingConfig(body, model, allowCompat)
		err = executor.ValidateThinkingConfig(body, model)
	case "codex":
		// Codex does not support allowCompat; always use false.
		body, err = normalizeCodexPayload(body, model, false)
	}

	body, _ = sjson.SetBytes(body, "model", model)
	body = filterThinkingBody(toProtocol, body, model, model)
	return body, err
}

func TestRawPayloadThinkingConversions(t *testing.T) {
	cleanup := registerCoreModels(t)
	defer cleanup()

	type scenario struct {
		name          string
		thinkingParam any // int for budget, string for effort level
	}

	numericName := func(budget int) string {
		if budget < 0 {
			return "budget-neg1"
		}
		return fmt.Sprintf("budget-%d", budget)
	}

	for _, model := range thinkingTestModels {
		supportsThinking := util.ModelSupportsThinking(model)
		usesLevels := util.ModelUsesThinkingLevels(model)
		allowCompat := isOpenAICompatModel(model)

		for _, from := range thinkingTestFromProtocols {
			var cases []scenario
			switch from {
			case "openai", "openai-response":
				cases = []scenario{
					{name: "no-thinking", thinkingParam: nil},
				}
				for _, lvl := range thinkingLevelSamples {
					cases = append(cases, scenario{
						name:          "effort-" + lvl,
						thinkingParam: lvl,
					})
				}
			case "gemini", "claude":
				cases = []scenario{
					{name: "no-thinking", thinkingParam: nil},
				}
				for _, budget := range thinkingNumericSamples {
					budget := budget
					cases = append(cases, scenario{
						name:          numericName(budget),
						thinkingParam: budget,
					})
				}
			}

			for _, to := range thinkingTestToProtocols {
				if from == to {
					continue
				}
				t.Logf("═══════════════════════════════════════════════════════════════════════════════")
				t.Logf("  RAW PAYLOAD: %s -> %s | model: %s", from, to, model)
				t.Logf("═══════════════════════════════════════════════════════════════════════════════")

				for _, cs := range cases {
					from := from
					to := to
					cs := cs
					testName := fmt.Sprintf("raw/%s->%s/%s/%s", from, to, model, cs.name)
					t.Run(testName, func(t *testing.T) {
						expectPresent, expectValue, expectErr := func() (bool, string, bool) {
							if cs.thinkingParam == nil {
								if to == "codex" && from != "openai-response" && supportsThinking && usesLevels {
									// Codex translators default reasoning.effort to "medium" for thinking-capable level models
									return true, "medium", false
								}
								return false, "", false
							}

							switch to {
							case "gemini":
								if !supportsThinking || usesLevels {
									return false, "", false
								}
								// Gemini expects numeric budget (only for non-level models)
								if budget, ok := cs.thinkingParam.(int); ok {
									norm := util.NormalizeThinkingBudget(model, budget)
									return true, fmt.Sprintf("%d", norm), false
								}
								// Convert effort level to budget for non-level models only
								if effort, ok := cs.thinkingParam.(string); ok && effort != "" {
									// "none" disables thinking - no thinkingBudget in output
									if strings.ToLower(effort) == "none" {
										return false, "", false
									}
									if budget, okB := util.ThinkingEffortToBudget(model, effort); okB {
										// ThinkingEffortToBudget already returns normalized budget
										return true, fmt.Sprintf("%d", budget), false
									}
									// Invalid effort does not map to a budget
									return false, "", false
								}
								return false, "", false
							case "claude":
								if !supportsThinking || usesLevels {
									return false, "", false
								}
								// Claude expects numeric budget (only for non-level models)
								if budget, ok := cs.thinkingParam.(int); ok && budget > 0 {
									norm := util.NormalizeThinkingBudget(model, budget)
									return true, fmt.Sprintf("%d", norm), false
								}
								// Convert effort level to budget for non-level models only
								if effort, ok := cs.thinkingParam.(string); ok && effort != "" {
									// "none" and "auto" don't produce budget_tokens
									lower := strings.ToLower(effort)
									if lower == "none" || lower == "auto" {
										return false, "", false
									}
									if budget, okB := util.ThinkingEffortToBudget(model, effort); okB {
										// ThinkingEffortToBudget already returns normalized budget
										return true, fmt.Sprintf("%d", budget), false
									}
									// Invalid effort - claude sets thinking.type:enabled but no budget_tokens
									return false, "", false
								}
								return false, "", false
							case "openai":
								if allowCompat {
									if effort, ok := cs.thinkingParam.(string); ok && strings.TrimSpace(effort) != "" {
										normalized := strings.ToLower(strings.TrimSpace(effort))
										return true, normalized, false
									}
									if budget, ok := cs.thinkingParam.(int); ok {
										if mapped, okM := util.ThinkingBudgetToEffort(model, budget); okM && mapped != "" {
											return true, mapped, false
										}
									}
									return false, "", false
								}
								if !supportsThinking || !usesLevels {
									return false, "", false
								}
								if effort, ok := cs.thinkingParam.(string); ok && effort != "" {
									if normalized, okN := util.NormalizeReasoningEffortLevel(model, effort); okN {
										return true, normalized, false
									}
									return false, "", true // invalid level
								}
								if budget, ok := cs.thinkingParam.(int); ok {
									if mapped, okM := util.ThinkingBudgetToEffort(model, budget); okM && mapped != "" {
										// Check if the mapped effort is valid for this model
										if _, validLevel := util.NormalizeReasoningEffortLevel(model, mapped); !validLevel {
											return true, mapped, true // expect validation error
										}
										return true, mapped, false
									}
								}
								return false, "", false
							case "codex":
								// Codex does not support allowCompat; require thinking-capable level models.
								if !supportsThinking || !usesLevels {
									return false, "", false
								}
								if effort, ok := cs.thinkingParam.(string); ok && effort != "" {
									if normalized, okN := util.NormalizeReasoningEffortLevel(model, effort); okN {
										return true, normalized, false
									}
									return false, "", true
								}
								if budget, ok := cs.thinkingParam.(int); ok {
									if mapped, okM := util.ThinkingBudgetToEffort(model, budget); okM && mapped != "" {
										// Check if the mapped effort is valid for this model
										if _, validLevel := util.NormalizeReasoningEffortLevel(model, mapped); !validLevel {
											return true, mapped, true // expect validation error
										}
										return true, mapped, false
									}
								}
								if from != "openai-response" {
									// Codex translators default reasoning.effort to "medium" for thinking-capable models
									return true, "medium", false
								}
								return false, "", false
							}
							return false, "", false
						}()

						body, err := buildBodyForProtocolWithRawThinking(t, from, to, model, cs.thinkingParam)
						actualPresent, actualValue := func() (bool, string) {
							path := ""
							switch to {
							case "gemini":
								path = "generationConfig.thinkingConfig.thinkingBudget"
							case "claude":
								path = "thinking.budget_tokens"
							case "openai":
								path = "reasoning_effort"
							case "codex":
								path = "reasoning.effort"
							}
							if path == "" {
								return false, ""
							}
							val := gjson.GetBytes(body, path)
							if to == "codex" && !val.Exists() {
								reasoning := gjson.GetBytes(body, "reasoning")
								if reasoning.Exists() {
									val = reasoning.Get("effort")
								}
							}
							if !val.Exists() {
								return false, ""
							}
							if val.Type == gjson.Number {
								return true, fmt.Sprintf("%d", val.Int())
							}
							return true, val.String()
						}()

						t.Logf("from=%s to=%s model=%s param=%v present(expect=%v got=%v) value(expect=%s got=%s) err(expect=%v got=%v) body=%s",
							from, to, model, cs.thinkingParam, expectPresent, actualPresent, expectValue, actualValue, expectErr, err != nil, string(body))

						if expectErr {
							if err == nil {
								t.Fatalf("expected validation error but got none, body=%s", string(body))
							}
							return
						}
						if err != nil {
							t.Fatalf("unexpected error: %v body=%s", err, string(body))
						}

						if expectPresent != actualPresent {
							t.Fatalf("presence mismatch: expect %v got %v body=%s", expectPresent, actualPresent, string(body))
						}
						if expectPresent && expectValue != actualValue {
							t.Fatalf("value mismatch: expect %s got %s body=%s", expectValue, actualValue, string(body))
						}
					})
				}
			}
		}
	}
}

func TestThinkingBudgetToEffort(t *testing.T) {
	cleanup := registerCoreModels(t)
	defer cleanup()

	cases := []struct {
		name   string
		model  string
		budget int
		want   string
		ok     bool
	}{
		{name: "dynamic-auto", model: "gpt-5", budget: -1, want: "auto", ok: true},
		{name: "zero-none", model: "gpt-5", budget: 0, want: "minimal", ok: true},
		{name: "low-min", model: "gpt-5", budget: 1, want: "low", ok: true},
		{name: "low-max", model: "gpt-5", budget: 1024, want: "low", ok: true},
		{name: "medium-min", model: "gpt-5", budget: 1025, want: "medium", ok: true},
		{name: "medium-max", model: "gpt-5", budget: 8192, want: "medium", ok: true},
		{name: "high-min", model: "gpt-5", budget: 8193, want: "high", ok: true},
		{name: "high-max", model: "gpt-5", budget: 24576, want: "high", ok: true},
		{name: "over-max-clamps-to-highest", model: "gpt-5", budget: 64000, want: "high", ok: true},
		{name: "over-max-xhigh-model", model: "gpt-5.2", budget: 64000, want: "xhigh", ok: true},
		{name: "negative-unsupported", model: "gpt-5", budget: -5, want: "", ok: false},
	}

	for _, cs := range cases {
		cs := cs
		t.Run(cs.name, func(t *testing.T) {
			got, ok := util.ThinkingBudgetToEffort(cs.model, cs.budget)
			if ok != cs.ok {
				t.Fatalf("ok mismatch for model=%s budget=%d: expect %v got %v", cs.model, cs.budget, cs.ok, ok)
			}
			if got != cs.want {
				t.Fatalf("value mismatch for model=%s budget=%d: expect %q got %q", cs.model, cs.budget, cs.want, got)
			}
		})
	}
}

func TestThinkingEffortToBudget(t *testing.T) {
	cleanup := registerCoreModels(t)
	defer cleanup()

	cases := []struct {
		name   string
		model  string
		effort string
		want   int
		ok     bool
	}{
		{name: "none", model: "gemini-2.5-pro", effort: "none", want: 0, ok: true},
		{name: "auto", model: "gemini-2.5-pro", effort: "auto", want: -1, ok: true},
		{name: "minimal", model: "gemini-2.5-pro", effort: "minimal", want: 512, ok: true},
		{name: "low", model: "gemini-2.5-pro", effort: "low", want: 1024, ok: true},
		{name: "medium", model: "gemini-2.5-pro", effort: "medium", want: 8192, ok: true},
		{name: "high", model: "gemini-2.5-pro", effort: "high", want: 24576, ok: true},
		{name: "xhigh", model: "gemini-2.5-pro", effort: "xhigh", want: 32768, ok: true},
		{name: "empty-unsupported", model: "gemini-2.5-pro", effort: "", want: 0, ok: false},
		{name: "invalid-unsupported", model: "gemini-2.5-pro", effort: "ultra", want: 0, ok: false},
		{name: "case-insensitive", model: "gemini-2.5-pro", effort: "LOW", want: 1024, ok: true},
		{name: "case-insensitive-medium", model: "gemini-2.5-pro", effort: "MEDIUM", want: 8192, ok: true},
	}

	for _, cs := range cases {
		cs := cs
		t.Run(cs.name, func(t *testing.T) {
			got, ok := util.ThinkingEffortToBudget(cs.model, cs.effort)
			if ok != cs.ok {
				t.Fatalf("ok mismatch for model=%s effort=%s: expect %v got %v", cs.model, cs.effort, cs.ok, ok)
			}
			if got != cs.want {
				t.Fatalf("value mismatch for model=%s effort=%s: expect %d got %d", cs.model, cs.effort, cs.want, got)
			}
		})
	}
}
