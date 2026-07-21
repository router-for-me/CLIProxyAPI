package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/accountlimits"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAccountLimitsRequiresProxyAuthentication(t *testing.T) {
	server := newTestServer(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/account/limits", nil)

	server.engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestAccountLimitsReturnsCapturedAnthropicLimits(t *testing.T) {
	server := newTestServer(t)
	registerLimitsAuth(t, server, &cliproxyauth.Auth{ID: "claude-local", Provider: "claude"})
	accountlimits.CaptureAnthropicRateLimits("claude-local", http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.25"},
	}, time.Unix(1779695597, 0))

	recorder := performLimitsRequest(server, "/v1/account/limits")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload accountlimits.ProviderLimitsPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Object != accountlimits.ProviderLimitsObject || payload.AccountID != "claude-local" || payload.Provider != accountlimits.ProviderAnthropic {
		t.Fatalf("unexpected payload metadata: %+v", payload)
	}
	if len(payload.Snapshots) != 1 || payload.Snapshots[0].Primary == nil || payload.Snapshots[0].Primary.UsedPercent != 25 {
		t.Fatalf("unexpected snapshots: %+v", payload.Snapshots)
	}
}

func TestAccountLimitsRequiresSelectorWhenMultipleCredentialsMatch(t *testing.T) {
	server := newTestServer(t)
	registerLimitsAuth(t, server,
		&cliproxyauth.Auth{ID: "claude-a", Provider: "claude"},
		&cliproxyauth.Auth{ID: "claude-b", Provider: "claude"},
	)

	recorder := performLimitsRequest(server, "/v1/account/limits")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusConflict, recorder.Body.String())
	}

	recorder = performLimitsRequest(server, "/v1/account/limits?provider=anthropic&account_id=claude-b")
	if recorder.Code != http.StatusOK {
		t.Fatalf("selected status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAccountLimitsFetchesCodexUsageFromLocalCredential(t *testing.T) {
	var receivedAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		receivedAuthorization = request.Header.Get("Authorization")
		if request.URL.Path != "/api/codex/usage" {
			t.Fatalf("path = %q, want /api/codex/usage", request.URL.Path)
		}
		if request.Header.Get("ChatGPT-Account-Id") != "chatgpt-account" {
			t.Fatalf("ChatGPT-Account-Id = %q", request.Header.Get("ChatGPT-Account-Id"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":12,"limit_window_seconds":18000}},"plan_type":"plus"}`))
	}))
	defer upstream.Close()

	server := newTestServer(t)
	registerLimitsAuth(t, server, &cliproxyauth.Auth{
		ID:       "codex-local",
		Provider: "codex",
		Metadata: map[string]any{"access_token": testCodexJWT("chatgpt-account")},
		Attributes: map[string]string{
			"base_url": upstream.URL,
		},
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/account/limits", nil)
	request.Header.Set("Authorization", "Bearer test-key")
	server.engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if receivedAuthorization != "Bearer "+testCodexJWT("chatgpt-account") {
		t.Fatalf("Authorization = %q", receivedAuthorization)
	}
	var payload accountlimits.ProviderLimitsPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.AccountID != "codex-local" || payload.Provider != accountlimits.ProviderOpenAI || payload.CapturedAt == nil {
		t.Fatalf("unexpected payload metadata: %+v", payload)
	}
}

func TestAccountLimitsFetchesZaiQuotaFromLocalConfig(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/monitor/usage/quota/limit" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer zai-key" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":30}],"level":"max"}}`))
	}))
	defer upstream.Close()

	server := newTestServer(t)
	server.cfg.OpenAICompatibility = []proxyconfig.OpenAICompatibility{{
		Name:          "zai",
		BaseURL:       upstream.URL + "/v1",
		APIKeyEntries: []proxyconfig.OpenAICompatibilityAPIKey{{APIKey: "zai-key"}},
	}}

	recorder := performLimitsRequest(server, "/v1/account/limits")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload accountlimits.ProviderLimitsPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Provider != accountlimits.ProviderZai || payload.AccountID != "zai" || payload.CapturedAt == nil {
		t.Fatalf("unexpected payload metadata: %+v", payload)
	}
}

func registerLimitsAuth(t *testing.T, server *Server, entries ...*cliproxyauth.Auth) {
	t.Helper()
	for _, entry := range entries {
		if _, err := server.handlers.AuthManager.Register(context.Background(), entry); err != nil {
			t.Fatalf("register auth: %v", err)
		}
	}
}

func performLimitsRequest(server *Server, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer test-key")
	server.engine.ServeHTTP(recorder, request)
	return recorder
}

func testCodexJWT(accountID string) string {
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": accountID},
	})
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
