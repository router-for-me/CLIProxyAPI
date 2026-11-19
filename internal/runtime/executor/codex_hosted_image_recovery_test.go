package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexHostedImageRecoveryIdentity(t *testing.T) {
	for _, tc := range []struct {
		name              string
		headers           map[string]string
		configured, route string
		disabled, recover bool
	}{
		{name: "foreign_token", headers: map[string]string{"Authorization": "Bearer token-b"}},
		{name: "foreign_account", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
		{name: "matching", headers: map[string]string{"Authorization": "Bearer token-a", "Chatgpt-Account-Id": "account-a"}, recover: true},
		{name: "unrelated", headers: map[string]string{"X-Hosted": "yes"}, recover: true},
		{name: "default_foreign", configured: "default", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
		{name: "invalid_foreign", configured: "not-gpt", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
		{name: "lite_foreign", configured: "gpt-6-sol", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
		{name: "disabled_sdk_foreign", disabled: true, headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
		{name: "direct_ignores_base", route: "gpt-image-2", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}, recover: true},
		{name: "selection_restore_foreign", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
		{name: "ordinary_sdk_foreign", route: "gpt-5.5", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
	} {
		for _, stream := range []bool{false, true} {
			mode := "execute"
			if stream {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				t.Setenv("DISABLE_QUOTA_COOLDOWN", "false")
				id := t.Name()
				route := tc.route
				if route == "" {
					route = "legacy-" + strings.ReplaceAll(id, "/", "-")
				}
				base := "gpt-hosted-" + strings.ReplaceAll(id, "/", "-")
				configured := base
				if tc.configured != "" {
					configured = tc.configured
					if configured == "default" {
						configured = ""
					}
					base = "gpt-5.4-mini"
				}
				cfg := &config.Config{SDKConfig: config.SDKConfig{GPTImage2BaseModel: configured}}
				if tc.disabled {
					cfg.DisableImageGeneration = config.DisableImageGenerationAll
				}
				type wire struct{ path, model, token, account string }
				captured := make(chan wire, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					captured <- wire{r.URL.Path, ojson.GetBytes(body, "model").String(), r.Header.Get("Authorization"), r.Header.Get("Chatgpt-Account-Id")}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`)
				}))
				defer server.Close()
				reg := registry.GetGlobalRegistry()
				globalID := id + "-catalog"
				reg.RegisterClient(globalID, "codex", []*registry.ModelInfo{{ID: base, Type: "codex", Config: &registry.ModelConfig{OverrideHeader: tc.headers}}})
				defer reg.UnregisterClient(globalID)
				routeType := registry.OpenAIImageModelType
				if tc.name == "ordinary_sdk_foreign" {
					routeType = "codex"
				}
				reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: route, Type: routeType}})
				defer reg.UnregisterClient(id)
				manager := cliproxyauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 1)
				manager.RegisterExecutor(NewCodexExecutor(cfg))
				if tc.name == "selection_restore_foreign" {
					manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{"codex": {{Name: "gpt-image-2", Alias: route, Fork: true}}})
				}
				auth := &cliproxyauth.Auth{ID: id, Provider: "codex", Status: cliproxyauth.StatusActive, Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "token-a", "account_id": "account-a"}}
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
				doc := ojson.NewDocString(`{"prompt":"a tree"}`)
				if err := doc.Set("model", route); err != nil {
					t.Fatal(err)
				}
				req := cliproxyexecutor.Request{Model: route, Payload: doc.Bytes()}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image"), Stream: stream, Metadata: map[string]any{cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations"}}
				if tc.name == "selection_restore_foreign" {
					req.Model = "gpt-original-" + strings.ReplaceAll(id, "/", "-")
					opts.Metadata[cliproxyexecutor.AuthSelectionModelMetadataKey] = route
				}
				var err error
				if stream {
					_, err = manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
				} else {
					_, err = manager.Execute(context.Background(), []string{"codex"}, req, opts)
				}
				if err == nil {
					t.Fatal("actual image quota rejection missing")
				}
				var got wire
				select {
				case got = <-captured:
				default:
					t.Fatalf("actual hosted image request missing: %v", err)
				}
				want := wire{"/responses", base, "Bearer token-a", "account-a"}
				if value, ok := tc.headers["Authorization"]; ok {
					want.token = value
				}
				if value, ok := tc.headers["Chatgpt-Account-Id"]; ok {
					want.account = value
				}
				if tc.route == "gpt-image-2" {
					want = wire{"/images/generations", route, "Bearer token-a", "account-a"}
				}
				if got != want {
					t.Fatalf("actual image wire identity=%+v want=%+v", got, want)
				}
				before, ok := manager.GetByID(id)
				if !ok || !cliproxyauth.QuotaRecoveryCandidate(before) {
					t.Fatalf("actual image rejection did not establish quota: %+v", before)
				}
				// The caller has verified canonical account-a usage capacity. The manager
				// must still refuse a reset whose image builder can select another identity.
				recovered, _, err := manager.ResetQuotaIfUnchanged(context.Background(), before)
				if err != nil {
					t.Fatal(err)
				}
				after, _ := manager.GetByID(id)
				if tc.recover {
					if recovered == nil || cliproxyauth.QuotaRecoveryCandidate(after) {
						t.Fatal("matching image identity did not recover")
					}
				} else if recovered != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("outer usage cleared foreign hosted image cooldown")
				}
			})
		}
	}
}
