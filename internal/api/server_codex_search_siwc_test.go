package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type codexSearchSIWCTransport func(*http.Request) (*http.Response, error)

func (transport codexSearchSIWCTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func TestCodexAlphaSearchSIWCWithRealExecutor(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		path        string
		apiKey      bool
		wantURL     string
		wantBearer  string
		wantAccount string
	}{
		{
			name:        "v1",
			path:        "/v1/alpha/search",
			wantURL:     "https://chatgpt.com/backend-api/codex/alpha/search",
			wantBearer:  "Bearer original-oauth-token",
			wantAccount: "original-account",
		},
		{
			name:        "codex_alias",
			path:        "/backend-api/codex/alpha/search",
			wantURL:     "https://chatgpt.com/backend-api/codex/alpha/search",
			wantBearer:  "Bearer original-oauth-token",
			wantAccount: "original-account",
		},
		{
			name:       "opt_in_api_key",
			path:       "/v1/alpha/search",
			apiKey:     true,
			wantURL:    "https://codex.example.com/v1/alpha/search",
			wantBearer: "Bearer api-key-token",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := newTestServer(t)
			server.handlers.AuthManager.RegisterExecutor(executor.NewCodexAutoExecutor(server.getConfig()))
			credential := &auth.Auth{
				ID:       "codex-search-dual-partition",
				Provider: "codex",
				Status:   auth.StatusActive,
				Metadata: map[string]any{
					"type":          "codex",
					"access_token":  "original-oauth-token",
					"refresh_token": "original-refresh-token",
					"account_id":    "original-account",
					"use_siwc":      true,
					"siwc": map[string]any{
						"access_token":  "nested-siwc-token",
						"refresh_token": "nested-siwc-refresh-token",
						"client_id":     "oaiapp_test",
					},
				},
			}
			if testCase.apiKey {
				credential.Metadata = nil
				credential.Attributes = map[string]string{
					auth.AttributeAPIKey:           "api-key-token",
					auth.AttributeCodexAlphaSearch: "true",
					"base_url":                     "https://codex.example.com/v1/",
				}
			}
			registry.GetGlobalRegistry().RegisterClient(credential.ID, "codex", []*registry.ModelInfo{{ID: "gpt-6.1-sol"}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(credential.ID)
			})
			if _, errRegister := server.handlers.AuthManager.Register(t.Context(), credential); errRegister != nil {
				t.Fatalf("register Codex credential: %v", errRegister)
			}

			var upstreamRequests []*http.Request
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexSearchSIWCTransport(func(req *http.Request) (*http.Response, error) {
				upstreamRequests = append(upstreamRequests, req.Clone(req.Context()))
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"results":[]}`)),
					Request:    req,
				}, nil
			}))
			req := httptest.NewRequest(http.MethodPost, testCase.path, strings.NewReader(`{"id":"siwc-search-repro","model":"gpt-6.1-sol","commands":{"search_query":[{"q":"OpenAI"}]}}`)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			server.engine.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
			}
			if len(upstreamRequests) != 1 {
				t.Fatalf("upstream transport calls = %d, want 1", len(upstreamRequests))
			}
			upstream := upstreamRequests[0]
			if got := upstream.URL.String(); got != testCase.wantURL {
				t.Errorf("upstream URL = %q, want %q", got, testCase.wantURL)
			}
			if got := upstream.Header.Get("Authorization"); got != testCase.wantBearer {
				t.Errorf("final transport Authorization = %q, want %q", got, testCase.wantBearer)
			}
			if got := upstream.Header.Get("Chatgpt-Account-Id"); got != testCase.wantAccount {
				t.Errorf("final transport Chatgpt-Account-Id = %q, want %q", got, testCase.wantAccount)
			}
		})
	}
}
