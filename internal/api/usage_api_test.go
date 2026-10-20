package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type usageAPIResponsePayload struct {
	Providers map[string]usageAPIProviderPayload `json:"providers"`
}

type usageAPIProviderPayload struct {
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests"`
	Credentials    []usageAPICredentialPayload    `json:"credentials"`
}

type usageAPICredentialPayload struct {
	Name           string                         `json:"name"`
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests"`
}

func sumUsageAPIBuckets(buckets []coreauth.RecentRequestBucket) (int64, int64) {
	var success int64
	var failed int64
	for _, bucket := range buckets {
		success += bucket.Success
		failed += bucket.Failed
	}
	return success, failed
}

func registerUsageAPITestAuth(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
	t.Helper()
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth %s: %v", auth.ID, err)
	}
}

func markUsageAPITestResult(t *testing.T, manager *coreauth.Manager, authID, provider string, success bool) {
	t.Helper()
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   authID,
		Provider: provider,
		Model:    "test-model",
		Success:  success,
	})
}

func performUsageAPIRequest(server *Server, authorization string) *httptest.ResponseRecorder {
	return performUsageAPIRequestTarget(server, "/v1/usage", authorization)
}

func performUsageAPIRequestTarget(server *Server, target, authorization string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	server.engine.ServeHTTP(rec, req)
	return rec
}

func TestUsageQueryDisabledReturnsNotFound(t *testing.T) {
	server := newTestServerWithConfig(t, &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		UsageAPI:  proxyconfig.UsageAPIConfig{Enabled: false, Providers: []string{"codex"}},
	})
	registerUsageAPITestAuth(t, server.handlers.AuthManager, &coreauth.Auth{
		ID:         "codex-auth",
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "sk-upstream-secret"},
	})

	rec := performUsageAPIRequest(server, "Bearer test-key")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-upstream-secret") {
		t.Fatalf("disabled endpoint leaked credentials: %s", rec.Body.String())
	}
}

