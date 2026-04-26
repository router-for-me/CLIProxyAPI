package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}

func newManagementTestServer(t *testing.T) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
		RemoteManagement: proxyconfig.RemoteManagement{
			SecretKey: "test-secret",
		},
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}

func TestHealthz(t *testing.T) {
	server := newTestServer(t)

	t.Run("GET", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
		}
		if resp.Status != "ok" {
			t.Fatalf("unexpected response status: got %q want %q", resp.Status, "ok")
		}
	})

	t.Run("HEAD", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("expected empty body for HEAD request, got %q", rr.Body.String())
		}
	})
}

func TestNewServer_ConfiguresInboundHTTPServerLimits(t *testing.T) {
	server := newTestServer(t)

	if server.server == nil {
		t.Fatal("expected underlying http server to be initialized")
	}
	if server.server.ReadHeaderTimeout != inboundReadHeaderTimeout {
		t.Fatalf("unexpected ReadHeaderTimeout: got %s want %s", server.server.ReadHeaderTimeout, inboundReadHeaderTimeout)
	}
	if server.server.IdleTimeout != inboundIdleTimeout {
		t.Fatalf("unexpected IdleTimeout: got %s want %s", server.server.IdleTimeout, inboundIdleTimeout)
	}
	if server.server.MaxHeaderBytes != inboundMaxHeaderBytes {
		t.Fatalf("unexpected MaxHeaderBytes: got %d want %d", server.server.MaxHeaderBytes, inboundMaxHeaderBytes)
	}
}

func TestPublicCORSOptions_UsesExplicitHeaders(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
	req.Header.Set("Origin", "https://example.com")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("unexpected status code: got %d want %d", rr.Code, http.StatusNoContent)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("unexpected allow origin: got %q want %q", rr.Header().Get("Access-Control-Allow-Origin"), "*")
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); got != corsAllowedHeaders {
		t.Fatalf("unexpected allow headers: got %q want %q", got, corsAllowedHeaders)
	}
}

func TestManagementCORSOptions_Denied(t *testing.T) {
	server := newManagementTestServer(t)

	req := httptest.NewRequest(http.MethodOptions, "/v0/management/config", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status code: got %d want %d", rr.Code, http.StatusForbidden)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("expected empty allow origin, got %q", rr.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestManagementUsageDetailRetentionLimitRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		AuthDir:                   authDir,
		UsageDetailRetentionLimit: 7,
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("usage-detail-retention-limit: 7\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	server := NewServer(
		cfg,
		auth.NewManager(nil, nil, nil),
		sdkaccess.NewManager(),
		configPath,
		WithLocalManagementPassword("local-pass"),
	)

	getReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage-detail-retention-limit", nil)
	getReq.RemoteAddr = "127.0.0.1:12345"
	getReq.Header.Set("Authorization", "Bearer local-pass")
	getResp := httptest.NewRecorder()
	server.engine.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d; body=%s", getResp.Code, http.StatusOK, getResp.Body.String())
	}
	if !strings.Contains(getResp.Body.String(), `"usage-detail-retention-limit":7`) {
		t.Fatalf("GET body missing usage detail retention limit: %s", getResp.Body.String())
	}

	putReq := httptest.NewRequest(http.MethodPut, "/v0/management/usage-detail-retention-limit", strings.NewReader(`{"value":12}`))
	putReq.RemoteAddr = "127.0.0.1:12345"
	putReq.Header.Set("Authorization", "Bearer local-pass")
	putReq.Header.Set("Content-Type", "application/json")
	putResp := httptest.NewRecorder()
	server.engine.ServeHTTP(putResp, putReq)
	if putResp.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d; body=%s", putResp.Code, http.StatusOK, putResp.Body.String())
	}
	if cfg.UsageDetailRetentionLimit != 12 {
		t.Fatalf("UsageDetailRetentionLimit = %d, want 12", cfg.UsageDetailRetentionLimit)
	}
	savedConfig, errReadConfig := os.ReadFile(configPath)
	if errReadConfig != nil {
		t.Fatalf("failed to read saved config: %v", errReadConfig)
	}
	if !strings.Contains(string(savedConfig), "usage-detail-retention-limit: 12") {
		t.Fatalf("saved config missing updated retention limit: %s", string(savedConfig))
	}
	server.UpdateClients(cfg)
	if !server.managementRoutesEnabled.Load() {
		t.Fatalf("management routes disabled after local-password config update")
	}
	getAfterUpdateReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage-detail-retention-limit", nil)
	getAfterUpdateReq.RemoteAddr = "127.0.0.1:12345"
	getAfterUpdateReq.Header.Set("Authorization", "Bearer local-pass")
	getAfterUpdateResp := httptest.NewRecorder()
	server.engine.ServeHTTP(getAfterUpdateResp, getAfterUpdateReq)
	if getAfterUpdateResp.Code != http.StatusOK {
		t.Fatalf("GET after UpdateClients status = %d, want %d; body=%s", getAfterUpdateResp.Code, http.StatusOK, getAfterUpdateResp.Body.String())
	}

	patchReq := httptest.NewRequest(http.MethodPatch, "/v0/management/usage-detail-retention-limit", strings.NewReader(`{"value":-5}`))
	patchReq.RemoteAddr = "127.0.0.1:12345"
	patchReq.Header.Set("Authorization", "Bearer local-pass")
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp := httptest.NewRecorder()
	server.engine.ServeHTTP(patchResp, patchReq)
	if patchResp.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d; body=%s", patchResp.Code, http.StatusOK, patchResp.Body.String())
	}
	if cfg.UsageDetailRetentionLimit != 0 {
		t.Fatalf("negative UsageDetailRetentionLimit = %d, want 0", cfg.UsageDetailRetentionLimit)
	}
}

