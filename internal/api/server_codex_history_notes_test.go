package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexHistoryNotesForwardsOAuthRequest(t *testing.T) {
	server := newTestServer(t)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token", "account_id": "account-123"},
	}
	if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
		t.Fatalf("register Codex auth: %v", err)
	}

	cases := []struct {
		path        string
		body        string
		upstreamURL string
	}{
		{
			path:        "/v1/alpha/notes/v2/list_files_by_prefix",
			body:        `{"prefix":"","context":{"session_id":"11111111-1111-4111-8111-111111111111","current_agent_name":"/root"}}`,
			upstreamURL: "https://chatgpt.com/backend-api/codex/alpha/notes/v2/list_files_by_prefix",
		},
		{
			path:        "/v1/alpha/history/v2/list_windows",
			body:        `{"limit":1,"context":{"session_id":"11111111-1111-4111-8111-111111111111","current_agent_name":"/root"}}`,
			upstreamURL: "https://chatgpt.com/backend-api/codex/alpha/history/v2/list_windows",
		},
		{
			path:        "/backend-api/codex/alpha/notes/v2/list_files_by_prefix",
			body:        `{"prefix":"","context":{"session_id":"22222222-2222-4222-8222-222222222222","current_agent_name":"/root"}}`,
			upstreamURL: "https://chatgpt.com/backend-api/codex/alpha/notes/v2/list_files_by_prefix",
		},
		{
			path:        "/backend-api/codex/alpha/history/v2/list_windows",
			body:        `{"limit":1,"context":{"session_id":"22222222-2222-4222-8222-222222222222","current_agent_name":"/root"}}`,
			upstreamURL: "https://chatgpt.com/backend-api/codex/alpha/history/v2/list_windows",
		},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			executor.request = nil
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
			}
			if executor.request == nil {
				t.Fatal("Codex executor did not receive a request")
			}
			if got := executor.request.URL.String(); got != tc.upstreamURL {
				t.Fatalf("upstream URL = %q, want %q", got, tc.upstreamURL)
			}
			if got := string(executor.body); got != tc.body {
				t.Fatalf("upstream body = %q, want %q", got, tc.body)
			}
			if got := executor.request.Header.Get("Authorization"); got != "Bearer codex-token" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := executor.request.Header.Get("Chatgpt-Account-Id"); got != "account-123" {
				t.Fatalf("Chatgpt-Account-Id = %q", got)
			}
		})
	}
}

func TestCodexHistoryNotesPreservesEncryptedHeadersAndSessionContext(t *testing.T) {
	server := newTestServer(t)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token"},
	}
	if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
		t.Fatalf("register Codex auth: %v", err)
	}

	body := `{"query":"secret","context":{"session_id":"33333333-3333-4333-8333-333333333333","current_agent_name":"/root"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/notes/v2/search_contents", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-openai-encrypted-tool-arguments", "true")
	req.Header.Set("x-openai-tool-output-truncation-policy", `{"max_bytes":4096}`)
	req.Header.Set("Version", "0.153.4")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if executor.request == nil {
		t.Fatal("Codex executor did not receive a request")
	}
	if got := executor.request.Header.Get("x-openai-encrypted-tool-arguments"); got != "true" {
		t.Fatalf("encrypted arguments header = %q", got)
	}
	if got := executor.request.Header.Get("x-openai-tool-output-truncation-policy"); got != `{"max_bytes":4096}` {
		t.Fatalf("truncation policy header = %q", got)
	}
	if got := executor.request.Header.Get("Version"); got != "0.153.4" {
		t.Fatalf("Version = %q", got)
	}
	if got := executor.request.Header.Get("Session_id"); got != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("Session_id = %q", got)
	}
	if got := executor.request.Header.Get("X-Session-ID"); got != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("X-Session-ID = %q", got)
	}
}

func TestCodexHistoryNotesOptInAPIKeyUsesConfiguredEndpoint(t *testing.T) {
	server := newTestServer(t)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-alpha-api-key",
		Provider: "codex",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			auth.AttributeAPIKey:           "codex-alpha-key",
			auth.AttributeCodexAlphaSearch: "true",
			"base_url":                     "https://codex.example.com/v1/",
		},
	}
	if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
		t.Fatalf("register Codex API key: %v", err)
	}

	body := `{"prefix":"","context":{"session_id":"44444444-4444-4444-8444-444444444444","current_agent_name":"/root"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/notes/v2/list_files_by_prefix", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if executor.request == nil {
		t.Fatal("Codex executor did not receive a request")
	}
	if got, want := executor.request.URL.String(), "https://codex.example.com/v1/alpha/notes/v2/list_files_by_prefix"; got != want {
		t.Fatalf("upstream URL = %q, want %q", got, want)
	}
	if got := executor.request.Header.Get("Authorization"); got != "Bearer codex-alpha-key" {
		t.Fatalf("Authorization = %q, want API key bearer", got)
	}
	if got := string(executor.body); got != body {
		t.Fatalf("upstream body = %q, want %q", got, body)
	}
}

func TestCodexHistoryNotesRejectsOrdinaryAPIKey(t *testing.T) {
	server := newTestServer(t)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-plain-api-key",
		Provider: "codex",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			auth.AttributeAPIKey: "plain-key",
			"base_url":           "https://codex.example.com/v1",
		},
	}
	if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
		t.Fatalf("register Codex API key: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/history/v2/list_windows", strings.NewReader(`{"limit":1}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
	if executor.request != nil {
		t.Fatal("ordinary API key sent an upstream request")
	}
}

func TestCodexHistoryNotesRejectsUnknownAction(t *testing.T) {
	server := newTestServer(t)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token"},
	}
	if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
		t.Fatalf("register Codex auth: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/notes/v2/not_a_real_op", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusNotFound, rr.Body.String())
	}
	if executor.request != nil {
		t.Fatal("unknown action sent an upstream request")
	}
}

func TestParseCodexHistoryNotesPath(t *testing.T) {
	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{path: "/v1/alpha/notes/v2/list_files_by_prefix", want: "alpha/notes/v2/list_files_by_prefix", ok: true},
		{path: "/backend-api/codex/alpha/history/v2/list_windows", want: "alpha/history/v2/list_windows", ok: true},
		{path: "/v1/alpha/notes/v2/not_a_real_op", ok: false},
		{path: "/v1/alpha/search", ok: false},
	}
	for _, tc := range cases {
		got, ok := parseCodexHistoryNotesPath(tc.path)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("parseCodexHistoryNotesPath(%q) = (%q, %v), want (%q, %v)", tc.path, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCodexHistoryNotesSessionIDFromContext(t *testing.T) {
	body := []byte(`{"prefix":"","context":{"session_id":"55555555-5555-4555-8555-555555555555","current_agent_name":"/root"}}`)
	if got := codexHistoryNotesSessionID(body, ""); got != "55555555-5555-4555-8555-555555555555" {
		t.Fatalf("session id = %q", got)
	}
	if got := codexHistoryNotesSessionID([]byte(`{}`), "header-session"); got != "header-session" {
		t.Fatalf("header fallback = %q", got)
	}
}
