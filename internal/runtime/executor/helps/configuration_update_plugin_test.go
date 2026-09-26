package helps_test

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	helps "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type editUpdatePluginHooks struct {
	summaryRemovingPluginHooks
	effort string
}

func (h *editUpdatePluginHooks) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, body []byte, _ bool) []byte {
	if h.effort == "" {
		out, _ := sjson.DeleteBytes(body, "input.0")
		return out
	}
	out, _ := sjson.SetBytes(body, "input.0.reasoning.effort", h.effort)
	return out
}

func TestPluginConfigurationUpdateNormalizationIsAuthoritative(t *testing.T) {
	const source = `{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"hi"}]}`
	for _, tc := range []struct{ name, edit, want string }{
		{name: "delete", want: "medium"},
		{name: "rewrite", edit: "high", want: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sdktranslator.SetPluginHooks(&editUpdatePluginHooks{effort: tc.edit})
			t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
			translated := sdktranslator.TranslateRequestEnvelope(t.Context(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex, sdktranslator.RequestEnvelope{Model: "opaque-route", Body: []byte(source)})
			if !translated.ConfigurationUpdatesChanged {
				t.Fatal("plugin update edit not reported by translator")
			}
			req := cliproxyexecutor.Request{Model: "opaque-route", Payload: []byte(source), Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: "opaque-route", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}}}}
			body, errApply := helps.ApplyRequestThinking(translated.Body, req, cliproxyexecutor.Options{}, "openai-response", "codex", "codex", translated.ConfigurationUpdatesChanged)
			if errApply != nil {
				t.Fatal(errApply)
			}
			if got := gjson.GetBytes(body, "reasoning.effort").String(); got != tc.want {
				t.Fatalf("effort = %q, want %q; body=%s", got, tc.want, body)
			}
			if len(gjson.GetBytes(body, "input").Array()) != 1 {
				t.Fatalf("unexpected input: %s", body)
			}
		})
	}
}

func TestNativeCrossProtocolTranslationWithoutPluginRetainsSourceUpdate(t *testing.T) {
	const source = `{"model":"private-chat","reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"user","content":"hi"}]}`
	translated := sdktranslator.TranslateRequestEnvelope(t.Context(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, sdktranslator.RequestEnvelope{Model: "private-chat", Body: []byte(source)})
	if translated.ConfigurationUpdatesChanged {
		t.Fatal("native translation was incorrectly reported as a plugin edit")
	}
	req := cliproxyexecutor.Request{Model: "private-chat", Payload: []byte(source), Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: "private-chat", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}}}}
	body, errApply := helps.ApplyRequestThinking(translated.Body, req, cliproxyexecutor.Options{}, "openai-response", "openai", "openai", translated.ConfigurationUpdatesChanged)
	if errApply != nil {
		t.Fatal(errApply)
	}
	if got := gjson.GetBytes(body, "reasoning_effort").String(); got != "high" {
		t.Fatalf("cross-protocol effort = %q, want high; body=%s", got, body)
	}
}
