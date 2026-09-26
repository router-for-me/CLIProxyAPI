package executor

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type updateNormalizingHooks struct{ effort string }

func (h *updateNormalizingHooks) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, body []byte, _ bool) []byte {
	if h.effort == "" {
		out, _ := sjson.DeleteBytes(body, "input.0")
		return out
	}
	out, _ := sjson.SetBytes(body, "input.0.reasoning.effort", h.effort)
	return out
}
func (*updateNormalizingHooks) TranslateRequest(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) ([]byte, bool) {
	return nil, false
}
func (*updateNormalizingHooks) NormalizeResponseBefore(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}
func (*updateNormalizingHooks) TranslateResponse(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
	return nil, false
}
func (*updateNormalizingHooks) NormalizeResponseAfter(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func TestXAIResponsesPluginNormalizationOverridesOldSourceUpdate(t *testing.T) {
	const source = `{"model":"grok-4.5","reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"hi"}]}`
	for _, tc := range []struct{ name, effort, want string }{
		{name: "delete update", want: "medium"},
		{name: "rewrite update", effort: "high", want: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sdktranslator.SetPluginHooks(&updateNormalizingHooks{effort: tc.effort})
			t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
			exec := NewXAIExecutor(&config.Config{})
			prepared, errPrepare := exec.prepareResponsesRequest(t.Context(), cliproxyexecutor.Request{Model: "grok-4.5", Payload: []byte(source)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, false)
			if errPrepare != nil {
				t.Fatal(errPrepare)
			}
			if got := gjson.GetBytes(prepared.body, "reasoning.effort").String(); got != tc.want {
				t.Fatalf("effort = %q, want %q; body=%s", got, tc.want, prepared.body)
			}
			if len(gjson.GetBytes(prepared.body, "input").Array()) != 1 {
				t.Fatalf("plugin-normalized update was restored: %s", prepared.body)
			}
		})
	}
}
