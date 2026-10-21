package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usageapi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func usageTestServer(t *testing.T, cfg *proxyconfig.Config) *Server {
	t.Helper()
	if cfg == nil {
		cfg = &proxyconfig.Config{}
	}
	cfg.SDKConfig = sdkconfig.SDKConfig{APIKeys: []string{"user-key"}}
	server := newTestServerWithConfig(t, cfg)
	_, errRegister := server.handlers.AuthManager.Register(context.Background(), &auth.Auth{
		ID:       "claude-account",
		Provider: "claude",
		Status:   auth.StatusActive,
		Quota: auth.QuotaState{
			ObservedAt: time.UnixMilli(1_759_999_000_000),
			Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": "0.5",
				"Anthropic-Ratelimit-Unified-5h-Reset":       "1760000000",
			},
		},
	})
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	return server
}

func requestUsage(server *Server, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, request)
	return recorder
}

func TestUsageEndpointDisabledByDefault(t *testing.T) {
	server := usageTestServer(t, nil)
	recorder := requestUsage(server, "Bearer user-key")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestUsageEndpointRequiresProviderAllowList(t *testing.T) {
	server := usageTestServer(t, &proxyconfig.Config{UsageAPI: proxyconfig.UsageAPIConfig{Enabled: true}})
	recorder := requestUsage(server, "Bearer user-key")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestUsageEndpointRequiresProxyAPIKey(t *testing.T) {
	server := usageTestServer(t, &proxyconfig.Config{
		UsageAPI: proxyconfig.UsageAPIConfig{Enabled: true, Providers: []string{"claude"}},
	})
	recorder := requestUsage(server, "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestUsageEndpointRejectsManagementKey(t *testing.T) {
	cfg := &proxyconfig.Config{
		UsageAPI:         proxyconfig.UsageAPIConfig{Enabled: true, Providers: []string{"claude"}},
		RemoteManagement: proxyconfig.RemoteManagement{SecretKey: "admin-key"},
	}
	cfg.SDKConfig = sdkconfig.SDKConfig{}
	server := newTestServerWithConfig(t, cfg)
	recorder := requestUsage(server, "Bearer admin-key")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestUsageEndpointReturnsObservedLimits(t *testing.T) {
	server := usageTestServer(t, &proxyconfig.Config{
		UsageAPI: proxyconfig.UsageAPIConfig{Enabled: true, Providers: []string{"claude"}},
	})
	recorder := requestUsage(server, "Bearer user-key")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	var response usageapi.Response
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v; body = %s", errUnmarshal, recorder.Body.String())
	}
	if response.GeneratedAt <= 0 {
		t.Fatalf("generatedAt = %d, want positive", response.GeneratedAt)
	}
	if len(response.Reports) != 1 {
		t.Fatalf("reports = %#v, want one", response.Reports)
	}
	report := response.Reports[0]
	if report.Provider != "claude" {
		t.Fatalf("provider = %q, want claude", report.Provider)
	}
	if report.FetchedAt != 1_759_999_000_000 {
		t.Fatalf("fetchedAt = %d", report.FetchedAt)
	}
	if len(report.Limits) != 1 || report.Limits[0].ID != "claude:5h" {
		t.Fatalf("limits = %#v", report.Limits)
	}
	if got := report.Metadata["source"]; got != "observed-signals" {
		t.Fatalf("source = %v, want observed-signals", got)
	}
}
