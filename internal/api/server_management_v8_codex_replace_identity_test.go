package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestManagementV8CodexReplaceIdentity(t *testing.T) {
	for _, name := range []string{"literal_foreign", "regex_foreign", "computed_foreign", "wildcard_foreign", "model_path_matching_headers_conservative", "non_model_path", "role_text", "other_model", "other_protocol", "invalid_regex"} {
		t.Run(name, func(t *testing.T) {
			if child := os.Getenv("CPA_OVERAGE_CHILD_CASE"); child == "" {
				runIsolatedOverageCase(t, name)
				return
			} else if child != name {
				t.Fatal("OVERAGE_CHILD_INFRASTRUCTURE: wrong replace identity case")
			}
			route, rewritten := "replace-route-"+name, "replace-final-"+name
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("port: 8317\nremote-management: {secret-key: test-password}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg.DisableImageGeneration = config.DisableImageGenerationAll
			rule := config.PayloadReplaceRule{Models: []config.PayloadModelRule{{Name: route, Protocol: "codex"}}, Target: config.PayloadReplaceTarget{Paths: []string{"model"}}, Replacements: []config.PayloadReplacement{{route: rewritten}}}
			expectedModel, expectedToken := rewritten, "Bearer foreign-token"
			recover := false
			switch name {
			case "invalid_regex":
				rule.Replacements = []config.PayloadReplacement{{"pattern": "[", "replacement": rewritten}}
				expectedModel, expectedToken, recover = route, "Bearer selected-token", true
			case "regex_foreign":
				rule.Replacements = []config.PayloadReplacement{{"pattern": "^" + route + "$", "replacement": rewritten}}
			case "computed_foreign":
				rule.Target.Paths = []string{"@this.model"}
			case "wildcard_foreign":
				rule.Target.Paths = []string{"mo*"}
			case "model_path_matching_headers_conservative":
				expectedToken = "Bearer selected-token"
			case "non_model_path":
				rule.Target.Paths = []string{"metadata.note"}
				expectedModel, expectedToken, recover = route, "Bearer selected-token", true
			case "role_text":
				rule.Target = config.PayloadReplaceTarget{Role: "user", All: true}
				expectedModel, expectedToken, recover = route, "Bearer selected-token", true
			case "other_model":
				rule.Models[0].Name = "unrelated-route"
				expectedModel, expectedToken, recover = route, "Bearer selected-token", true
			case "other_protocol":
				rule.Models[0].Protocol = "claude"
				expectedModel, expectedToken, recover = route, "Bearer selected-token", true
			}
			cfg.Payload.Replace = []config.PayloadReplaceRule{rule}
			reg := registry.GetGlobalRegistry()
			id := t.Name()
			reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: route}})
			reg.RegisterClient(id+"-metadata", "codex", []*registry.ModelInfo{{ID: rewritten, Config: &registry.ModelConfig{OverrideHeader: map[string]string{"Authorization": expectedToken}}}})
			t.Cleanup(func() { reg.UnregisterClient(id); reg.UnregisterClient(id + "-metadata") })
			type observed struct{ model, token, account string }
			captured := make(chan observed, 1)
			usageCalls := 0
			const usage = `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/responses") {
					body, e := io.ReadAll(r.Body)
					if e != nil {
						t.Error(e)
					}
					captured <- observed{ojson.GetBytes(body, "model").String(), r.Header.Get("Authorization"), r.Header.Get("Chatgpt-Account-Id")}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", "3600")
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"plan limit","resets_in_seconds":3600}}`)
					return
				}
				if r.Host != "chatgpt.com" || r.URL.Path != "/backend-api/wham/usage" || r.Header.Get("Authorization") != "Bearer selected-token" {
					t.Errorf("canonical selected usage identity changed: host=%s path=%s", r.Host, r.URL.Path)
				}
				usageCalls++
				w.Header().Set("X-Usage-Evidence", "preserved")
				_, _ = io.WriteString(w, usage)
			}))
			defer upstream.Close()
			transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}}
			previous := http.DefaultTransport
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
			a, err := manager.Register(ctx, &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "selected-token", "account_id": "selected-account"}})
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{"model": route, "input": []any{map[string]any{"role": "user", "content": route}}, "metadata": map[string]any{"note": route}})
			_, err = manager.Execute(ctx, []string{"codex"}, coreexecutor.Request{Model: route, Payload: payload}, coreexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
			if err == nil {
				t.Fatal("actual final executor rejection missing")
			}
			select {
			case wire := <-captured:
				if wire.model != expectedModel || wire.token != expectedToken || wire.account != "selected-account" {
					t.Fatalf("final replace wire identity changed: %+v", wire)
				}
			default:
				t.Fatal("actual Codex wire request missing")
			}
			before, _ := manager.GetByID(id)
			if !coreauth.QuotaRecoveryCandidate(before) {
				t.Fatal("actual rejection did not produce recoverable quota candidate")
			}
			h := management.NewHandler(cfg, path, manager)
			h.SetLocalPassword("test-password")
			s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
			s.managementRoutesEnabled.Store(true)
			s.registerManagementRoutes()
			body, _ := json.Marshal(map[string]any{"authIndex": a.EnsureIndex(), "method": "GET", "url": "https://chatgpt.com/backend-api/wham/usage", "header": map[string]string{"Authorization": "Bearer $TOKEN$", "Chatgpt-Account-Id": "selected-account"}})
			req := httptest.NewRequest(http.MethodPost, "/v8/management/requests/api-call", strings.NewReader(string(body))).WithContext(ctx)
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set("Authorization", "Bearer test-password")
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			s.engine.ServeHTTP(response, req)
			if response.Code != 200 || usageCalls != 1 {
				t.Fatalf("canonical v8 usage failed: status=%d calls=%d", response.Code, usageCalls)
			}
			var wrapped struct {
				Status int         `json:"status_code"`
				Body   string      `json:"body"`
				Header http.Header `json:"header"`
			}
			if err = json.Unmarshal(response.Body.Bytes(), &wrapped); err != nil || wrapped.Status != 200 || wrapped.Body != usage || wrapped.Header.Get("X-Usage-Evidence") != "preserved" {
				t.Fatal("canonical usage response changed")
			}
			after, _ := manager.GetByID(id)
			if recover {
				if after.Unavailable || after.Quota.Exceeded {
					t.Fatal("non-model or nonmatching replace rule blocked healthy usage recovery")
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("canonical usage cleared quota despite model-capable replacement identity")
			}
			if !t.Failed() {
				fmt.Println("OVERAGE_CASE_COMPLETED:" + name)
			}
		})
	}
}
