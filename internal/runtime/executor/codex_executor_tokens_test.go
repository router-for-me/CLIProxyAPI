package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type codexTokenSchemaHooks struct {
	body []byte
}

func (h *codexTokenSchemaHooks) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, body []byte, _ bool) []byte {
	h.body = bytes.Clone(body)
	return body
}

func (*codexTokenSchemaHooks) TranslateRequest(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (*codexTokenSchemaHooks) NormalizeResponseBefore(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func (*codexTokenSchemaHooks) TranslateResponse(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (*codexTokenSchemaHooks) NormalizeResponseAfter(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func TestCodexExecutorCountTokensPreservesToolNumberSchemas(t *testing.T) {
	for _, source := range []struct {
		format  sdktranslator.Format
		payload string
	}{
		{format: sdktranslator.FormatOpenAIResponse, payload: `{"input":"hi","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"yield_time_ms":{"type":"number"}}}}]}`},
		{format: sdktranslator.FormatClaude, payload: `{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"exec_command","input_schema":{"type":"object","properties":{"yield_time_ms":{"type":"number"}}}}]}`},
	} {
		for _, compat := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/compat=%t", source.format, compat), func(t *testing.T) {
				hooks := &codexTokenSchemaHooks{}
				sdktranslator.SetPluginHooks(hooks)
				t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
				exec := NewCodexExecutor(&config.Config{})
				resp, errCount := exec.CountTokens(t.Context(), nil, cliproxyexecutor.Request{
					Model:   "gpt-5.4",
					Payload: []byte(source.payload),
					Metadata: map[string]any{
						"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: "gpt-5.4", IsCompat: compat},
					},
				}, cliproxyexecutor.Options{
					SourceFormat: source.format,
					Headers:      http.Header{"User-Agent": []string{"codex_cli_rs/0.1"}},
				})
				if errCount != nil {
					t.Fatalf("CountTokens() error = %v", errCount)
				}
				// Token counts alone can miss a schema rewrite: number and integer
				// may have the same token length. Inspect the translated schema too.
				if got := gjson.GetBytes(hooks.body, "tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
					t.Fatalf("yield_time_ms.type = %q, want number; body=%s", got, hooks.body)
				}
				if len(resp.Payload) == 0 {
					t.Fatal("CountTokens() returned an empty response")
				}
			})
		}
	}
}
