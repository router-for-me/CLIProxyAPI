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
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"time"
)

type originalRequestedModelTransport struct {
	http.RoundTripper
	inferenceHost string
}

func (p originalRequestedModelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	inference := req.Method == http.MethodPost && req.URL.Host == p.inferenceHost && req.URL.Path == "/responses"
	usage := req.Method == http.MethodGet && req.URL.Host == "chatgpt.com" && req.URL.Path == "/backend-api/wham/usage"
	if req.URL.Scheme != "https" || req.URL.RawQuery != "" || req.URL.User != nil || (!inference && !usage) {
		return nil, fmt.Errorf("unexpected original requested-model fixture destination: %s %s://%s%s", req.Method, req.URL.Scheme, req.URL.Host, req.URL.Path)
	}
	return p.RoundTripper.RoundTrip(req)
}

func (p originalRequestedModelTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper {
	return p
}

func TestManagementV8HTTPOriginalRequestedModel(t *testing.T) {
	for _, name := range []string{"auto_foreign", "auto_suffix_foreign", "auto_matching", "auto_suffix_matching", "auto_rule_case_nonmatch"} {
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
			requested := "auto"
			if strings.Contains(name, "suffix") {
				requested = "auto(8192)"
			}
			ruleName := requested
			expectedModel, expectedToken := rewritten, "Bearer foreign-token"
			recover := strings.Contains(name, "matching") || name == "auto_rule_case_nonmatch"
			if recover {
				expectedToken = "Bearer selected-token"
			}
			if name == "auto_rule_case_nonmatch" {
				ruleName = "AUTO"
				expectedModel = route
			}
			cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: ruleName, Protocol: "codex"}}, Params: map[string]any{"model": rewritten}}}
			reg := registry.GetGlobalRegistry()
			id := t.Name()
			reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: route, Created: time.Now().Unix()}})
			reg.RegisterClient(id+"-metadata", "codex", []*registry.ModelInfo{{ID: rewritten, Created: 1, Config: &registry.ModelConfig{OverrideHeader: map[string]string{"Authorization": expectedToken}}}})
			t.Cleanup(func() { reg.UnregisterClient(id); reg.UnregisterClient(id + "-metadata") })
			type observed struct{ model, token, account string }
			captured := make(chan observed, 1)
			usageCalls := 0
			const usage = `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`
			var upstreamURL string
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.TLS != nil && r.Method == http.MethodPost && r.Host == strings.TrimPrefix(upstreamURL, "https://") && r.URL.Path == "/responses" && r.URL.RawQuery == "" {
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
				if r.TLS == nil || r.Method != http.MethodGet || r.Host != "chatgpt.com" || r.URL.Path != "/backend-api/wham/usage" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer selected-token" {
					t.Errorf("canonical selected usage identity changed: host=%s path=%s", r.Host, r.URL.Path)
					http.Error(w, "unexpected fixture request", http.StatusBadRequest)
					return
				}
				usageCalls++
				w.Header().Set("X-Usage-Evidence", "preserved")
				_, _ = io.WriteString(w, usage)
			}))
			upstreamURL = upstream.URL
			defer upstream.Close()
			transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if network != "tcp" || (addr != upstream.Listener.Addr().String() && addr != "chatgpt.com:443") {
					return nil, fmt.Errorf("unexpected original requested-model fixture dial: %s %s", network, addr)
				}
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}}
			guarded := originalRequestedModelTransport{RoundTripper: transport, inferenceHost: strings.TrimPrefix(upstream.URL, "https://")}
			previous := http.DefaultTransport
			// Management clones a concrete default transport; Clone preserves this local-only dial guard.
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(guarded))
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetRoundTripperProvider(guarded)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
			a, err := manager.Register(ctx, &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": upstream.URL}, Metadata: map[string]any{"access_token": "selected-token", "account_id": "selected-account"}})
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{"model": requested, "input": []any{map[string]any{"role": "user", "content": route}}, "metadata": map[string]any{"note": route}})
			public := gin.New()
			responses := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(effectiveSDKConfig(cfg), manager))
			public.POST("/v1/responses", responses.Responses)
			failed := httptest.NewRecorder()
			failedRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(payload))).WithContext(ctx)
			failedRequest.Header.Set("Content-Type", "application/json")
			public.ServeHTTP(failed, failedRequest)
			if failed.Code != http.StatusTooManyRequests {
				t.Fatalf("actual original-name HTTP rejection missing: status=%d body=%s", failed.Code, failed.Body.String())
			}
			select {
			case wire := <-captured:
				if wire.model != expectedModel || wire.token != expectedToken || wire.account != "selected-account" {
					t.Fatalf("original HTTP requested-name wire identity changed: %+v", wire)
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
					t.Fatal("matching or case-nonmatching original request blocked healthy usage recovery")
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("canonical usage cleared foreign original-request-name restriction")
			}
			if !t.Failed() {
				fmt.Println("OVERAGE_CASE_COMPLETED:" + name)
			}
		})
	}
}
