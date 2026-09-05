package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// Exercise the actual outgoing request: translation must retain service_tier
// until conditional routing has run, then the configured filter removes it.
func TestOpenAICompatServiceTierRouting(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tier := range []string{"", "default", "priority"} {
			t.Run(fmt.Sprintf("stream=%t/tier=%s", stream, tier), func(t *testing.T) {
				bodies := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					bodies <- body
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
					}
				}))
				t.Cleanup(server.Close)
				modelRule := config.PayloadModelRule{Name: "openrouter-test-exacto", Protocol: "openai"}
				priorityRule := modelRule
				priorityRule.Match = []map[string]any{{"service_tier": "priority"}}
				cfg := &config.Config{Payload: config.PayloadConfig{
					Override: []config.PayloadRule{
						{Models: []config.PayloadModelRule{modelRule}, Params: map[string]any{
							"provider.only": []string{"reviewed"}, "provider.quantizations": []string{"fp8", "unknown"},
						}},
						{Models: []config.PayloadModelRule{priorityRule}, Params: map[string]any{
							"model": "vendor/test:nitro", "provider.only": []string{"reviewed/fast"},
						}},
					},
					Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{modelRule}, Params: []string{"service_tier"}}},
				}}
				executor := NewOpenAICompatExecutor("openai-compatibility", cfg)
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL}}
				tierField := ""
				if tier != "" {
					tierField = fmt.Sprintf(`,"service_tier":%q`, tier)
				}
				request := cliproxyexecutor.Request{
					Model:   "vendor/test:exacto",
					Payload: []byte(fmt.Sprintf(`{"model":"openrouter-test-exacto","reasoning":{"effort":"high"},"input":[{"role":"user","content":"hi"}]%s}`, tierField)),
				}
				options := cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatOpenAIResponse,
					ResponseFormat: sdktranslator.FormatOpenAI,
					Metadata:       map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "openrouter-test-exacto"},
				}
				if stream {
					result, err := executor.ExecuteStream(context.Background(), auth, request, options)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := executor.Execute(context.Background(), auth, request, options); err != nil {
					t.Fatal(err)
				}
				body := <-bodies
				wantModel, wantProvider := "vendor/test:exacto", "reviewed"
				if tier == "priority" {
					wantModel, wantProvider = "vendor/test:nitro", "reviewed/fast"
				}
				for path, want := range map[string]string{"model": wantModel, "provider.only.0": wantProvider, "reasoning_effort": "high", "provider.quantizations.0": "fp8"} {
					if got := gjson.GetBytes(body, path).String(); got != want {
						t.Errorf("%s = %q, want %q; body=%s", path, got, want, body)
					}
				}
				if gjson.GetBytes(body, "service_tier").Exists() {
					t.Errorf("service_tier leaked to upstream: %s", body)
				}
			})
		}
	}
}
