package helps_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	helps "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type editUpdatePluginHooks struct {
	summaryRemovingPluginHooks
	effort string
	calls  int
}

func (h *editUpdatePluginHooks) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, body []byte, _ bool) []byte {
	h.calls++
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

func TestRequestPairPreservesPluginConfigurationUpdateIntent(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, compat := range []bool{false, true} {
			for _, distinct := range []bool{false, true} {
				hooks := &editUpdatePluginHooks{}
				sdktranslator.SetPluginHooks(hooks)
				t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
				original := []byte(`{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"hi"}]}`)
				request := original
				wantCalls := 1
				if distinct {
					request = []byte(`{"input":[{"role":"user","content":"different"}]}`)
					wantCalls = 2
				}
				inputBefore := bytes.Clone(request)
				base, work, updatesChanged := helps.TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(t.Context(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex, "opaque-route", original, request, stream, compat)
				if hooks.calls != wantCalls {
					t.Fatalf("stream=%v compat=%v distinct=%v: plugin calls = %d, want %d", stream, compat, distinct, hooks.calls, wantCalls)
				}
				if updatesChanged != !distinct {
					t.Fatalf("stream=%v compat=%v distinct=%v: working update intent = %v, want %v", stream, compat, distinct, updatesChanged, !distinct)
				}
				if !distinct && !bytes.Equal(base, work) {
					t.Fatal("same input produced different plugin results")
				}
				baselineBefore := bytes.Clone(base)
				work[0] = 'X'
				if !bytes.Equal(base, baselineBefore) || !bytes.Equal(request, inputBefore) {
					t.Fatal("working buffer aliases the baseline or input")
				}
			}
		}
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