func TestUsageQueryRequiresAuthenticatedClientKey(t *testing.T) {
	t.Run("no access providers configured", func(t *testing.T) {
		server := newTestServerWithConfig(t, &proxyconfig.Config{
			UsageAPI: proxyconfig.UsageAPIConfig{Enabled: true, Providers: []string{"codex"}},
		})
		registerUsageAPITestAuth(t, server.handlers.AuthManager, &coreauth.Auth{
			ID:         "codex-auth",
			Provider:   "codex",
			Attributes: map[string]string{"api_key": "sk-upstream-secret"},
		})

		rec := performUsageAPIRequest(server, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "providers") {
			t.Fatalf("unauthenticated response exposed usage: %s", rec.Body.String())
		}
	})

	t.Run("configured access keys reject missing key", func(t *testing.T) {
		server := newTestServerWithConfig(t, &proxyconfig.Config{
			SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
			UsageAPI:  proxyconfig.UsageAPIConfig{Enabled: true, Providers: []string{"codex"}},
		})

		rec := performUsageAPIRequest(server, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})

	t.Run("management key is not a client key", func(t *testing.T) {
		server := newTestServerWithConfig(t, &proxyconfig.Config{
			SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
			UsageAPI:  proxyconfig.UsageAPIConfig{Enabled: true, Providers: []string{"codex"}},
		})

		rec := performUsageAPIRequest(server, "Bearer management-secret")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})
}

func TestUsageQueryFiltersProvidersAndRedactsCredentialMaterial(t *testing.T) {
	server := newTestServerWithConfig(t, &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		UsageAPI: proxyconfig.UsageAPIConfig{
			Enabled:   true,
			Providers: []string{" CODEX "},
			Credentials: []proxyconfig.UsageAPICredential{
				{AuthIndex: "idx-claude", Name: "team-claude"},
			},
		},
	})
	manager := server.handlers.AuthManager

	registerUsageAPITestAuth(t, manager, &coreauth.Auth{
		ID:       "codex-auth-id",
		Index:    "idx-codex",
		Provider: "codex",
		FileName: "/secret/codex.json",
		Label:    "Codex Ops",
		Attributes: map[string]string{
			"api_key":  "sk-codex-secret",
			"base_url": "https://codex.internal.example.com",
		},
		Metadata: map[string]any{"email": "codex@example.com"},
	})
	registerUsageAPITestAuth(t, manager, &coreauth.Auth{
		ID:       "claude-auth-id",
		Index:    "idx-claude",
		Provider: "claude",
		FileName: "/secret/claude.json",
		Attributes: map[string]string{
			"api_key":  "sk-claude-secret",
			"base_url": "https://claude.internal.example.com",
		},
		Metadata: map[string]any{"email": "claude@example.com"},
	})
	registerUsageAPITestAuth(t, manager, &coreauth.Auth{
		ID:         "gemini-auth-id",
		Index:      "idx-gemini",
		Provider:   "gemini",
		Attributes: map[string]string{"api_key": "sk-gemini-secret"},
	})

	markUsageAPITestResult(t, manager, "codex-auth-id", "codex", true)
	markUsageAPITestResult(t, manager, "codex-auth-id", "codex", false)
	markUsageAPITestResult(t, manager, "claude-auth-id", "claude", true)
	markUsageAPITestResult(t, manager, "gemini-auth-id", "gemini", true)

	rec := performUsageAPIRequest(server, "Bearer test-key")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	var payload usageAPIResponsePayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v body=%s", err, rec.Body.String())
	}

	codex, ok := payload.Providers["codex"]
	if !ok {
		t.Fatalf("missing codex provider: %s", rec.Body.String())
	}
	if codex.Success != 1 || codex.Failed != 1 {
		t.Fatalf("codex totals = %d/%d, want 1/1", codex.Success, codex.Failed)
	}
	if len(codex.Credentials) != 0 {
		t.Fatalf("codex credentials = %d, want 0", len(codex.Credentials))
	}
	if len(codex.RecentRequests) != 20 {
		t.Fatalf("codex buckets len = %d, want 20", len(codex.RecentRequests))
	}
	if success, failed := sumUsageAPIBuckets(codex.RecentRequests); success != 1 || failed != 1 {
		t.Fatalf("codex bucket totals = %d/%d, want 1/1", success, failed)
	}

	claude, ok := payload.Providers["claude"]
	if !ok {
		t.Fatalf("missing claude provider: %s", rec.Body.String())
	}
	if claude.Success != 1 || claude.Failed != 0 {
		t.Fatalf("claude totals = %d/%d, want 1/0", claude.Success, claude.Failed)
	}
	if len(claude.Credentials) != 1 || claude.Credentials[0].Name != "team-claude" {
		t.Fatalf("claude credentials = %#v, want team-claude", claude.Credentials)
	}
	if claude.Credentials[0].Success != 1 || claude.Credentials[0].Failed != 0 {
		t.Fatalf("claude credential totals = %d/%d, want 1/0", claude.Credentials[0].Success, claude.Credentials[0].Failed)
	}
	if success, failed := sumUsageAPIBuckets(claude.Credentials[0].RecentRequests); success != 1 || failed != 0 {
		t.Fatalf("claude credential bucket totals = %d/%d, want 1/0", success, failed)
	}

	if _, ok := payload.Providers["gemini"]; ok {
		t.Fatalf("gemini provider was exposed: %s", rec.Body.String())
	}

	for _, forbidden := range []string{
		"sk-codex-secret",
		"sk-claude-secret",
		"sk-gemini-secret",
		"codex.internal.example.com",
		"claude.internal.example.com",
		"codex@example.com",
		"claude@example.com",
		"codex-auth-id",
		"claude-auth-id",
		"/secret/codex.json",
		"/secret/claude.json",
		"Codex Ops",
		"idx-codex",
		"idx-claude",
	} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("usage response leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestUsageQueryEmptyAllowlistExposesNothing(t *testing.T) {
	server := newTestServerWithConfig(t, &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		UsageAPI:  proxyconfig.UsageAPIConfig{Enabled: true},
	})
	registerUsageAPITestAuth(t, server.handlers.AuthManager, &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
	})
	markUsageAPITestResult(t, server.handlers.AuthManager, "codex-auth", "codex", true)

	rec := performUsageAPIRequest(server, "Bearer test-key")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload usageAPIResponsePayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Providers) != 0 {
		t.Fatalf("providers = %#v, want empty", payload.Providers)
	}
}

func TestUsageQueryUsesCompatibilityProviderName(t *testing.T) {
	server := newTestServerWithConfig(t, &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		UsageAPI:  proxyconfig.UsageAPIConfig{Enabled: true, Providers: []string{"my-gateway"}},
	})
	registerUsageAPITestAuth(t, server.handlers.AuthManager, &coreauth.Auth{
		ID:         "compat-auth",
		Provider:   "openai",
		Attributes: map[string]string{"compat_name": "My-Gateway", "api_key": "sk-compat-secret"},
	})
	markUsageAPITestResult(t, server.handlers.AuthManager, "compat-auth", "openai", true)

	rec := performUsageAPIRequest(server, "Bearer test-key")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload usageAPIResponsePayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	provider, ok := payload.Providers["my-gateway"]
	if !ok || provider.Success != 1 {
		t.Fatalf("providers = %#v, want my-gateway with one success", payload.Providers)
	}
	if strings.Contains(rec.Body.String(), "sk-compat-secret") {
		t.Fatalf("usage response leaked compatibility credential: %s", rec.Body.String())
	}
}

