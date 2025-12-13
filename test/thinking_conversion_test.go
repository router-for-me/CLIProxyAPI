package test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// statusErr mirrors executor.statusErr to keep validation behavior aligned.
type statusErr struct {
	code int
	msg  string
}

func (e statusErr) Error() string { return e.msg }

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
	return func() {
		reg.UnregisterClient(uid + "-gemini")
		reg.UnregisterClient(uid + "-claude")
		reg.UnregisterClient(uid + "-openai")
		reg.UnregisterClient(uid + "-qwen")
	}
}

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

// applyThinkingMetadataLocal mirrors executor.applyThinkingMetadata.
func applyThinkingMetadataLocal(payload []byte, metadata map[string]any, model string) []byte {
	budgetOverride, includeOverride, ok := util.ResolveThinkingConfigFromMetadata(model, metadata)
	if !ok || (budgetOverride == nil && includeOverride == nil) {
		return payload
	}
	if !util.ModelSupportsThinking(model) {
		return payload
	}
	if budgetOverride != nil {
		norm := util.NormalizeThinkingBudget(model, *budgetOverride)
		budgetOverride = &norm
	}
	return util.ApplyGeminiThinkingConfig(payload, budgetOverride, includeOverride)
}

// applyReasoningEffortMetadataLocal mirrors executor.applyReasoningEffortMetadata.
func applyReasoningEffortMetadataLocal(payload []byte, metadata map[string]any, model, field string) []byte {
	if len(metadata) == 0 {
		return payload
	}
	if !util.ModelSupportsThinking(model) {
		return payload
	}
	if field == "" {
		return payload
	}
	if effort, ok := util.ReasoningEffortFromMetadata(metadata); ok && effort != "" {
		if util.ModelUsesThinkingLevels(model) {
			if updated, err := sjson.SetBytes(payload, field, effort); err == nil {
				return updated
			}
		}
	}
	if util.ModelUsesThinkingLevels(model) {
		if budget, _, _, matched := util.ThinkingFromMetadata(metadata); matched && budget != nil {
			if effort, ok := util.OpenAIThinkingBudgetToEffort(model, *budget); ok && effort != "" {
				if updated, err := sjson.SetBytes(payload, field, effort); err == nil {
					return updated
				}
			}
		}
	}
	return payload
}

// normalizeThinkingConfigLocal mirrors executor.normalizeThinkingConfig.
func normalizeThinkingConfigLocal(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}

	if !util.ModelSupportsThinking(model) {
		return stripThinkingFieldsLocal(payload, false)
	}

	if util.ModelUsesThinkingLevels(model) {
		return normalizeReasoningEffortLevelLocal(payload, model)
	}

	// Model supports thinking but uses numeric budgets, not levels.
	// Strip effort string fields since they are not applicable.
	return stripThinkingFieldsLocal(payload, true)
}

// stripThinkingFieldsLocal mirrors executor.stripThinkingFields.
func stripThinkingFieldsLocal(payload []byte, effortOnly bool) []byte {
	fieldsToRemove := []string{
		"reasoning_effort",
		"reasoning.effort",
	}
	if !effortOnly {
		fieldsToRemove = append([]string{"reasoning"}, fieldsToRemove...)
	}
	out := payload
	for _, field := range fieldsToRemove {
		if gjson.GetBytes(out, field).Exists() {
			out, _ = sjson.DeleteBytes(out, field)
		}
	}
	return out
}

// normalizeReasoningEffortLevelLocal mirrors executor.normalizeReasoningEffortLevel.
func normalizeReasoningEffortLevelLocal(payload []byte, model string) []byte {
	out := payload

	if effort := gjson.GetBytes(out, "reasoning_effort"); effort.Exists() {
		if normalized, ok := util.NormalizeReasoningEffortLevel(model, effort.String()); ok {
			out, _ = sjson.SetBytes(out, "reasoning_effort", normalized)
		}
	}

	if effort := gjson.GetBytes(out, "reasoning.effort"); effort.Exists() {
		if normalized, ok := util.NormalizeReasoningEffortLevel(model, effort.String()); ok {
			out, _ = sjson.SetBytes(out, "reasoning.effort", normalized)
		}
	}

	return out
}

