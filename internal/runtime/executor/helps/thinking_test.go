package helps_test

import (
	"context"
	"testing"

	"github.com/MoeForge/moego/ojson"
	helps "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type summaryRemovingPluginHooks struct {
	t *testing.T
}

func (h *summaryRemovingPluginHooks) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, body []byte, _ bool) []byte {
	h.t.Helper()
	const path = "generationConfig.thinkingConfig.includeThoughts"
	if !ojson.GetBytes(body, path).Bool() {
		h.t.Fatalf("request normalizer did not receive enabled summary: %s", body)
	}
	out, _ := ojson.DeleteBytes(body, path)
	return out
}

func (*summaryRemovingPluginHooks) TranslateRequest(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (*summaryRemovingPluginHooks) NormalizeResponseBefore(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func (*summaryRemovingPluginHooks) TranslateResponse(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (*summaryRemovingPluginHooks) NormalizeResponseAfter(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func TestApplyThinkingWithSourcePayloadPreservesNormalizerSummaryRemoval(t *testing.T) {
	hooks := &summaryRemovingPluginHooks{t: t}
	sdktranslator.SetPluginHooks(hooks)
	t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })

	source := []byte(`{"model":"gemini-3.6-flash","reasoning":{"effort":"high","summary":"auto"},"input":"hi"}`)
	translated := sdktranslator.TranslateRequest(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatGemini,
		"gemini-3.6-flash",
		source,
		false,
	)
	const summaryPath = "generationConfig.thinkingConfig.includeThoughts"
	if ojson.GetBytes(translated, summaryPath).Exists() {
		t.Fatalf("request normalizer did not remove summary: %s", translated)
	}

	out, err := helps.ApplyThinkingWithSourcePayload(
		translated,
		source,
		source,
		"gemini-3.6-flash",
		sdktranslator.FormatOpenAIResponse.String(),
		sdktranslator.FormatGemini.String(),
		"gemini",
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithSourcePayload() error = %v", err)
	}
	if ojson.GetBytes(out, summaryPath).Exists() {
		t.Fatalf("executor restored summary removed by request normalizer: %s", out)
	}
}

func TestApplyThinkingWithSourcePayloadPreservesOriginalOnlySummary(t *testing.T) {
	currentSource := []byte(`{"model":"gemini-3.6-flash","input":"hi"}`)
	originalSource := []byte(`{"model":"gemini-3.6-flash","reasoning":{"summary":null},"input":"hi"}`)
	body := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`)

	out, err := helps.ApplyThinkingWithSourcePayload(
		body,
		currentSource,
		originalSource,
		"gemini-3.6-flash",
		sdktranslator.FormatOpenAIResponse.String(),
		sdktranslator.FormatGemini.String(),
		"gemini",
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithSourcePayload() error = %v", err)
	}
	if include := ojson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || include.Bool() {
		t.Fatalf("original disabled summary was not preserved: %s", out)
	}
}

func TestApplyThinkingWithSourcePayload_AntigravityClaudeThinkingDisplayUpdates(t *testing.T) {
	source := []byte(`{"model":"gemini-3.8-flash-high","max_tokens":4096,"thinking":{"type":"adaptive","display":"updates"},"messages":[{"role":"user","content":"Hello, think first."}]}`)
	translated := sdktranslator.TranslateRequest(
		sdktranslator.FormatClaude,
		sdktranslator.FormatAntigravity,
		"gemini-3.8-flash-high",
		source,
		false,
	)

	out, err := helps.ApplyThinkingWithSourcePayload(
		translated,
		source,
		source,
		"gemini-3.8-flash-high",
		sdktranslator.FormatClaude.String(),
		sdktranslator.FormatAntigravity.String(),
		"antigravity",
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithSourcePayload() error = %v", err)
	}
	include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts")
	if !include.Exists() || !include.Bool() {
		t.Fatalf("includeThoughts not enabled: %s", out)
	}
	level := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String()
	if level != "high" {
		t.Fatalf("thinkingLevel = %q, want high: %s", level, out)
	}
}

func TestApplyThinkingWithSourcePayload_AntigravityResponsesReasoningSummaryAuto(t *testing.T) {
	source := []byte(`{"model":"gemini-3.8-flash-high","reasoning":{"effort":"low","summary":"auto"},"input":"hi"}`)
	translated := sdktranslator.TranslateRequest(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatAntigravity,
		"gemini-3.8-flash-high",
		source,
		false,
	)

	out, err := helps.ApplyThinkingWithSourcePayload(
		translated,
		source,
		source,
		"gemini-3.8-flash-high",
		sdktranslator.FormatOpenAIResponse.String(),
		sdktranslator.FormatAntigravity.String(),
		"antigravity",
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithSourcePayload() error = %v", err)
	}
	include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts")
	if !include.Exists() || !include.Bool() {
		t.Fatalf("includeThoughts not enabled: %s", out)
	}
	level := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String()
	if level != "low" {
		t.Fatalf("thinkingLevel = %q, want low: %s", level, out)
	}
}

func TestApplyRequestThinking_AntigravityClaude55PreservesThinkingLevel(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5-5-high", "claude-opus-5-5-high"} {
		for _, level := range []string{"low", "medium", "high"} {
			t.Run(model+"/"+level, func(t *testing.T) {
				source := []byte(`{"contents":[{"role":"user","parts":[{"text":"Reply OK"}]}],"generationConfig":{"maxOutputTokens":128000,"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"` + level + `"}}}`)
				translated := sdktranslator.TranslateRequest(
					sdktranslator.FormatGemini,
					sdktranslator.FormatAntigravity,
					model,
					source,
					true,
				)

				out, err := helps.ApplyThinkingWithSourcePayload(
					translated,
					source,
					source,
					model,
					sdktranslator.FormatGemini.String(),
					sdktranslator.FormatAntigravity.String(),
					"antigravity",
				)
				if err != nil {
					t.Fatalf("ApplyThinkingWithSourcePayload() error = %v", err)
				}
				if got := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != level {
					t.Fatalf("thinkingLevel = %q, want %q: %s", got, level, out)
				}
				if budget := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget"); budget.Exists() {
					t.Fatalf("thinkingBudget should be absent, got %s: %s", budget.Raw, out)
				}
				if include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
					t.Fatalf("includeThoughts = %s, want true: %s", include.Raw, out)
				}
			})
		}
	}
}
