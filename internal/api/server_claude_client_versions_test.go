package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

func TestManagementClaudeClientVersionsReportsObservedDrift(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	helps.ResetClaudeClientVersionRegistry()
	t.Cleanup(helps.ResetClaudeClientVersionRegistry)

	cfg := &proxyconfig.Config{ClaudeHeaderDefaults: proxyconfig.ClaudeHeaderDefaults{
		UserAgent: "claude-cli/2.1.252 (external, cli)",
	}}
	helps.ObserveClaudeClientVersion(nil, "sk-observed-credential", http.Header{
		"User-Agent": {"claude-cli/2.1.259 (external, cli)"},
	}, cfg)

	server := newTestServer(t)
	server.cfg.ClaudeHeaderDefaults = cfg.ClaudeHeaderDefaults
	server.mgmt.SetConfig(server.cfg)

	unauthorized := httptest.NewRequest(http.MethodGet, "/v0/management/claude-client-versions", nil)
	unauthorizedRecorder := httptest.NewRecorder()
	server.engine.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", unauthorizedRecorder.Code, http.StatusUnauthorized)
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/claude-client-versions", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var report helps.ClaudeClientVersionsReport
	if err := json.Unmarshal(recorder.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v body=%s", err, recorder.Body.String())
	}
	if report.ConfiguredVersion != "2.1.252" || report.ConfigKey != helps.ClaudeClientVersionConfigKey {
		t.Fatalf("report = %#v, want the configured 2.1.252 baseline and config key", report)
	}
	if len(report.Credentials) != 1 {
		t.Fatalf("credentials = %#v, want one", report.Credentials)
	}
	credential := report.Credentials[0]
	if credential.HighestObservedVersion != "2.1.259" || !credential.Mismatched {
		t.Fatalf("credential = %#v, want 2.1.259 flagged as mismatched", credential)
	}
	if credential.Credential == "api-key:sk-observed-credential" {
		t.Fatalf("credential label %q leaks the raw API key", credential.Credential)
	}
}