// validateThinkingConfigLocal mirrors executor.validateThinkingConfig.
func validateThinkingConfigLocal(payload []byte, model string) error {
	if len(payload) == 0 || model == "" {
		return nil
	}
	if !util.ModelSupportsThinking(model) || !util.ModelUsesThinkingLevels(model) {
		return nil
	}

	levels := util.GetModelThinkingLevels(model)
	checkField := func(path string) error {
		if effort := gjson.GetBytes(payload, path); effort.Exists() {
			if _, ok := util.NormalizeReasoningEffortLevel(model, effort.String()); !ok {
				return statusErr{
					code: http.StatusBadRequest,
					msg:  fmt.Sprintf("unsupported reasoning effort level %q for model %s (supported: %s)", effort.String(), model, strings.Join(levels, ", ")),
				}
			}
		}
		return nil
	}

	if err := checkField("reasoning_effort"); err != nil {
		return err
	}
	if err := checkField("reasoning.effort"); err != nil {
		return err
	}
	return nil
}

// normalizeCodexPayload mirrors codex_executor's reasoning + streaming tweaks.
func normalizeCodexPayload(body []byte, upstreamModel string) ([]byte, error) {
	body = normalizeThinkingConfigLocal(body, upstreamModel)
	if err := validateThinkingConfigLocal(body, upstreamModel); err != nil {
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
	switch toProtocol {
	case "gemini":
		body = applyThinkingMetadataLocal(body, metadata, normalizedModel)
		body = util.ApplyDefaultThinkingIfNeeded(normalizedModel, body)
		body = util.NormalizeGeminiThinkingBudget(normalizedModel, body)
		body = util.StripThinkingConfigIfUnsupported(normalizedModel, body)
	case "claude":
		if budget, ok := util.ResolveClaudeThinkingConfig(normalizedModel, metadata); ok {
			body = util.ApplyClaudeThinkingConfig(body, budget)
		}
	case "openai":
		body = applyReasoningEffortMetadataLocal(body, metadata, normalizedModel, "reasoning_effort")
		body = normalizeThinkingConfigLocal(body, upstreamModel)
		err = validateThinkingConfigLocal(body, upstreamModel)
	case "codex": // OpenAI responses / codex
		body = applyReasoningEffortMetadataLocal(body, metadata, normalizedModel, "reasoning.effort")
		// Mirror CodexExecutor final normalization and model override so tests log the final body.
		body, err = normalizeCodexPayload(body, upstreamModel)
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

	models := []string{
		"gpt-5",             // supports levels (low/medium/high)
		"gemini-2.5-pro",    // supports numeric budget
		"qwen3-coder-flash", // no thinking support
	}
	fromProtocols := []string{"openai", "claude", "gemini", "openai-response"}
	toProtocols := []string{"gemini", "claude", "openai", "codex"}

	type scenario struct {
		name        string
		modelSuffix string
		expectFn    func(info *registry.ModelInfo) (present bool, budget int64)
	}

	buildBudgetFn := func(raw int) func(info *registry.ModelInfo) (bool, int64) {
		return func(info *registry.ModelInfo) (bool, int64) {
			if info == nil || info.Thinking == nil {
				return false, 0
			}
			return true, int64(util.NormalizeThinkingBudget(info.ID, raw))
		}
	}

	levelBudgetFn := func(level string) func(info *registry.ModelInfo) (bool, int64) {
		return func(info *registry.ModelInfo) (bool, int64) {
			if info == nil || info.Thinking == nil {
				return false, 0
			}
			if b, ok := util.ThinkingEffortToBudget(info.ID, level); ok {
				return true, int64(b)
			}
			return false, 0
		}
	}

	for _, model := range models {
		info := registry.GetGlobalRegistry().GetModelInfo(model)
		min, max := 0, 0
		if info != nil && info.Thinking != nil {
			min = info.Thinking.Min
			max = info.Thinking.Max
		}

		for _, from := range fromProtocols {
			// Scenario selection follows protocol semantics:
			// - OpenAI-style protocols (openai/openai-response) express thinking as levels.
			// - Claude/Gemini-style protocols express thinking as numeric budgets.
			cases := []scenario{
				{name: "no-suffix", modelSuffix: model, expectFn: func(_ *registry.ModelInfo) (bool, int64) { return false, 0 }},
			}
			if from == "openai" || from == "openai-response" {
				cases = append(cases,
					scenario{name: "level-low", modelSuffix: fmt.Sprintf("%s(low)", model), expectFn: levelBudgetFn("low")},
					scenario{name: "level-high", modelSuffix: fmt.Sprintf("%s(high)", model), expectFn: levelBudgetFn("high")},
					scenario{name: "level-auto", modelSuffix: fmt.Sprintf("%s(auto)", model), expectFn: levelBudgetFn("auto")},
				)
			} else { // claude or gemini
				if util.ModelUsesThinkingLevels(model) {
					// Numeric budgets for level-based models are mapped into levels when needed.
					cases = append(cases,
						scenario{name: "numeric-0", modelSuffix: fmt.Sprintf("%s(0)", model), expectFn: buildBudgetFn(0)},
						scenario{name: "numeric-1024", modelSuffix: fmt.Sprintf("%s(1024)", model), expectFn: buildBudgetFn(1024)},
						scenario{name: "numeric-1025", modelSuffix: fmt.Sprintf("%s(1025)", model), expectFn: buildBudgetFn(1025)},
						scenario{name: "numeric-8192", modelSuffix: fmt.Sprintf("%s(8192)", model), expectFn: buildBudgetFn(8192)},
						scenario{name: "numeric-8193", modelSuffix: fmt.Sprintf("%s(8193)", model), expectFn: buildBudgetFn(8193)},
						scenario{name: "numeric-24576", modelSuffix: fmt.Sprintf("%s(24576)", model), expectFn: buildBudgetFn(24576)},
						scenario{name: "numeric-24577", modelSuffix: fmt.Sprintf("%s(24577)", model), expectFn: buildBudgetFn(24577)},
					)
				} else {
					cases = append(cases,
						scenario{name: "numeric-below-min", modelSuffix: fmt.Sprintf("%s(%d)", model, min-10), expectFn: buildBudgetFn(min - 10)},
						scenario{name: "numeric-above-max", modelSuffix: fmt.Sprintf("%s(%d)", model, max+10), expectFn: buildBudgetFn(max + 10)},
					)
				}
			}

			for _, to := range toProtocols {
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
								if !util.ModelSupportsThinking(normalizedModel) {
									return false, "", false
								}
								if !util.ModelUsesThinkingLevels(normalizedModel) {
									// Non-levels models don't support effort strings in openai
									return false, "", false
								}
								effort, ok := util.ReasoningEffortFromMetadata(metadata)
								if !ok || strings.TrimSpace(effort) == "" {
									if budget, _, _, matched := util.ThinkingFromMetadata(metadata); matched && budget != nil {
										if mapped, okMap := util.OpenAIThinkingBudgetToEffort(normalizedModel, *budget); okMap {
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
								if !util.ModelSupportsThinking(normalizedModel) {
									return false, "", false
								}
								if !util.ModelUsesThinkingLevels(normalizedModel) {
									// Non-levels models don't support effort strings in codex
									if from != "openai-response" {
										return false, "", false
									}
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
									if mapped, okMap := util.OpenAIThinkingBudgetToEffort(normalizedModel, *budget); okMap && mapped != "" {
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
		if budget, ok := thinkingParam.(int); ok && budget > 0 {
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
	switch toProtocol {
	case "gemini":
		body = util.ApplyDefaultThinkingIfNeeded(model, body)
		body = util.NormalizeGeminiThinkingBudget(model, body)
		body = util.StripThinkingConfigIfUnsupported(model, body)
	case "claude":
		// For raw payload, Claude thinking is passed through by translator
		// No additional processing needed as thinking is already in body
	case "openai":
		body = normalizeThinkingConfigLocal(body, model)
		err = validateThinkingConfigLocal(body, model)
	case "codex":
		body, err = normalizeCodexPayload(body, model)
	}

	body, _ = sjson.SetBytes(body, "model", model)
	body = filterThinkingBody(toProtocol, body, model, model)
	return body, err
}

func TestRawPayloadThinkingConversions(t *testing.T) {
	cleanup := registerCoreModels(t)
	defer cleanup()

	models := []string{
		"gpt-5",             // supports levels (low/medium/high)
		"gemini-2.5-pro",    // supports numeric budget
		"qwen3-coder-flash", // no thinking support
	}
	fromProtocols := []string{"openai", "claude", "gemini", "openai-response"}
	toProtocols := []string{"gemini", "claude", "openai", "codex"}

	type scenario struct {
		name          string
		thinkingParam any // int for budget, string for effort level
	}

	for _, model := range models {
		supportsThinking := util.ModelSupportsThinking(model)
		usesLevels := util.ModelUsesThinkingLevels(model)

		for _, from := range fromProtocols {
			var cases []scenario
			switch from {
			case "openai", "openai-response":
				cases = []scenario{
					{name: "no-thinking", thinkingParam: nil},
					{name: "effort-low", thinkingParam: "low"},
					{name: "effort-medium", thinkingParam: "medium"},
					{name: "effort-high", thinkingParam: "high"},
					{name: "effort-invalid-xhigh", thinkingParam: "xhigh"},
					{name: "effort-invalid-foo", thinkingParam: "foo"},
				}
			case "gemini":
				cases = []scenario{
					{name: "no-thinking", thinkingParam: nil},
					{name: "budget-1024", thinkingParam: 1024},
					{name: "budget-8192", thinkingParam: 8192},
					{name: "budget-16384", thinkingParam: 16384},
				}
			case "claude":
				cases = []scenario{
					{name: "no-thinking", thinkingParam: nil},
					{name: "budget-1024", thinkingParam: 1024},
					{name: "budget-8192", thinkingParam: 8192},
					{name: "budget-16384", thinkingParam: 16384},
				}
			}

			for _, to := range toProtocols {
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
								// No thinking param provided
								if to == "codex" && from != "openai-response" {
									// Codex translators default to medium
									if supportsThinking && usesLevels {
										return true, "medium", false
									}
								}
								return false, "", false
							}
							if !supportsThinking {
								return false, "", false
							}

							switch to {
							case "gemini":
								// Gemini expects numeric budget
								if budget, ok := cs.thinkingParam.(int); ok {
									norm := util.NormalizeThinkingBudget(model, budget)
									return true, fmt.Sprintf("%d", norm), false
								}
								if effort, ok := cs.thinkingParam.(string); ok && effort != "" {
									if b, okB := util.ThinkingEffortToBudget(model, effort); okB {
										return true, fmt.Sprintf("%d", b), false
									}
								}
								return false, "", false
							case "claude":
								// Claude expects numeric budget
								if budget, ok := cs.thinkingParam.(int); ok && budget > 0 {
									norm := util.NormalizeThinkingBudget(model, budget)
									return true, fmt.Sprintf("%d", norm), false
								}
								if effort, ok := cs.thinkingParam.(string); ok && effort != "" {
									if b, okB := util.ThinkingEffortToBudget(model, effort); okB && b > 0 {
										return true, fmt.Sprintf("%d", b), false
									}
								}
								return false, "", false
							case "openai":
								if !usesLevels {
									return false, "", false
								}
								if effort, ok := cs.thinkingParam.(string); ok && effort != "" {
									if normalized, okN := util.NormalizeReasoningEffortLevel(model, effort); okN {
										return true, normalized, false
									}
									return false, "", true // invalid level
								}
								if budget, ok := cs.thinkingParam.(int); ok {
									if mapped, okM := util.OpenAIThinkingBudgetToEffort(model, budget); okM && mapped != "" {
										return true, mapped, false
									}
								}
								return false, "", false
							case "codex":
								if !usesLevels {
									return false, "", false
								}
								if effort, ok := cs.thinkingParam.(string); ok && effort != "" {
									if normalized, okN := util.NormalizeReasoningEffortLevel(model, effort); okN {
										return true, normalized, false
									}
									return false, "", true
								}
								if budget, ok := cs.thinkingParam.(int); ok {
									if mapped, okM := util.OpenAIThinkingBudgetToEffort(model, budget); okM && mapped != "" {
										return true, mapped, false
									}
								}
								// thinkingParam was non-nil but couldn't map - no default medium
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

func TestOpenAIThinkingBudgetToEffortRanges(t *testing.T) {
	cleanup := registerCoreModels(t)
	defer cleanup()

	cases := []struct {
		name   string
		model  string
		budget int
		want   string
		ok     bool
	}{
		{name: "zero-none", model: "gpt-5", budget: 0, want: "none", ok: true},
		{name: "low-min", model: "gpt-5", budget: 1, want: "low", ok: true},
		{name: "low-max", model: "gpt-5", budget: 1024, want: "low", ok: true},
		{name: "medium-min", model: "gpt-5", budget: 1025, want: "medium", ok: true},
		{name: "medium-max", model: "gpt-5", budget: 8192, want: "medium", ok: true},
		{name: "high-min", model: "gpt-5", budget: 8193, want: "high", ok: true},
		{name: "high-max", model: "gpt-5", budget: 24576, want: "high", ok: true},
		{name: "over-max-clamps-to-highest", model: "gpt-5", budget: 24577, want: "high", ok: true},
		{name: "over-max-xhigh-model", model: "gpt-5.2", budget: 50000, want: "xhigh", ok: true},
		{name: "negative-unsupported", model: "gpt-5", budget: -5, want: "", ok: false},
	}

	for _, cs := range cases {
		cs := cs
		t.Run(cs.name, func(t *testing.T) {
			got, ok := util.OpenAIThinkingBudgetToEffort(cs.model, cs.budget)
			if ok != cs.ok {
				t.Fatalf("ok mismatch for model=%s budget=%d: expect %v got %v", cs.model, cs.budget, cs.ok, ok)
			}
			if got != cs.want {
				t.Fatalf("value mismatch for model=%s budget=%d: expect %q got %q", cs.model, cs.budget, cs.want, got)
			}
		})
	}
}
