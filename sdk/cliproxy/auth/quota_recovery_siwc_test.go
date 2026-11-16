package auth_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestQuotaRecoverySIWCPartitionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, account            string
		bound, disabled, recover bool
		outerUnknown             bool
		headers                  map[string]string
		dynamicAccount           bool
	}{
		{name: "plain", recover: true},
		{name: "disabled_partition", bound: true, disabled: true, recover: true},
		{name: "unknown_partition", bound: true},
		{name: "different_account", bound: true, account: "other-account"},
		{name: "known_same_account", bound: true, account: "outer-account", recover: true},
		{name: "unproven_outer_account", bound: true, outerUnknown: true},
		{name: "account_header_override", bound: true, account: "outer-account", headers: map[string]string{"Chatgpt-Account-Id": "other-account"}},
		{name: "bearer_header_override", bound: true, account: "outer-account", headers: map[string]string{"Authorization": "Bearer other-token"}},
		{name: "dynamic_account_header", bound: true, account: "outer-account", headers: map[string]string{"Chatgpt-Account-Id": "$X-Selected-Account"}, dynamicAccount: true},
		{name: "matching_identity_headers", bound: true, account: "outer-account", headers: map[string]string{"Chatgpt-Account-Id": "outer-account", "Authorization": "Bearer siwc-token"}, recover: true},
		{name: "unrelated_header", bound: true, account: "outer-account", headers: map[string]string{"X-Test": "preserved"}, recover: true},
		{name: "ordinary_account_header", headers: map[string]string{"Chatgpt-Account-Id": "other-account"}},
		{name: "ordinary_bearer_header", headers: map[string]string{"Authorization": "Bearer other-token"}},
		{name: "ordinary_dynamic_header", headers: map[string]string{"Chatgpt-Account-Id": "$X-Selected-Account"}, dynamicAccount: true},
		{name: "ordinary_matching_headers", headers: map[string]string{"Authorization": "Bearer outer-token", "Chatgpt-Account-Id": "outer-account"}, recover: true},
		{name: "ordinary_unrelated_header", headers: map[string]string{"X-Test": "preserved"}, recover: true},
		{name: "disabled_account_header", bound: true, disabled: true, headers: map[string]string{"Chatgpt-Account-Id": "other-account"}},
		{name: "disabled_bearer_header", bound: true, disabled: true, headers: map[string]string{"Authorization": "Bearer other-token"}},
		{name: "disabled_dynamic_header", bound: true, disabled: true, headers: map[string]string{"Chatgpt-Account-Id": "$X-Selected-Account"}, dynamicAccount: true},
		{name: "disabled_matching_headers", bound: true, disabled: true, headers: map[string]string{"Authorization": "Bearer outer-token", "Chatgpt-Account-Id": "outer-account"}, recover: true},
		{name: "disabled_unrelated_header", bound: true, disabled: true, headers: map[string]string{"X-Test": "preserved"}, recover: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "codex.json")
			outerAccount := "outer-account"
			if tc.outerUnknown {
				outerAccount = ""
			}
			credential := ojson.NewDocString(`{"type":"codex","access_token":"outer-token","refresh_token":"outer-refresh"}`)
			if err := credential.Set("account_id", outerAccount); err != nil {
				t.Fatal(err)
			}
			if len(tc.headers) > 0 {
				if err := credential.Set("headers", tc.headers); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, credential.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{AuthDir: dir}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			var inferenceCalls, usageCalls int
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/responses":
					inferenceCalls++
					want := "outer-token"
					if tc.bound && !tc.disabled {
						want = "siwc-token"
					}
					if bearer := tc.headers["Authorization"]; bearer != "" {
						want = strings.TrimPrefix(bearer, "Bearer ")
					}
					if r.Header.Get("Authorization") != "Bearer "+want {
						t.Errorf("inference bearer did not select expected partition")
					}
					wantAccount := outerAccount
					if tc.bound && !tc.disabled {
						wantAccount = ""
					}
					if account := tc.headers["Chatgpt-Account-Id"]; account != "" {
						wantAccount = account
					}
					if tc.dynamicAccount {
						wantAccount = "other-account"
					}
					if r.Header.Get("Chatgpt-Account-Id") != wantAccount {
						t.Errorf("inference did not apply expected account selector")
					}
					if tc.headers["X-Test"] != "" && r.Header.Get("X-Test") != tc.headers["X-Test"] {
						t.Errorf("inference did not preserve unrelated configured header")
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"subscription limit","resets_in_seconds":3600}}`)
				case "/backend-api/wham/usage":
					usageCalls++
					if r.Header.Get("Authorization") != "Bearer outer-token" || r.Header.Get("Chatgpt-Account-Id") != outerAccount {
						t.Errorf("canonical usage did not inspect outer account")
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":1}}}`)
				default:
					t.Errorf("unexpected provider path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer upstream.Close()
			previous := http.DefaultTransport
			transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}}
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
			inputAuth := &coreauth.Auth{ID: "siwc-recovery", FileName: "codex.json", Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"type": "codex", "access_token": "outer-token", "refresh_token": "outer-refresh", "account_id": outerAccount}, Attributes: map[string]string{"path": path, "base_url": upstream.URL}}
			if len(tc.headers) > 0 {
				inputAuth.Metadata["headers"] = tc.headers
				coreauth.ApplyCustomHeadersFromMetadata(inputAuth)
			}
			auth, err := manager.Register(ctx, inputAuth)
			if err != nil {
				t.Fatal(err)
			}
			if tc.bound {
				tokens := &codexauth.CodexSIWCTokenData{AccessToken: "siwc-token", RefreshToken: "siwc-refresh", ClientID: "siwc-client"}
				if tc.account != "" || tc.outerUnknown {
					tokens.IDToken = "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"`+tc.account+`"}}`)) + ".sig"
				}
				auth, err = manager.BindCodexSIWC(ctx, auth.ID, auth.RegistrationEpoch, outerAccount, tokens)
				if err != nil {
					t.Fatal(err)
				}
				if tc.disabled {
					auth.Metadata["use_siwc"] = false
					auth, err = manager.Update(ctx, auth)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			const model = "siwc-recovery-model"
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			if tc.dynamicAccount {
				opts.Headers = http.Header{"X-Selected-Account": []string{"other-account"}}
			}
			_, err = manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"siwc-recovery-model","input":[]}`)}, opts)
			if err == nil || inferenceCalls != 1 {
				t.Fatalf("inference failure was not recorded through executor: calls=%d err=%v", inferenceCalls, err)
			}
			failed, _ := manager.GetByID(auth.ID)
			if !failed.Quota.Exceeded || !failed.Unavailable || failed.LastError == nil || failed.LastError.HTTPStatus != 429 {
				t.Fatalf("actual subscription failure did not establish cooldown: %+v", failed)
			}
			if got := coreauth.QuotaRecoveryCandidate(failed); got != tc.recover {
				t.Errorf("candidate accepted unrelated partition: got=%t want=%t", got, tc.recover)
			}
			doc := ojson.NewDocString(`{}`)
			usageHeaders := map[string]string{"Authorization": "Bearer $TOKEN$"}
			if outerAccount != "" {
				usageHeaders["Chatgpt-Account-Id"] = outerAccount
			}
			for key, value := range map[string]any{"authIndex": failed.EnsureIndex(), "method": "GET", "url": "https://chatgpt.com/backend-api/wham/usage", "header": usageHeaders} {
				if err := doc.Set(key, value); err != nil {
					t.Fatal(err)
				}
			}
			handler := management.NewHandler(cfg, "", manager)
			router := gin.New()
			router.POST("/v8/management/requests/api-call", handler.APICallV8)
			request := httptest.NewRequest(http.MethodPost, "/v8/management/requests/api-call", strings.NewReader(doc.String()))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK || usageCalls != 1 {
				t.Fatalf("canonical usage forwarding failed: status=%d calls=%d body=%s", response.Code, usageCalls, response.Body.String())
			}
			after, _ := manager.GetByID(auth.ID)
			if tc.recover {
				if after.Quota.Exceeded || after.Unavailable {
					t.Fatal("matching account did not recover after confirmed capacity")
				}
			} else if !after.Quota.Exceeded || !after.Unavailable || after.LastError == nil || !after.NextRetryAfter.Equal(failed.NextRetryAfter) {
				t.Fatal("outer account usage cleared another partition's cooldown")
			}
		})
	}
}

func TestQuotaRecoveryClaudeIdentityHeaders(t *testing.T) {
	for _, tc := range []struct {
		name             string
		headers          map[string]string
		recover, dynamic bool
	}{
		{name: "ordinary", recover: true},
		{name: "different_bearer", headers: map[string]string{"Authorization": "Bearer other-token"}},
		{name: "dynamic_bearer", headers: map[string]string{"Authorization": "$X-Identity"}, dynamic: true},
		{name: "competing_api_key", headers: map[string]string{"x-api-key": "other-key"}},
		{name: "dynamic_api_key", headers: map[string]string{"x-api-key": "$X-Identity"}, dynamic: true},
		{name: "matching_bearer", headers: map[string]string{"Authorization": "Bearer outer-token"}, recover: true},
		{name: "unrelated_header", headers: map[string]string{"X-Test": "preserved"}, recover: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := &config.Config{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			inferenceCalls, usageCalls := 0, 0
			inference := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				inferenceCalls++
				wantBearer := "Bearer outer-token"
				if v := tc.headers["Authorization"]; v != "" {
					wantBearer = v
				}
				if tc.name == "dynamic_bearer" {
					wantBearer = "Bearer other-token"
				}
				wantKey := tc.headers["x-api-key"]
				if tc.name == "dynamic_api_key" {
					wantKey = "other-key"
				}
				if r.Header.Get("Authorization") != wantBearer || r.Header.Get("x-api-key") != wantKey {
					t.Errorf("executor did not apply configured identity headers: bearer=%q key=%q", r.Header.Get("Authorization"), r.Header.Get("x-api-key"))
				}
				if tc.headers["X-Test"] != "" && r.Header.Get("X-Test") != "preserved" {
					t.Error("executor lost unrelated header")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(429)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"weekly usage limit reached"}}`)
			}))
			defer inference.Close()
			usage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				usageCalls++
				if r.URL.Path != "/api/oauth/usage" || r.Header.Get("Authorization") != "Bearer outer-token" || r.Header.Get("x-api-key") != "" {
					t.Error("canonical probe changed selected outer identity")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"five_hour":{"utilization":0},"seven_day":{"utilization":1}}`)
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
			input := &coreauth.Auth{ID: "claude-identity", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": inference.URL}, Metadata: map[string]any{"access_token": "outer-token", "headers": tc.headers}}
			coreauth.ApplyCustomHeadersFromMetadata(input)
			auth, err := manager.Register(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			const model = "claude-sonnet-4-5"
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			if tc.dynamic {
				identity := "Bearer other-token"
				if tc.name == "dynamic_api_key" {
					identity = "other-key"
				}
				opts.Headers = http.Header{"X-Identity": []string{identity}}
			}
			_, err = manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"test"}]}`)}, opts)
			if err == nil || inferenceCalls != 1 {
				t.Fatalf("actual executor did not record subscription failure: calls=%d err=%v", inferenceCalls, err)
			}
			failed, _ := manager.GetByID(auth.ID)
			if !failed.Quota.Exceeded || !failed.Unavailable || failed.LastError == nil || failed.LastError.HTTPStatus != 429 {
				t.Fatalf("failure did not establish cooldown: %+v", failed)
			}
			if got := coreauth.QuotaRecoveryCandidate(failed); got != tc.recover {
				t.Errorf("candidate trusted unobserved inference identity: got=%t want=%t", got, tc.recover)
			}
			doc := ojson.NewDocString(`{}`)
			for key, value := range map[string]any{"authIndex": failed.EnsureIndex(), "method": "GET", "url": "https://api.anthropic.com/api/oauth/usage", "header": map[string]string{"Authorization": "Bearer $TOKEN$"}} {
				if err := doc.Set(key, value); err != nil {
					t.Fatal(err)
				}
			}
			handler := management.NewHandler(cfg, "", manager)
			router := gin.New()
			router.POST("/v8/management/requests/api-call", handler.APICallV8)
			request := httptest.NewRequest(http.MethodPost, "/v8/management/requests/api-call", strings.NewReader(doc.String()))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != 200 || usageCalls != 1 {
				t.Fatalf("canonical forwarding failed: status=%d calls=%d body=%s", response.Code, usageCalls, response.Body.String())
			}
			after, _ := manager.GetByID(auth.ID)
			if tc.recover {
				if after.Quota.Exceeded || after.Unavailable {
					t.Fatal("matching identity failed to recover")
				}
			} else if !after.Quota.Exceeded || !after.Unavailable || after.LastError == nil || !after.NextRetryAfter.Equal(failed.NextRetryAfter) {
				t.Fatal("outer usage cleared another inference identity's cooldown")
			}
		})
	}
}
