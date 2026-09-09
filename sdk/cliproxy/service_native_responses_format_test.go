package cliproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNativeResponsesAfterAuthInterceptorUsesSelectedConfig(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		for _, operation := range []string{"execute", "stream", "count"} {
			stream := operation == "stream"
			t.Run(fmt.Sprintf("wrapped=%t/%s", wrapped, operation), func(t *testing.T) {
				const provider, model = "selected-format-probe", "gpt-4o"
				var upstreamPath string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamPath = r.URL.Path
					body, _ := io.ReadAll(r.Body)
					if r.URL.Path == "/responses" && !gjson.GetBytes(body, "input").Exists() {
						t.Errorf("Responses input was lost: %s", body)
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						if r.URL.Path == "/responses" {
							_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
						} else {
							_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						}
					} else if r.URL.Path == "/responses" {
						_, _ = io.WriteString(w, `{"status":"completed","output":[]}`)
					} else {
						_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
					}
				}))
				defer server.Close()
				cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
					{Name: provider, BaseURL: server.URL},
					{Name: provider, BaseURL: server.URL, WireAPI: "responses"},
				}}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				var executor coreauth.ProviderExecutor = runtimeexecutor.NewOpenAICompatExecutor(provider, cfg)
				if wrapped {
					executor = pluginhost.NewPluginRefreshCompatExecutor(executor, nil, cfg)
				}
				manager.RegisterExecutor(executor)
				for index, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
					auth := &coreauth.Auth{ID: fmt.Sprintf("%s/%d", t.Name(), index), Provider: provider, Attributes: map[string]string{
						"base_url": server.URL, "api_key": "test-key", "source": "config", "config_index": strconv.Itoa(index),
					}}
					if _, err := manager.Register(t.Context(), auth); err != nil {
						t.Fatal(err)
					}
					reg := registry.GetGlobalRegistry()
					reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
					payload := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
					wantPath := "/chat/completions"
					if format == sdktranslator.FormatOpenAIResponse {
						payload = []byte(`{"model":"gpt-4o","input":"hi"}`)
						wantPath = "/responses"
					}
					var reported sdktranslator.Format
					var calls int
					opts := cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: payload, Stream: stream,
						Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: auth.ID},
						RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
							reported = req.ToFormat
							calls++
							return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
						},
					}
					req := cliproxyexecutor.Request{Model: model, Payload: payload}
					var err error
					if stream {
						result, executeErr := manager.ExecuteStream(t.Context(), []string{provider}, req, opts)
						err = executeErr
						if result != nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									err = chunk.Err
								}
							}
						}
					} else if operation == "count" {
						wantPath = ""
						_, err = manager.ExecuteCount(t.Context(), []string{provider}, req, opts)
					} else {
						_, err = manager.Execute(t.Context(), []string{provider}, req, opts)
					}
					if err != nil || calls != 1 || reported != format || upstreamPath != wantPath {
						t.Errorf("config index %d: error=%v, calls=%d, ToFormat=%q, path=%q; want format=%q, path=%q", index, err, calls, reported, upstreamPath, format, wantPath)
					}
				}
			})
		}
	}
}
