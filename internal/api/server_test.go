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
	internalusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
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
		RemoteManagement:       proxyconfig.RemoteManagement{DisableControlPanel: true},
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}

func TestInjectManagementConfigVersionGuard(t *testing.T) {
	html := []byte("<html><body><main>ok</main></body></html>")

	out := injectManagementConfigVersionGuard(html, "sha256:test-version")

	if !strings.Contains(string(out), "cliproxy-config-version-guard") {
		t.Fatalf("expected injected guard script, got %s", string(out))
	}
	if !strings.Contains(string(out), `var latestConfigVersion = "sha256:test-version"`) {
		t.Fatalf("expected initial config version in guard script: %s", string(out))
	}
	if strings.Index(string(out), "cliproxy-config-version-guard") > strings.Index(string(out), "</body>") {
		t.Fatalf("guard script should be injected before closing body: %s", string(out))
	}
	again := injectManagementConfigVersionGuard(out, "sha256:test-version")
	if string(again) != string(out) {
		t.Fatalf("guard injection should be idempotent")
	}
}

func TestInjectManagementConfigVersionGuardReplacesOldGuard(t *testing.T) {
	html := []byte(`<html><body><script id="cliproxy-config-version-guard">old guard</script><main>ok</main></body></html>`)

	out := injectManagementConfigVersionGuard(html)
	body := string(out)

	if strings.Contains(body, "old guard") {
		t.Fatalf("expected old guard script to be replaced: %s", body)
	}
	if strings.Count(body, "cliproxy-config-version-guard") != 1 {
		t.Fatalf("expected exactly one guard script, got %s", body)
	}
	if !strings.Contains(body, "writeQueue") {
		t.Fatalf("expected serialized write queue in guard script: %s", body)
	}
	if !strings.Contains(body, "cliproxy:config-conflict") {
		t.Fatalf("expected conflict event publishing in guard script: %s", body)
	}
	if !strings.Contains(body, "response && response.status === 409") {
		t.Fatalf("expected guard to avoid promoting conflict versions to writable versions: %s", body)
	}
}

func TestUsagePersistenceEnabledHotReload(t *testing.T) {
	t.Setenv("PGSTORE_DSN", "")
	t.Setenv("pgstore_dsn", "")
	t.Setenv("PGSTORE_SCHEMA", "")
	t.Setenv("pgstore_schema", "")

	internalusage.CloseDatabasePlugin()
	defer internalusage.CloseDatabasePlugin()

	server := newTestServer(t)

	disabled := *server.cfg
	disabled.UsagePersistenceEnabled = false
	server.UpdateClients(&disabled)
	if internalusage.GetDatabasePlugin() != nil {
		t.Fatalf("expected database plugin to be nil when disabled")
	}

	enabled := disabled
	enabled.UsagePersistenceEnabled = true
	server.UpdateClients(&enabled)
	firstPlugin := internalusage.GetDatabasePlugin()
	if firstPlugin == nil {
		t.Fatalf("expected database plugin to be initialized when enabled")
	}
	if _, err := os.Stat(filepath.Join(enabled.AuthDir, "usage.db")); err != nil {
		t.Fatalf("expected sqlite usage db to exist: %v", err)
	}

	disabledAgain := enabled
	disabledAgain.UsagePersistenceEnabled = false
	server.UpdateClients(&disabledAgain)
	if internalusage.GetDatabasePlugin() != nil {
		t.Fatalf("expected database plugin to be nil after disabling")
	}

	enabledAgain := disabledAgain
	enabledAgain.UsagePersistenceEnabled = true
	server.UpdateClients(&enabledAgain)
	secondPlugin := internalusage.GetDatabasePlugin()
	if secondPlugin == nil {
		t.Fatalf("expected database plugin to be initialized after re-enabling")
	}
	if secondPlugin == firstPlugin {
		t.Fatalf("expected database plugin to be re-initialized after re-enabling")
	}
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

func TestUnsupportedOpenAIAudioEndpoints(t *testing.T) {
	server := newTestServer(t)
	paths := []string{
		"/v1/audio/transcriptions",
		"/v1/audio/translations",
		"/v1/audio/speech",
	}

	for _, path := range paths {
		path := path
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5.5"}`))
			req.Header.Set("Authorization", "Bearer test-key")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", path, rr.Code, http.StatusBadRequest, rr.Body.String())
			}

			var resp struct {
				Error struct {
					Code string `json:"code"`
					Type string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
			}
			if resp.Error.Code != "unsupported_endpoint" {
				t.Fatalf("unexpected error code for %s: got %q", path, resp.Error.Code)
			}
			if resp.Error.Type != "invalid_request_error" {
				t.Fatalf("unexpected error type for %s: got %q", path, resp.Error.Type)
			}
		})
	}
}

func TestUnifiedModelsHandlerRoutesClaudeCompatibleClients(t *testing.T) {
	testCases := []struct {
		name         string
		userAgent    string
		anthropicVer string
		wantClaude   bool
	}{
		{
			name:       "claude cli",
			userAgent:  "claude-cli/2.1.70 (external, cli)",
			wantClaude: true,
		},
		{
			name:       "anthropic js sdk",
			userAgent:  "Anthropic/JS 0.91.1",
			wantClaude: true,
		},
		{
			name:         "anthropic version header",
			userAgent:    "node",
			anthropicVer: "2023-06-01",
			wantClaude:   true,
		},
		{
			name:       "openai client",
			userAgent:  "curl/8.7.1",
			wantClaude: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)

			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("User-Agent", tc.userAgent)
			if tc.anthropicVer != "" {
				req.Header.Set("Anthropic-Version", tc.anthropicVer)
			}

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
			}

			var resp map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
			}
			_, hasMore := resp["has_more"]
			_, hasObject := resp["object"]
			if tc.wantClaude && (!hasMore || hasObject) {
				t.Fatalf("expected Claude models response, got %s", rr.Body.String())
			}
			if !tc.wantClaude && (!hasObject || hasMore) {
				t.Fatalf("expected OpenAI models response, got %s", rr.Body.String())
			}
		})
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
