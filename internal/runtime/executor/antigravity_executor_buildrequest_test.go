package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	antigravityclaude "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/antigravity/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityBuildRequest_SanitizesGeminiToolSchema(t *testing.T) {
	body := buildRequestBodyFromPayload(t, "gemini-2.5-pro")

	decl := extractFirstFunctionDeclaration(t, body)
	if _, ok := decl["parametersJsonSchema"]; ok {
		t.Fatalf("parametersJsonSchema should be renamed to parameters")
	}

	params, ok := decl["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing or invalid type")
	}
	assertSchemaSanitizedAndPropertyPreserved(t, params)
}

func TestAntigravityBuildRequest_SanitizesAntigravityToolSchema(t *testing.T) {
	body := buildRequestBodyFromPayload(t, "claude-opus-4-6")

	decl := extractFirstFunctionDeclaration(t, body)
	params, ok := decl["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing or invalid type")
	}
	assertSchemaSanitizedAndPropertyPreserved(t, params)
}

func TestSanitizeAntigravityClaudeCompatFields_RemovesOutputConfig(t *testing.T) {
	input := []byte(`{
		"output_config":{"effort":"max"},
		"request":{
			"output_config":{"effort":"high"},
			"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}
		}
	}`)

	out := sanitizeAntigravityClaudeCompatFields(input)

	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("unmarshal sanitized body error: %v, body=%s", err, string(out))
	}
	if _, ok := body["output_config"]; ok {
		t.Fatalf("top-level output_config should be removed")
	}
	req, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid type")
	}
	if _, ok := req["output_config"]; ok {
		t.Fatalf("request.output_config should be removed")
	}
	if _, ok := req["generationConfig"]; !ok {
		t.Fatalf("request.generationConfig should be preserved")
	}
}

func TestClampAntigravityClaudeMaxOutputTokens_ClampsThinkingModel(t *testing.T) {
	input := []byte(`{"request":{"generationConfig":{"maxOutputTokens":128000}}}`)
	out := clampAntigravityClaudeMaxOutputTokens("claude-opus-4-6-thinking", input)
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("expected maxOutputTokens=64000, got %d", got)
	}
}

func TestApplyAntigravityClaudeCompatTransforms_ClampsAndRemovesOutputConfig(t *testing.T) {
	input := []byte(`{
		"output_config":{"effort":"max"},
		"request":{
			"output_config":{"effort":"high"},
			"generationConfig":{"maxOutputTokens":128000}
		}
	}`)

	out := applyAntigravityClaudeCompatTransforms("claude-opus-4-6-thinking", input)

	if gjson.GetBytes(out, "output_config").Exists() {
		t.Fatalf("top-level output_config should be removed")
	}
	if gjson.GetBytes(out, "request.output_config").Exists() {
		t.Fatalf("request.output_config should be removed")
	}
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("expected maxOutputTokens=64000, got %d", got)
	}
}

