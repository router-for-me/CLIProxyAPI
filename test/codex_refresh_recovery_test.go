package test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type codexRecoveryTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t codexRecoveryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	local := req.Clone(req.Context())
	local.URL.Scheme = t.target.Scheme
	local.URL.Host = t.target.Host
	return t.base.RoundTrip(local)
}

func TestCodexSIWCRecoveryPreservesRequestPartition(t *testing.T) {
	for _, tc := range []struct {
		name          string
		missingSIWC   bool
		siwcStatus    int
		nativeAllowed bool
	}{
		{name: "expired SIWC", siwcStatus: http.StatusServiceUnavailable, nativeAllowed: true},
		{name: "missing SIWC with healthy model state", missingSIWC: true, siwcStatus: http.StatusServiceUnavailable, nativeAllowed: true},
		{name: "explicit SIWC rejection", siwcStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const model = "gpt-5.4"
			var outerCalls, siwcCalls, inferenceCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch req.URL.Path {
				case "/oauth/token":
					outerCalls.Add(1)
					_, _ = io.WriteString(w, `{"access_token":"outer-rotated","refresh_token":"outer-next","expires_in":86400}`)
				case "/api/accounts/oauth/token":
					siwcCalls.Add(1)
					w.WriteHeader(tc.siwcStatus)
					body := `{"error":"server_error"}`
					if tc.siwcStatus == http.StatusBadRequest {
						body = `{"error":"invalid_grant","code":"refresh_token_reused"}`
					}
					_, _ = io.WriteString(w, body)
				default:
					inferenceCalls.Add(1)
					if got := req.Header.Get("Authorization"); got != "Bearer outer-rotated" {
						t.Errorf("inference used %q, want the refreshed outer token", got)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"recovery-response\",\"status\":\"completed\",\"model\":\"gpt-5.4\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
				}
			}))
			t.Cleanup(server.Close)
			target, errParse := url.Parse(server.URL)
			if errParse != nil {
				t.Fatal(errParse)
			}
			transport := &http.Transport{}
			t.Cleanup(transport.CloseIdleConnections)
			originalTransport := http.DefaultTransport
			http.DefaultTransport = codexRecoveryTransport{target: target, base: transport}
			t.Cleanup(func() { http.DefaultTransport = originalTransport })

			cfg := &config.Config{AuthDir: t.TempDir()}
			manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
			authID := t.Name()
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
			now := time.Now()
			siwc := map[string]any{"refresh_token": authID + "-siwc", "client_id": "issued-client"}
			if !tc.missingSIWC {
				siwc["access_token"] = "siwc-expired"
				siwc["expires_at"] = now.Add(-time.Hour).Format(time.RFC3339)
			}
			auth := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive,
				Attributes: map[string]string{"base_url": server.URL},
				Metadata: map[string]any{
					"access_token": "outer-before", "refresh_token": authID + "-outer", "expired": now.Add(time.Hour).Format(time.RFC3339),
					"use_siwc": true, "siwc": siwc,
				},
			}
			if tc.missingSIWC {
				auth.ModelStates = map[string]*coreauth.ModelState{model: {Status: coreauth.StatusActive}}
			}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			if _, errRefresh := manager.ForceRefreshAuth(context.Background(), authID); errRefresh != nil {
				t.Fatal(errRefresh)
			}
			if outerCalls.Load() != 1 || siwcCalls.Load() != 1 {
				t.Fatalf("refresh HTTP calls = outer %d / SIWC %d, want 1 / 1", outerCalls.Load(), siwcCalls.Load())
			}
			recovered, _ := manager.GetByID(authID)
			if !recovered.RequiresSignIn || recovered.RefreshError == nil {
				t.Fatal("the unresolved or rejected SIWC grant lost its recovery requirement")
			}
			request := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)}
			assertSIWCBlocked := func() {
				t.Helper()
				_, errExecute := manager.Execute(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
				var authErr *coreauth.Error
				if !errors.As(errExecute, &authErr) || authErr.Code != "auth_unavailable" {
					t.Fatalf("unusable SIWC credential reached execution: %v", errExecute)
				}
			}
			assertSIWCBlocked()
			_, errNative := manager.Execute(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Headers:      http.Header{"User-Agent": {"codex_cli_rs/1.0.0"}},
			})
			if (errNative == nil) != tc.nativeAllowed {
				t.Fatalf("native outer inference: error=%v, want allowed=%v", errNative, tc.nativeAllowed)
			}
			assertSIWCBlocked()
			_, _ = manager.ForceRefreshAuth(context.Background(), authID)
			if siwcCalls.Load() != 1 {
				t.Fatal("a later refresh resubmitted the fenced SIWC grant")
			}
			assertSIWCBlocked()
			wantInference := int32(0)
			if tc.nativeAllowed {
				wantInference = 1
			}
			if inferenceCalls.Load() != wantInference {
				t.Fatalf("inference calls = %d, want %d", inferenceCalls.Load(), wantInference)
			}
		})
	}
}
