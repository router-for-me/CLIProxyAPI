package auth_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Exercise the supported SDK mixed credential through actual inference and the
// canonical v8 usage probe. Healthy usage for A cannot release a failure from B.
func TestQuotaRecoveryEffectiveInferenceToken(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, tc := range []struct {
			name, key         string
			recover           bool
			siwc, codexClient bool
		}{
			{name: "ordinary_oauth", recover: true},
			{name: "matching_api_key", key: "outer-token", recover: true},
			{name: "different_api_key", key: "other-token"},
			{name: "siwc_same_account", siwc: true, recover: true},
			{name: "siwc_matching_api_key", siwc: true, key: "outer-token", recover: true},
			{name: "siwc_codex_client_different_api_key", siwc: true, codexClient: true, key: "other-token"},
		} {
			if provider == "claude" && tc.siwc {
				continue
			}
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				cfg := &config.Config{}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				inferenceCalls, usageCalls := 0, 0
				wantToken := "outer-token"
				if tc.key != "" {
					wantToken = tc.key
				}
				if tc.siwc && !tc.codexClient {
					wantToken = "siwc-token"
				}
				inference := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					inferenceCalls++
					wantBearer, wantKey := "Bearer "+wantToken, ""
					if r.Header.Get("Authorization") != wantBearer || r.Header.Get("X-Api-Key") != wantKey {
						t.Error("inference did not use effective selected token")
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(429)
					if provider == "codex" {
						_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"subscription limit","resets_in_seconds":3600}}`)
					} else {
						_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"weekly usage limit reached"}}`)
					}
				}))
				defer inference.Close()
				usagePath, usageURL, usageBody := "/backend-api/wham/usage", "https://chatgpt.com/backend-api/wham/usage", `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":1}}}`
				if provider == "claude" {
					usagePath = "/api/oauth/usage"
					usageURL = "https://api.anthropic.com/api/oauth/usage"
					usageBody = `{"five_hour":{"utilization":0},"seven_day":{"utilization":1}}`
				}
				usage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					usageCalls++
					if r.URL.Path != usagePath || r.Header.Get("Authorization") != "Bearer outer-token" || r.Header.Get("X-Api-Key") != "" {
						t.Error("canonical usage did not inspect outer token")
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, usageBody)
				}))
				defer usage.Close()
				previous := http.DefaultTransport
				transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					if address != inference.Listener.Addr().String() {
						address = usage.Listener.Addr().String()
					}
					return (&net.Dialer{}).DialContext(ctx, network, address)
				}}
				http.DefaultTransport = transport
				defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
				input := &coreauth.Auth{ID: t.Name(), Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": inference.URL, "api_key": tc.key}, Metadata: map[string]any{"access_token": "outer-token"}}
				usageHeaders := map[string]string{"Authorization": "Bearer $TOKEN$"}
				if tc.siwc {
					input.Metadata["account_id"] = "outer-account"
					input.Metadata["use_siwc"] = true
					input.Metadata["siwc"] = map[string]any{"access_token": "siwc-token", "id_token": "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"outer-account"}}`)) + ".sig"}
					usageHeaders["Chatgpt-Account-Id"] = "outer-account"
				}
				auth, err := manager.Register(ctx, input)
				if err != nil {
					t.Fatal(err)
				}
				model := "effective-token-model"
				payload := []byte(`{"model":"effective-token-model","input":[]}`)
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				if provider == "codex" {
					manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
				} else {
					model = "claude-sonnet-4-5"
					payload = []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"test"}]}`)
					opts.SourceFormat = sdktranslator.FormatClaude
					manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
				}
				if tc.codexClient {
					ctx = coreauth.WithCodexClientRequest(ctx, true)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
				defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
				_, err = manager.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: model, Payload: payload}, opts)
				if err == nil || inferenceCalls != 1 {
					t.Fatalf("actual inference did not establish subscription failure: calls=%d err=%v", inferenceCalls, err)
				}
				failed, _ := manager.GetByID(auth.ID)
				if !failed.Quota.Exceeded || !failed.Unavailable || failed.LastError == nil || failed.LastError.HTTPStatus != 429 {
					t.Fatal("actual inference did not establish quota cooldown")
				}
				if got := coreauth.QuotaRecoveryCandidate(failed); got != tc.recover {
					t.Errorf("candidate trusted unobserved effective inference token: got=%t want=%t", got, tc.recover)
				}
				doc := ojson.NewDocString(`{}`)
				for key, value := range map[string]any{"authIndex": failed.EnsureIndex(), "method": "GET", "url": usageURL, "header": usageHeaders} {
					if err := doc.Set(key, value); err != nil {
						t.Fatal(err)
					}
				}
				router := gin.New()
				router.POST("/v8/management/requests/api-call", management.NewHandler(cfg, "", manager).APICallV8)
				request := httptest.NewRequest(http.MethodPost, "/v8/management/requests/api-call", strings.NewReader(doc.String()))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != 200 || usageCalls != 1 {
					t.Fatalf("canonical usage failed: status=%d calls=%d body=%s", response.Code, usageCalls, response.Body.String())
				}
				after, _ := manager.GetByID(auth.ID)
				if tc.recover {
					if after.Quota.Exceeded || after.Unavailable {
						t.Fatal("matching effective inference token did not recover")
					}
				} else if !after.Quota.Exceeded || !after.Unavailable || after.LastError == nil || !after.NextRetryAfter.Equal(failed.NextRetryAfter) {
					t.Fatal("outer usage cleared another effective inference token's cooldown")
				}
			})
		}
	}
}
