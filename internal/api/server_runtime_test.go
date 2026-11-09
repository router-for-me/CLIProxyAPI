package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func TestHostRuntimeControlRegistersAuthenticatedManagementRoutes(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	startedAt := time.Date(2026, time.August, 28, 1, 2, 3, 0, time.UTC)
	shutdownCalled := make(chan struct{}, 1)
	server := newTestServerWithOptions(t, WithHostRuntimeControl(managementHandlers.RuntimeInfo{
		ContractVersion:  "1",
		ComponentVersion: "7.0.0-pinable.1",
		Commit:           "abc123",
		BuildTime:        "2026-08-28T00:00:00Z",
		PID:              4321,
		StartedAt:        startedAt,
	}, func() { shutdownCalled <- struct{}{} }))

	unauthorized := httptest.NewRequest(http.MethodGet, "/v0/management/runtime-info", nil)
	unauthorizedRecorder := httptest.NewRecorder()
	server.engine.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorizedRecorder.Code, http.StatusUnauthorized)
	}

	infoRequest := httptest.NewRequest(http.MethodGet, "/v0/management/runtime-info", nil)
	infoRequest.Header.Set("Authorization", "Bearer test-management-key")
	infoRecorder := httptest.NewRecorder()
	server.engine.ServeHTTP(infoRecorder, infoRequest)
	if infoRecorder.Code != http.StatusOK {
		t.Fatalf("runtime-info status = %d, want %d; body=%s", infoRecorder.Code, http.StatusOK, infoRecorder.Body.String())
	}
	var info struct {
		ContractVersion  string `json:"contract_version"`
		ComponentVersion string `json:"component_version"`
		PID              int    `json:"pid"`
	}
	if err := json.Unmarshal(infoRecorder.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode runtime-info: %v", err)
	}
	if info.ContractVersion != "1" || info.ComponentVersion != "7.0.0-pinable.1" || info.PID != 4321 {
		t.Fatalf("runtime-info = %#v", info)
	}

	shutdownRequest := httptest.NewRequest(http.MethodPost, "/v0/management/runtime-shutdown", nil)
	shutdownRequest.RemoteAddr = "127.0.0.1:45678"
	shutdownRequest.Header.Set("Authorization", "Bearer test-management-key")
	shutdownRecorder := httptest.NewRecorder()
	server.engine.ServeHTTP(shutdownRecorder, shutdownRequest)
	if shutdownRecorder.Code != http.StatusAccepted {
		t.Fatalf("runtime-shutdown status = %d, want %d; body=%s", shutdownRecorder.Code, http.StatusAccepted, shutdownRecorder.Body.String())
	}
	select {
	case <-shutdownCalled:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not called")
	}
}

func TestRuntimeAccessProviderSurvivesConfigApply(t *testing.T) {
	runtimeProvider := configaccess.NewRuntimeAPIKeyProvider("cliproxyapi-ephemeral", "runtime-secret")
	server := newTestServerWithOptions(t, WithRuntimeAccessProviders(runtimeProvider))
	nextConfig := server.cfg.CloneForRuntime()
	nextConfig.APIKeys = nil
	server.UpdateClients(nextConfig)

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer runtime-secret")
	result, authErr := server.accessManager.Authenticate(request.Context(), request)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}
	if result.Provider != "cliproxyapi-ephemeral" {
		t.Fatalf("provider = %q, want cliproxyapi-ephemeral", result.Provider)
	}

	request.Header.Set("Authorization", "Bearer wrong-secret")
	if _, authErr = server.accessManager.Authenticate(request.Context(), request); !sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeInvalidCredential) {
		t.Fatalf("wrong key error = %v, want invalid credential", authErr)
	}
}

func TestRuntimeAccessProviderDoesNotEnableExampleConfigKey(t *testing.T) {
	runtimeProvider := configaccess.NewRuntimeAPIKeyProvider("cliproxyapi-ephemeral", "runtime-secret")
	server := newTestServerWithOptions(t, WithRuntimeAccessProviders(runtimeProvider))
	nextConfig := server.cfg.CloneForRuntime()
	nextConfig.APIKeys = []string{"your-api-key-1"}
	server.UpdateClients(nextConfig)

	tests := []struct {
		name       string
		key        string
		wantStatus int
	}{
		{name: "runtime key", key: "runtime-secret", wantStatus: http.StatusOK},
		{name: "example config key", key: "your-api-key-1", wantStatus: http.StatusUnauthorized},
		{name: "wrong key", key: "wrong-secret", wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			request.Header.Set("Authorization", "Bearer "+tt.key)
			recorder := httptest.NewRecorder()
			server.engine.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestExampleConfigKeyWithoutRuntimeProviderFailsClosed(t *testing.T) {
	server := newTestServer(t)
	nextConfig := server.cfg.CloneForRuntime()
	nextConfig.APIKeys = []string{"your-api-key-1"}
	server.UpdateClients(nextConfig)

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer your-api-key-1")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
}