func TestUsageQueryFiltersByProviderAndCredential(t *testing.T) {
	server := newTestServerWithConfig(t, &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		UsageAPI: proxyconfig.UsageAPIConfig{
			Enabled:   true,
			Providers: []string{"codex"},
			Credentials: []proxyconfig.UsageAPICredential{
				{AuthIndex: "idx-claude-primary", Name: "team-claude"},
				{AuthIndex: "idx-claude-backup", Name: "backup-claude"},
			},
		},
	})
	manager := server.handlers.AuthManager

	registerUsageAPITestAuth(t, manager, &coreauth.Auth{ID: "codex-auth", Index: "idx-codex", Provider: "codex"})
	registerUsageAPITestAuth(t, manager, &coreauth.Auth{ID: "claude-primary-auth", Index: "idx-claude-primary", Provider: "claude"})
	registerUsageAPITestAuth(t, manager, &coreauth.Auth{ID: "claude-backup-auth", Index: "idx-claude-backup", Provider: "claude"})
	registerUsageAPITestAuth(t, manager, &coreauth.Auth{ID: "gemini-auth", Index: "idx-gemini", Provider: "gemini"})

	markUsageAPITestResult(t, manager, "codex-auth", "codex", true)
	markUsageAPITestResult(t, manager, "claude-primary-auth", "claude", true)
	markUsageAPITestResult(t, manager, "claude-primary-auth", "claude", true)
	markUsageAPITestResult(t, manager, "claude-backup-auth", "claude", false)
	markUsageAPITestResult(t, manager, "gemini-auth", "gemini", true)

	decode := func(t *testing.T, target string) usageAPIResponsePayload {
		t.Helper()
		rec := performUsageAPIRequestTarget(server, target, "Bearer test-key")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d body=%s", target, rec.Code, http.StatusOK, rec.Body.String())
		}
		var payload usageAPIResponsePayload
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s decode payload: %v body=%s", target, err, rec.Body.String())
		}
		return payload
	}

	payload := decode(t, "/v1/usage?provider=codex")
	if len(payload.Providers) != 1 || payload.Providers["codex"].Success != 1 {
		t.Fatalf("provider filter payload = %#v, want only codex", payload.Providers)
	}

	payload = decode(t, "/v1/usage?provider=claude&credential=team-claude")
	claude, ok := payload.Providers["claude"]
	if !ok || len(payload.Providers) != 1 {
		t.Fatalf("provider+credential filter payload = %#v, want only claude", payload.Providers)
	}
	if claude.Success != 2 || claude.Failed != 0 {
		t.Fatalf("filtered claude totals = %d/%d, want 2/0", claude.Success, claude.Failed)
	}
	if len(claude.Credentials) != 1 || claude.Credentials[0].Name != "team-claude" {
		t.Fatalf("filtered claude credentials = %#v, want team-claude", claude.Credentials)
	}

	payload = decode(t, "/v1/usage?credential=team-claude")
	if len(payload.Providers) != 1 {
		t.Fatalf("credential-only filter payload = %#v, want only claude", payload.Providers)
	}
	claude = payload.Providers["claude"]
	if claude.Success != 2 || len(claude.Credentials) != 1 || claude.Credentials[0].Name != "team-claude" {
		t.Fatalf("credential-only filter claude = %#v, want team-claude totals only", claude)
	}

	payload = decode(t, "/v1/usage?provider=claude,codex")
	if len(payload.Providers) != 2 {
		t.Fatalf("comma filter payload = %#v, want claude and codex", payload.Providers)
	}

	payload = decode(t, "/v1/usage?provider=claude&provider=codex")
	if len(payload.Providers) != 2 {
		t.Fatalf("repeated filter payload = %#v, want claude and codex", payload.Providers)
	}

	payload = decode(t, "/v1/usage?provider=gemini")
	if len(payload.Providers) != 0 {
		t.Fatalf("filter widened allowlist: %#v", payload.Providers)
	}

	payload = decode(t, "/v1/usage?credential=idx-codex")
	if len(payload.Providers) != 0 {
		t.Fatalf("auth index credential filter matched unnamed credential: %#v", payload.Providers)
	}
}