func TestManagementHTMLCORSOptions_Denied(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodOptions, "/management.html", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status code: got %d want %d", rr.Code, http.StatusForbidden)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("expected empty allow origin, got %q", rr.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestAmpProviderModelRoutes(t *testing.T) {
	testCases := []struct {
		name         string
		path         string
		wantStatus   int
		wantContains string
	}{
		{
			name:         "openai root models",
			path:         "/api/provider/openai/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "groq root models",
			path:         "/api/provider/groq/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "openai models",
			path:         "/api/provider/openai/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "anthropic models",
			path:         "/api/provider/anthropic/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"data"`,
		},
		{
			name:         "google models v1",
			path:         "/api/provider/google/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
		{
			name:         "google models v1beta",
			path:         "/api/provider/google/v1beta/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer test-key")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", tc.path, rr.Code, tc.wantStatus, rr.Body.String())
			}
			if body := rr.Body.String(); !strings.Contains(body, tc.wantContains) {
				t.Fatalf("response body for %s missing %q: %s", tc.path, tc.wantContains, body)
			}
		})
	}
}

func TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")

	originalWD, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatalf("failed to get current working directory: %v", errGetwd)
	}

	tmpDir := t.TempDir()
	if errChdir := os.Chdir(tmpDir); errChdir != nil {
		t.Fatalf("failed to switch working directory: %v", errChdir)
	}
	defer func() {
		if errChdirBack := os.Chdir(originalWD); errChdirBack != nil {
			t.Fatalf("failed to restore working directory: %v", errChdirBack)
		}
	}()

	// Force ResolveLogDirectory to fallback to auth-dir/logs by making ./logs not a writable directory.
	if errWriteFile := os.WriteFile(filepath.Join(tmpDir, "logs"), []byte("not-a-directory"), 0o644); errWriteFile != nil {
		t.Fatalf("failed to create blocking logs file: %v", errWriteFile)
	}

	configDir := filepath.Join(tmpDir, "config")
	if errMkdirConfig := os.MkdirAll(configDir, 0o755); errMkdirConfig != nil {
		t.Fatalf("failed to create config dir: %v", errMkdirConfig)
	}
	configPath := filepath.Join(configDir, "config.yaml")

	authDir := filepath.Join(tmpDir, "auth")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: proxyconfig.SDKConfig{
			RequestLog: false,
		},
		AuthDir:           authDir,
		ErrorLogsMaxFiles: 10,
	}

	logger := defaultRequestLoggerFactory(cfg, configPath)
	fileLogger, ok := logger.(*internallogging.FileRequestLogger)
	if !ok {
		t.Fatalf("expected *FileRequestLogger, got %T", logger)
	}

	errLog := fileLogger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"issue-1711",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("failed to write forced error request log: %v", errLog)
	}

	authLogsDir := filepath.Join(authDir, "logs")
	authEntries, errReadAuthDir := os.ReadDir(authLogsDir)
	if errReadAuthDir != nil {
		t.Fatalf("failed to read auth logs dir %s: %v", authLogsDir, errReadAuthDir)
	}
	foundErrorLogInAuthDir := false
	for _, entry := range authEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			foundErrorLogInAuthDir = true
			break
		}
	}
	if !foundErrorLogInAuthDir {
		t.Fatalf("expected forced error log in auth fallback dir %s, got entries: %+v", authLogsDir, authEntries)
	}

	configLogsDir := filepath.Join(configDir, "logs")
	configEntries, errReadConfigDir := os.ReadDir(configLogsDir)
	if errReadConfigDir != nil && !os.IsNotExist(errReadConfigDir) {
		t.Fatalf("failed to inspect config logs dir %s: %v", configLogsDir, errReadConfigDir)
	}
	for _, entry := range configEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("unexpected forced error log in config dir %s", configLogsDir)
		}
	}
}
