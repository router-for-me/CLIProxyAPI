package executor

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeAndKimiPayloadCacheOptionsAreFinal(t *testing.T) {
	for _, provider := range []string{"claude", "kimi"} {
		for _, operation := range []string{"execute", "stream", "count"} {
			t.Run(provider+"/"+operation, func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
					}
					if ojson.GetBytes(body, "prompt_cache_options.mode").String() != "explicit" {
						t.Errorf("payload-injected cache options removed: %s", body)
					}
					if ojson.GetBytes(body, "betas").Exists() || !strings.Contains(r.Header.Get("Anthropic-Beta"), "final-beta") || strings.Contains(r.Header.Get("Anthropic-Beta"), "obsolete-beta") {
						t.Errorf("beta headers must use the final payload: body=%s beta=%s", body, r.Header.Get("Anthropic-Beta"))
					}
					if bytes.Contains(body, []byte("cache_control")) {
						t.Errorf("payload-injected explicit mode must not rerun cache placement: %s", body)
					}
					// Explicitly injected fields are the caller's responsibility; no silent cleanup retry.
					http.Error(w, `{"error":{"type":"invalid_request_error","message":"unsupported cache option"}}`, http.StatusBadRequest)
				}))
				defer server.Close()
				cfg := &config.Config{Payload: config.PayloadConfig{
					Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*", NotExist: []string{"prompt_cache_options"}}}, Params: map[string]any{"prompt_cache_options.mode": "explicit", "betas": []string{"final-beta"}}}},
					Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"messages.*.content.*.cache_control", "system.*.cache_control", "cache_control"}}},
				}}
				var executor cliproxyauth.ProviderExecutor = NewClaudeExecutor(cfg)
				if provider == "kimi" {
					executor = NewKimiExecutor(cfg)
				}
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: "claude-sonnet-4-5", Payload: []byte(`{"model":"claude-sonnet-4-5","betas":["obsolete-beta"],"prompt_cache_options":{"mode":"explicit"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
				before := helps.PayloadConfigEvaluationCount()
				var errExecute error
				switch operation {
				case "execute":
					_, errExecute = executor.Execute(t.Context(), auth, req, opts)
				case "stream":
					_, errExecute = executor.ExecuteStream(t.Context(), auth, req, opts)
				case "count":
					if provider == "claude" {
						_, errExecute = executor.(*ClaudeExecutor).countTokensUpstream(t.Context(), auth, req, opts)
					} else {
						_, errExecute = executor.CountTokens(t.Context(), auth, req, opts)
					}
				}
				if errExecute == nil || requests != 1 || helps.PayloadConfigEvaluationCount()-before != 1 {
					t.Fatalf("expected one failed configured request, got error=%v requests=%d evaluations=%d", errExecute, requests, helps.PayloadConfigEvaluationCount()-before)
				}
			})
		}
	}
}
