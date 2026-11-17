package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexDirectImageRecoveryNormalizedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, route string
		headers     map[string]string
		recover     bool
	}{
		{name: "foreign_account", route: "codex/gpt-image-1.5(high)", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
		{name: "foreign_bearer", route: "codex/gpt-image-1.5(high)", headers: map[string]string{"Authorization": "Bearer token-b"}},
		{name: "case_normalization", route: "CODEX/GPT-IMAGE-1.5(high)", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
		{name: "explicit_mapping_matching", route: "codex/gpt-image-1.5(high)", headers: map[string]string{"Authorization": "Bearer token-a", "Chatgpt-Account-Id": "account-a"}, recover: true},
		{name: "explicit_mapping_unrelated", route: "codex/gpt-image-1.5(high)", headers: map[string]string{"X-Image-Identity": "unchanged"}, recover: true},
		{name: "explicit_mapping_sdk_restore", route: "codex/gpt-image-1.5(high)", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
		{name: "matching_identity", route: "codex/gpt-image-1.5(high)", headers: map[string]string{"Authorization": "Bearer token-a", "Chatgpt-Account-Id": "account-a"}, recover: true},
		{name: "unrelated_headers", route: "codex/gpt-image-1.5(high)", headers: map[string]string{"X-Image-Identity": "unchanged"}, recover: true},
	} {
		for _, stream := range []bool{false, true} {
			mode := "execute"
			if stream {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				t.Setenv("DISABLE_QUOTA_COOLDOWN", "false")
				id := t.Name()
				cfg := &config.Config{}
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
				if tc.name == "explicit_mapping_matching" || tc.name == "explicit_mapping_unrelated" || tc.name == "explicit_mapping_sdk_restore" {
					cfg.CodexKey = []config.CodexKey{{BaseURL: server.URL, Models: []config.CodexModel{{Name: "codex/gpt-image-1.5"}}}}
				}
				reg := registry.GetGlobalRegistry()
				catalog := id + "-catalog"
				reg.RegisterClient(catalog, "codex", []*registry.ModelInfo{{ID: "gpt-image-1.5", Type: registry.OpenAIImageModelType, Config: &registry.ModelConfig{OverrideHeader: tc.headers}}})
				defer reg.UnregisterClient(catalog)
				// The selected auth exposes only the prefixed SDK route. Its normalized
				// header-selection model is globally known but not registered on this auth.
				reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: tc.route, Type: registry.OpenAIImageModelType}})
				defer reg.UnregisterClient(id)
				manager := cliproxyauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 1)
				manager.RegisterExecutor(NewCodexExecutor(cfg))
				auth := &cliproxyauth.Auth{ID: id, Provider: "codex", Status: cliproxyauth.StatusActive, Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "token-a", "account_id": "account-a"}}
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
				doc := ojson.NewDocString(`{"prompt":"a tree"}`)
				if err := doc.Set("model", tc.route); err != nil {
					t.Fatal(err)
				}
				req := cliproxyexecutor.Request{Model: tc.route, Payload: doc.Bytes()}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image"), Stream: stream, Metadata: map[string]any{cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations"}}
				if tc.name == "explicit_mapping_sdk_restore" {
					req.Model = "gpt-image-1.5"
					opts.Metadata[cliproxyexecutor.AuthSelectionModelMetadataKey] = tc.route
				}
				var err error
				if stream {
					_, err = manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
				} else {
					_, err = manager.Execute(context.Background(), []string{"codex"}, req, opts)
				}
				if err == nil {
					t.Fatal("actual direct image quota rejection missing")
				}
				var got wire
				select {
				case got = <-captured:
				default:
					t.Fatalf("actual normalized direct image request missing: %v", err)
				}
				want := wire{"/images/generations", "gpt-image-1.5", "Bearer token-a", "account-a"}
				if tc.name == "explicit_mapping_matching" || tc.name == "explicit_mapping_unrelated" {
					want.model = "codex/gpt-image-1.5"
				}
				if token, ok := tc.headers["Authorization"]; ok {
					want.token = token
				}
				if account, ok := tc.headers["Chatgpt-Account-Id"]; ok {
					want.account = account
				}
				if got != want {
					t.Fatalf("actual normalized direct image wire identity=%+v want=%+v", got, want)
				}
				before, ok := manager.GetByID(id)
				if !ok || !cliproxyauth.QuotaRecoveryCandidate(before) {
					t.Fatal("actual direct image rejection did not establish recoverable quota")
				}
				// Canonical outer-account capacity cannot establish capacity for a different
				// identity applied by the normalized direct-image model's header override.
				recovered, _, err := manager.ResetQuotaIfUnchanged(context.Background(), before)
				if err != nil {
					t.Fatal(err)
				}
				after, _ := manager.GetByID(id)
				if tc.recover {
					if recovered == nil || cliproxyauth.QuotaRecoveryCandidate(after) {
						t.Fatal("matching normalized direct image identity did not recover")
					}
				} else if recovered != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("outer usage cleared foreign normalized direct image cooldown")
				}
			})
		}
	}
}