func TestApplyAntigravityClaudeCompatTransforms_RebalancesThinkingBudgetAfterTokenClamp(t *testing.T) {
	input := []byte(`{
		"output_config":{"effort":"max"},
		"request":{
			"generationConfig":{
				"maxOutputTokens":128000,
				"thinkingConfig":{"thinkingBudget":128000,"includeThoughts":true}
			}
		}
	}`)

	out := applyAntigravityClaudeCompatTransforms("claude-opus-4-6-thinking", input)

	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("expected maxOutputTokens=64000, got %d", got)
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 63999 {
		t.Fatalf("expected thinkingBudget=63999, got %d, body=%s", got, string(out))
	}
	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should stay true, body=%s", string(out))
	}
	if gjson.GetBytes(out, "output_config").Exists() {
		t.Fatalf("output_config should be stripped, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_RemovesThinkingConfigWhenRebalancedBudgetFallsBelowMinimum(t *testing.T) {
	input := []byte(`{
		"request":{
			"generationConfig":{
				"maxOutputTokens":1024,
				"thinkingConfig":{"thinkingBudget":2048,"includeThoughts":true}
			}
		}
	}`)

	out := applyAntigravityClaudeCompatTransforms("claude-opus-4-6-thinking", input)

	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 1024 {
		t.Fatalf("expected maxOutputTokens=1024, got %d", got)
	}
	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig").Exists() {
		t.Fatalf("thinkingConfig should be removed when adjusted budget falls below minimum, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_RebalancesThinkingBudgetWithDerivedMaxOutputTokens(t *testing.T) {
	input := []byte(`{
		"request":{
			"generationConfig":{
				"thinkingConfig":{"thinkingBudget":128000,"includeThoughts":true}
			}
		}
	}`)

	out := applyAntigravityClaudeCompatTransforms("claude-opus-4-6-thinking", input)

	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("expected maxOutputTokens=64000, got %d, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 63999 {
		t.Fatalf("expected thinkingBudget=63999, got %d, body=%s", got, string(out))
	}
	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should stay true, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_PreservesEffortThinkingLevelWithoutSynthesizingBudget(t *testing.T) {
	input := []byte(`{
		"output_config":{"effort":"max"},
		"request":{
			"generationConfig":{
				"thinkingConfig":{"thinkingLevel":"max","includeThoughts":true}
			}
		}
	}`)

	out := applyAntigravityClaudeCompatTransforms("claude-opus-4-6-thinking", input)

	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "max" {
		t.Fatalf("thinkingLevel = %q, want %q, body=%s", got, "max", string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget"); got.Exists() {
		t.Fatalf("thinkingBudget should remain absent for effort-only path, got %s, body=%s", got.Raw, string(out))
	}
	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should remain true, body=%s", string(out))
	}
	if gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Exists() {
		t.Fatalf("maxOutputTokens should remain absent on effort-only path without explicit max, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_PreservesNonClaudeLowBudget(t *testing.T) {
	input := []byte(`{
		"request":{
			"generationConfig":{
				"thinkingConfig":{"thinkingBudget":100}
			}
		}
	}`)

	out := applyAntigravityClaudeCompatTransforms("gemini-3-flash", input)

	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 100 {
		t.Fatalf("thinkingBudget = %d, want 100, body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Exists() {
		t.Fatalf("maxOutputTokens should remain absent for non-Claude compat path, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_PreservesNonClaudeOversizedBudget(t *testing.T) {
	input := []byte(`{
		"request":{
			"generationConfig":{
				"thinkingConfig":{"thinkingBudget":70000}
			}
		}
	}`)

	out := applyAntigravityClaudeCompatTransforms("gemini-3-flash", input)

	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 70000 {
		t.Fatalf("thinkingBudget = %d, want 70000, body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Exists() {
		t.Fatalf("maxOutputTokens should remain absent for non-Claude compat path, body=%s", string(out))
	}
}

func TestClampAntigravityClaudeMaxOutputTokens_PreservesWithinLimit(t *testing.T) {
	input := []byte(`{"request":{"generationConfig":{"maxOutputTokens":64000}}}`)
	out := clampAntigravityClaudeMaxOutputTokens("claude-opus-4-6-thinking", input)
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("expected maxOutputTokens=64000, got %d", got)
	}
}

func TestClampAntigravityClaudeMaxOutputTokens_PreservesNonThinkingModel(t *testing.T) {
	input := []byte(`{"request":{"generationConfig":{"maxOutputTokens":128000}}}`)
	out := clampAntigravityClaudeMaxOutputTokens("claude-opus-4-6", input)
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 128000 {
		t.Fatalf("expected maxOutputTokens=128000, got %d", got)
	}
}

func TestClampAntigravityClaudeMaxOutputTokens_NoopForNonClaude(t *testing.T) {
	input := []byte(`{"request":{"generationConfig":{"maxOutputTokens":128000}}}`)
	out := clampAntigravityClaudeMaxOutputTokens("gemini-2.5-pro", input)
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 128000 {
		t.Fatalf("expected maxOutputTokens=128000, got %d", got)
	}
}

func TestClampAntigravityClaudeMaxOutputTokens_ClampsLargeInt64Value(t *testing.T) {
	input := []byte(`{"request":{"generationConfig":{"maxOutputTokens":9223372036854775807}}}`)
	out := clampAntigravityClaudeMaxOutputTokens("claude-opus-4-6-thinking", input)
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("expected maxOutputTokens=64000, got %d", got)
	}
}

func TestPreserveClaudeEffortForAntigravity_AddsThinkingLevel(t *testing.T) {
	original := []byte(`{"output_config":{"effort":"max"}}`)
	translated := []byte(`{"request":{}}`)

	out := preserveClaudeEffortForAntigravity(sdktranslator.FromString("claude"), original, translated)

	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "max" {
		t.Fatalf("thinkingLevel = %q, want %q, body=%s", got, "max", string(out))
	}
	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should be true, body=%s", string(out))
	}
}

func TestPreserveClaudeEffortForAntigravity_AddsThinkingLevelAlongsideExistingBudget(t *testing.T) {
	original := []byte(`{"output_config":{"effort":"max"}}`)
	translated := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":2048,"includeThoughts":false}}}}`)

	out := preserveClaudeEffortForAntigravity(sdktranslator.FromString("claude"), original, translated)

	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2048 {
		t.Fatalf("thinkingBudget = %d, want 2048, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "max" {
		t.Fatalf("thinkingLevel = %q, want %q, body=%s", got, "max", string(out))
	}
}

func TestPreserveClaudeEffortForAntigravity_NoopForNonClaudeSource(t *testing.T) {
	original := []byte(`{"output_config":{"effort":"max"}}`)
	translated := []byte(`{"request":{}}`)

	out := preserveClaudeEffortForAntigravity(sdktranslator.FromString("openai"), original, translated)

	if got := string(out); got != string(translated) {
		t.Fatalf("unexpected mutation for non-claude source: got=%s want=%s", got, string(translated))
	}
}

func TestPreserveClaudeEffortForAntigravity_IgnoresNonStringOrBlankEffort(t *testing.T) {
	tests := []struct {
		name     string
		original string
	}{
		{name: "null", original: `{"output_config":{"effort":null}}`},
		{name: "number", original: `{"output_config":{"effort":123}}`},
		{name: "bool", original: `{"output_config":{"effort":false}}`},
		{name: "blank", original: `{"output_config":{"effort":"   "}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translated := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":2048,"includeThoughts":false}}}}`)

			out := preserveClaudeEffortForAntigravity(sdktranslator.FromString("claude"), []byte(tt.original), translated)

			if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel"); got.Exists() {
				t.Fatalf("thinkingLevel should not be set for %s effort, body=%s", tt.name, string(out))
			}
			if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2048 {
				t.Fatalf("thinkingBudget = %d, want 2048, body=%s", got, string(out))
			}
			if gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
				t.Fatalf("includeThoughts should remain unchanged for %s effort, body=%s", tt.name, string(out))
			}
		})
	}
}

func TestPreserveClaudeEffortForAntigravity_PreservesDisabledThinking(t *testing.T) {
	original := []byte(`{"thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`)
	translated := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":2048,"thinkingLevel":"medium","includeThoughts":true}}}}`)

	out := preserveClaudeEffortForAntigravity(sdktranslator.FromString("claude"), original, translated)

	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Fatalf("thinkingLevel should be cleared for disabled thinking, body=%s", string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 0 {
		t.Fatalf("thinkingBudget = %d, want 0, body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should be false for disabled thinking, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_StripsBridgedOutputConfig(t *testing.T) {
	original := []byte(`{"output_config":{"effort":"max"}}`)
	translated := []byte(`{"request":{}}`)

	bridged := preserveClaudeEffortForAntigravity(sdktranslator.FromString("claude"), original, translated)
	withCompat := append([]byte(`{"output_config":{"effort":"max"},`), bridged[1:]...)
	out := applyAntigravityClaudeCompatTransforms("claude-opus-4-6-thinking", withCompat)

	if gjson.GetBytes(out, "output_config").Exists() {
		t.Fatalf("output_config should be stripped, body=%s", string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "max" {
		t.Fatalf("thinkingLevel = %q, want %q, body=%s", got, "max", string(out))
	}
}

func TestPrepareAntigravityRequestPayloads_LeavesDefaultSourceUnmodifiedByThinkingTransforms(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "antigravity"}},
					Params: map[string]any{
						"generationConfig.thinkingConfig.includeThoughts": false,
						"generationConfig.thinkingConfig.thinkingBudget":  1024,
					},
				},
			},
		},
	}
	executor := &AntigravityExecutor{cfg: cfg}
	req := cliproxyexecutor.Request{
		Model:   "claude-opus-4-6-thinking",
		Payload: []byte(`{"output_config":{"effort":"max"}}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: []byte(`{"output_config":{"effort":"max"}}`),
	}

	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-stream", true: "stream"}[stream], func(t *testing.T) {
			translated, originalTranslated, err := executor.prepareAntigravityRequestPayloads(req, opts, stream)
			if err != nil {
				t.Fatalf("prepareAntigravityRequestPayloads error = %v", err)
			}

			got := applyPayloadConfigWithRoot(cfg, "claude-opus-4-6-thinking", "antigravity", "request", translated, originalTranslated, "")

			if gjson.GetBytes(originalTranslated, "request.generationConfig.thinkingConfig").Exists() {
				t.Fatalf("originalTranslated should remain the raw translated source for defaults, body=%s", string(originalTranslated))
			}
			if gotBudget := gjson.GetBytes(got, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); gotBudget != 1024 {
				t.Fatalf("thinkingBudget = %d, want %d, body=%s", gotBudget, 1024, string(got))
			}
			if gotLevel := gjson.GetBytes(got, "request.generationConfig.thinkingConfig.thinkingLevel"); gotLevel.Exists() {
				t.Fatalf("thinkingLevel should not suppress defaults when the client omitted target fields, body=%s", string(got))
			}
			if gjson.GetBytes(got, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
				t.Fatalf("includeThoughts default should apply when the client omitted it, body=%s", string(got))
			}
		})
	}
}

func TestAntigravityCountTokens_UsesSharedClaudeCompatPreparation(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body error: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens":321}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "antigravity"}},
					Params: map[string]any{
						"debug.label": "count-tokens",
					},
				},
			},
		},
	}
	executor := &AntigravityExecutor{cfg: cfg}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"expired":      time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	payload := []byte(`{
		"model":"claude-opus-4-6-thinking",
		"max_tokens":128000,
		"thinking":{"type":"adaptive"},
		"output_config":{"effort":"max"},
		"messages":[{"role":"user","content":[{"type":"text","text":"Hello!"}]}]
	}`)

	_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-6-thinking",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("CountTokens error = %v", err)
	}

	if got := gjson.GetBytes(capturedBody, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("maxOutputTokens = %d, want 64000, body=%s", got, string(capturedBody))
	}
	if got := gjson.GetBytes(capturedBody, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 63999 {
		t.Fatalf("thinkingBudget = %d, want 63999, body=%s", got, string(capturedBody))
	}
	if gjson.GetBytes(capturedBody, "output_config").Exists() {
		t.Fatalf("output_config should be stripped, body=%s", string(capturedBody))
	}
	if got := gjson.GetBytes(capturedBody, "request.debug.label").String(); got != "count-tokens" {
		t.Fatalf("request.debug.label = %q, want %q, body=%s", got, "count-tokens", string(capturedBody))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_CherryAdaptiveMaxPayload(t *testing.T) {
	original := []byte(`{
		"model":"claude-opus-4-6-thinking",
		"max_tokens":128000,
		"thinking":{"type":"adaptive"},
		"output_config":{"effort":"max"},
		"messages":[{"role":"user","content":[{"type":"text","text":"Hello!"}]}],
		"stream":true
	}`)

	baseModel := thinking.ParseSuffix("claude-opus-4-6-thinking").ModelName
	translated := antigravityclaude.ConvertClaudeRequestToAntigravity(baseModel, original, true)
	translated = preserveClaudeEffortForAntigravity(sdktranslator.FromString("claude"), original, translated)

	out, err := thinking.ApplyThinking(translated, "claude-opus-4-6-thinking", "claude", "antigravity", "antigravity")
	if err != nil {
		t.Fatalf("ApplyThinking error = %v", err)
	}

	out = applyAntigravityClaudeCompatTransforms(baseModel, out)

	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("maxOutputTokens = %d, want 64000, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 63999 {
		t.Fatalf("thinkingBudget = %d, want 63999, body=%s", got, string(out))
	}
	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should be true, body=%s", string(out))
	}
	if gjson.GetBytes(out, "output_config").Exists() {
		t.Fatalf("output_config should be removed, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_ClaudeEffortNoneDisablesThinking(t *testing.T) {
	original := []byte(`{
		"model":"claude-opus-4-6-thinking",
		"max_tokens":128000,
		"thinking":{"type":"adaptive"},
		"output_config":{"effort":"none"},
		"messages":[{"role":"user","content":[{"type":"text","text":"Hello!"}]}],
		"stream":true
	}`)

	baseModel := thinking.ParseSuffix("claude-opus-4-6-thinking").ModelName
	translated := antigravityclaude.ConvertClaudeRequestToAntigravity(baseModel, original, true)
	translated = preserveClaudeEffortForAntigravity(sdktranslator.FromString("claude"), original, translated)

	out, err := thinking.ApplyThinking(translated, "claude-opus-4-6-thinking", "claude", "antigravity", "antigravity")
	if err != nil {
		t.Fatalf("ApplyThinking error = %v", err)
	}

	out = applyAntigravityClaudeCompatTransforms(baseModel, out)

	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("maxOutputTokens = %d, want 64000, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 0 {
		t.Fatalf("thinkingBudget = %d, want 0, body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Fatalf("thinkingLevel should be cleared for disabled thinking, body=%s", string(out))
	}
	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should be false for disabled thinking, body=%s", string(out))
	}
	if gjson.GetBytes(out, "output_config").Exists() {
		t.Fatalf("output_config should be removed, body=%s", string(out))
	}
	if gjson.GetBytes(out, "request.output_config").Exists() {
		t.Fatalf("request.output_config should be removed, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_ClaudeEffortAutoPreservesAdaptiveThinking(t *testing.T) {
	original := []byte(`{
		"model":"claude-opus-4-6-thinking",
		"max_tokens":128000,
		"thinking":{"type":"adaptive"},
		"output_config":{"effort":"auto"},
		"messages":[{"role":"user","content":[{"type":"text","text":"Hello!"}]}],
		"stream":true
	}`)

	baseModel := thinking.ParseSuffix("claude-opus-4-6-thinking").ModelName
	translated := antigravityclaude.ConvertClaudeRequestToAntigravity(baseModel, original, true)
	translated = preserveClaudeEffortForAntigravity(sdktranslator.FromString("claude"), original, translated)

	out, err := thinking.ApplyThinking(translated, "claude-opus-4-6-thinking", "claude", "antigravity", "antigravity")
	if err != nil {
		t.Fatalf("ApplyThinking error = %v", err)
	}

	out = applyAntigravityClaudeCompatTransforms(baseModel, out)

	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("maxOutputTokens = %d, want 64000, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != -1 {
		t.Fatalf("thinkingBudget = %d, want -1, body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Fatalf("thinkingLevel should be cleared for adaptive auto thinking, body=%s", string(out))
	}
	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should remain true for adaptive auto thinking, body=%s", string(out))
	}
	if gjson.GetBytes(out, "output_config").Exists() {
		t.Fatalf("output_config should be removed, body=%s", string(out))
	}
	if gjson.GetBytes(out, "request.output_config").Exists() {
		t.Fatalf("request.output_config should be removed, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_DisabledThinkingOverridesClaudeEffort(t *testing.T) {
	original := []byte(`{
		"model":"claude-opus-4-6-thinking",
		"max_tokens":128000,
		"thinking":{"type":"disabled"},
		"output_config":{"effort":"high"},
		"messages":[{"role":"user","content":[{"type":"text","text":"Hello!"}]}],
		"stream":true
	}`)

	baseModel := thinking.ParseSuffix("claude-opus-4-6-thinking").ModelName
	translated := antigravityclaude.ConvertClaudeRequestToAntigravity(baseModel, original, true)
	translated = preserveClaudeEffortForAntigravity(sdktranslator.FromString("claude"), original, translated)

	out, err := thinking.ApplyThinking(translated, "claude-opus-4-6-thinking", "claude", "antigravity", "antigravity")
	if err != nil {
		t.Fatalf("ApplyThinking error = %v", err)
	}

	out = applyAntigravityClaudeCompatTransforms(baseModel, out)

	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("maxOutputTokens = %d, want 64000, body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Fatalf("thinkingLevel should be cleared when disabled thinking wins, body=%s", string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 0 {
		t.Fatalf("thinkingBudget = %d, want 0, body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should be false when disabled thinking wins, body=%s", string(out))
	}
	if gjson.GetBytes(out, "output_config").Exists() {
		t.Fatalf("output_config should be removed, body=%s", string(out))
	}
	if gjson.GetBytes(out, "request.output_config").Exists() {
		t.Fatalf("request.output_config should be removed, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_ManualBudgetPayload(t *testing.T) {
	original := []byte(`{
		"model":"claude-opus-4-6-thinking",
		"max_tokens":128000,
		"thinking":{"type":"enabled","budget_tokens":64000},
		"messages":[{"role":"user","content":[{"type":"text","text":"Hello!"}]}],
		"stream":true
	}`)

	baseModel := thinking.ParseSuffix("claude-opus-4-6-thinking").ModelName
	translated := antigravityclaude.ConvertClaudeRequestToAntigravity(baseModel, original, true)

	out, err := thinking.ApplyThinking(translated, "claude-opus-4-6-thinking", "claude", "antigravity", "antigravity")
	if err != nil {
		t.Fatalf("ApplyThinking error = %v", err)
	}

	out = applyAntigravityClaudeCompatTransforms(baseModel, out)

	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("maxOutputTokens = %d, want 64000, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 63999 {
		t.Fatalf("thinkingBudget = %d, want 63999, body=%s", got, string(out))
	}
	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should be true, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_ManualBudgetPayloadWithoutMaxTokens(t *testing.T) {
	original := []byte(`{
		"model":"claude-opus-4-6-thinking",
		"thinking":{"type":"enabled","budget_tokens":64000},
		"messages":[{"role":"user","content":[{"type":"text","text":"Hello!"}]}],
		"stream":true
	}`)

	baseModel := thinking.ParseSuffix("claude-opus-4-6-thinking").ModelName
	translated := antigravityclaude.ConvertClaudeRequestToAntigravity(baseModel, original, true)

	out, err := thinking.ApplyThinking(translated, "claude-opus-4-6-thinking", "claude", "antigravity", "antigravity")
	if err != nil {
		t.Fatalf("ApplyThinking error = %v", err)
	}

	out = applyAntigravityClaudeCompatTransforms(baseModel, out)

	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("maxOutputTokens = %d, want 64000, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 63999 {
		t.Fatalf("thinkingBudget = %d, want 63999, body=%s", got, string(out))
	}
	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should be true, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_ClaudeEffortOverridesEnabledBudget(t *testing.T) {
	original := []byte(`{
		"model":"claude-opus-4-6-thinking",
		"max_tokens":128000,
		"thinking":{"type":"enabled","budget_tokens":64000},
		"output_config":{"effort":"low"},
		"messages":[{"role":"user","content":[{"type":"text","text":"Hello!"}]}],
		"stream":true
	}`)

	baseModel := thinking.ParseSuffix("claude-opus-4-6-thinking").ModelName
	translated := antigravityclaude.ConvertClaudeRequestToAntigravity(baseModel, original, true)
	translated = preserveClaudeEffortForAntigravity(sdktranslator.FromString("claude"), original, translated)

	out, err := thinking.ApplyThinking(translated, "claude-opus-4-6-thinking", "claude", "antigravity", "antigravity")
	if err != nil {
		t.Fatalf("ApplyThinking error = %v", err)
	}

	out = applyAntigravityClaudeCompatTransforms(baseModel, out)

	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 1024 {
		t.Fatalf("thinkingBudget = %d, want 1024, body=%s", got, string(out))
	}
	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should remain true, body=%s", string(out))
	}
}

func TestApplyAntigravityClaudeCompatTransforms_NonStringEffortFallsBackToBudget(t *testing.T) {
	tests := []struct {
		name   string
		effort string
	}{
		{name: "null", effort: "null"},
		{name: "number", effort: "123"},
		{name: "bool", effort: "false"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := []byte(`{
				"model":"claude-opus-4-6-thinking",
				"max_tokens":128000,
				"thinking":{"type":"enabled","budget_tokens":64000},
				"output_config":{"effort":` + tt.effort + `},
				"messages":[{"role":"user","content":[{"type":"text","text":"Hello!"}]}],
				"stream":true
			}`)

			baseModel := thinking.ParseSuffix("claude-opus-4-6-thinking").ModelName
			translated := antigravityclaude.ConvertClaudeRequestToAntigravity(baseModel, original, true)
			translated = preserveClaudeEffortForAntigravity(sdktranslator.FromString("claude"), original, translated)

			out, err := thinking.ApplyThinking(translated, "claude-opus-4-6-thinking", "claude", "antigravity", "antigravity")
			if err != nil {
				t.Fatalf("ApplyThinking error = %v", err)
			}

			out = applyAntigravityClaudeCompatTransforms(baseModel, out)

			if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 63999 {
				t.Fatalf("thinkingBudget = %d, want 63999, body=%s", got, string(out))
			}
			if gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
				t.Fatalf("thinkingLevel should be omitted when non-string effort falls back to budget, body=%s", string(out))
			}
			if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
				t.Fatalf("includeThoughts should remain true for budget fallback, body=%s", string(out))
			}
		})
	}
}

func buildRequestBodyFromPayload(t *testing.T, modelName string) map[string]any {
	t.Helper()

	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{}
	payload := []byte(`{
		"request": {
			"tools": [
				{
					"function_declarations": [
						{
							"name": "tool_1",
							"parametersJsonSchema": {
								"$schema": "http://json-schema.org/draft-07/schema#",
								"$id": "root-schema",
								"type": "object",
								"properties": {
									"$id": {"type": "string"},
									"arg": {
										"type": "object",
										"prefill": "hello",
										"properties": {
											"mode": {
												"type": "string",
												"deprecated": true,
												"enum": ["a", "b"],
												"enumTitles": ["A", "B"]
											}
										}
									}
								},
								"patternProperties": {
									"^x-": {"type": "string"}
								}
							}
						}
					]
				}
			]
		}
	}`)

	req, err := executor.buildRequest(context.Background(), auth, "token", modelName, payload, false, "", "https://example.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}

	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal request body error: %v, body=%s", err, string(raw))
	}
	return body
}

func extractFirstFunctionDeclaration(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	request, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid type")
	}
	tools, ok := request["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools missing or empty")
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("first tool invalid type")
	}
	decls, ok := tool["function_declarations"].([]any)
	if !ok || len(decls) == 0 {
		t.Fatalf("function_declarations missing or empty")
	}
	decl, ok := decls[0].(map[string]any)
	if !ok {
		t.Fatalf("first function declaration invalid type")
	}
	return decl
}

func assertSchemaSanitizedAndPropertyPreserved(t *testing.T, params map[string]any) {
	t.Helper()

	if _, ok := params["$id"]; ok {
		t.Fatalf("root $id should be removed from schema")
	}
	if _, ok := params["patternProperties"]; ok {
		t.Fatalf("patternProperties should be removed from schema")
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing or invalid type")
	}
	if _, ok := props["$id"]; !ok {
		t.Fatalf("property named $id should be preserved")
	}

	arg, ok := props["arg"].(map[string]any)
	if !ok {
		t.Fatalf("arg property missing or invalid type")
	}
	if _, ok := arg["prefill"]; ok {
		t.Fatalf("prefill should be removed from nested schema")
	}

	argProps, ok := arg["properties"].(map[string]any)
	if !ok {
		t.Fatalf("arg.properties missing or invalid type")
	}
	mode, ok := argProps["mode"].(map[string]any)
	if !ok {
		t.Fatalf("mode property missing or invalid type")
	}
	if _, ok := mode["enumTitles"]; ok {
		t.Fatalf("enumTitles should be removed from nested schema")
	}
	if _, ok := mode["deprecated"]; ok {
		t.Fatalf("deprecated should be removed from nested schema")
	}
}
