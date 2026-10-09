package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestV8SharedUpstreamRequestSettingsAffectBothAuthKinds(t *testing.T) {
	for _, provider := range []string{"codex", "xai"} {
		for _, kind := range []string{cliproxyauth.AuthKindAPIKey, cliproxyauth.AuthKindOAuth} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", provider, kind, stream), func(t *testing.T) {
					captured := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
						}
						captured <- body
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":{"message":"captured request"}}`))
					}))
					defer server.Close()
					cfg, err := config.ParseConfigBytes([]byte(`proxy-url: direct
disable-image-generation: true
client: {codex: {optimize-multi-agent-v2: true}}
upstream:
  codex: {orphan-delegation-compatibility: true}
  xai: {inject-x-search: true}
`))
					if err != nil {
						t.Fatal(err)
					}
					auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Status: cliproxyauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"auth_kind": kind, "base_url": server.URL}}
					if kind == cliproxyauth.AuthKindAPIKey {
						auth.Attributes["api_key"] = "test-key"
					} else {
						auth.Metadata = map[string]any{"access_token": "test-token"}
					}
					var executor cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
					if provider == "xai" {
						executor = NewXAIExecutor(cfg)
					}
					manager := cliproxyauth.NewManager(nil, nil, nil)
					manager.SetConfig(cfg)
					manager.RegisterExecutor(executor)
					if _, err = manager.Register(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: "gpt-5.4"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{
  "input":[{"type":"function_call_output","name":"create_thread","namespace":"codex_app","output":"<codex_delegation>task</codex_delegation>"}],
  "tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}}}}]}]
}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: http.Header{"User-Agent": {"codex_cli_rs/0.144.1"}, "X-Openai-Subagent": {"collab_spawn"}}}
					if stream {
						_, err = manager.ExecuteStream(context.Background(), []string{provider}, req, opts)
					} else {
						_, err = manager.Execute(context.Background(), []string{provider}, req, opts)
					}
					if err == nil {
						t.Fatal("expected the test upstream's rejection")
					}
					var body []byte
					select {
					case body = <-captured:
					default:
						t.Fatalf("request did not reach upstream: %v", err)
					}
					if got := gjson.GetBytes(body, "input.0.type").String(); got != "message" {
						t.Fatalf("shared orphan delegation type = %q; body=%s", got, body)
					}
					if provider == "codex" {
						if got := gjson.GetBytes(body, "tools.0.name").String(); got != "collaboration-optimize" {
							t.Fatalf("client collaboration namespace = %q, want collaboration-optimize", got)
						}
					} else if !gjson.GetBytes(body, `tools.#(type=="x_search")`).Exists() {
						t.Fatal("shared x_search setting was not applied")
					}
					if !cfg.Client.Codex.OptimizeMultiAgentV2 || !cfg.Codex.OrphanDelegationCompatibility || !cfg.XAI.InjectXSearch {
						t.Fatal("request changed shared configuration")
					}
				})
			}
		}
	}
}

func TestV8ScopedExecutorsPreserveWebsocketStores(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("upstream: {codex: {response-steering: true}, xai: {inject-x-search: true}}\noauth: {providers: {codex: {header-defaults: {user-agent: oauth-agent}}}}"))
	if err != nil {
		t.Fatal(err)
	}
	codex := NewCodexAutoExecutor(cfg)
	boundCodex := codex.ForAPIKey().(*CodexAutoExecutor)
	if boundCodex.wsExec.store != codex.wsExec.store || !boundCodex.httpExec.cfg.Codex.ResponseSteering || !boundCodex.wsExec.cfg.Codex.ResponseSteering || boundCodex.httpExec.cfg.CodexHeaderDefaults.UserAgent != "" {
		t.Fatal("scoped Codex executor lost sessions or inherited OAuth settings")
	}
	xai := NewXAIAutoExecutor(cfg)
	boundXAI := xai.ForAPIKey().(*XAIAutoExecutor)
	if boundXAI.wsExec.store != xai.wsExec.store || boundXAI.wsExec.idStore != xai.wsExec.idStore || !boundXAI.httpExec.cfg.XAI.InjectXSearch || !boundXAI.wsExec.cfg.XAI.InjectXSearch || boundXAI.httpExec.cfg.CodexHeaderDefaults.UserAgent != "" {
		t.Fatal("scoped xAI executor lost sessions or inherited OAuth settings")
	}
	if codex.httpExec.cfg != cfg || codex.wsExec.cfg != cfg || xai.httpExec.cfg != cfg || xai.wsExec.cfg != cfg || !cfg.Codex.ResponseSteering || !cfg.XAI.InjectXSearch {
		t.Fatal("scoping changed the registered executors")
	}
}
