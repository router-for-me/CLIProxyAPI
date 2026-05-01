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
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
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
	if err := os.WriteFile(configPath, []byte("api-keys:\n  - test-key\n"), 0o644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	return NewServer(cfg, authManager, accessManager, configPath)
}

func newConfiguredTestServer(t *testing.T, mutate func(*proxyconfig.Config), opts ...ServerOption) *Server {
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
	if mutate != nil {
		mutate(cfg)
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("api-keys:\n  - test-key\n"), 0o644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	return NewServer(cfg, authManager, accessManager, configPath, opts...)
}

func usageTestRecord() coreusage.Record {
	return coreusage.Record{
		Provider: "provider-a",
		Model:    "model-a",
		APIKey:   "key-a",
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
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

func TestNewServer_UsageStatisticsModeOffDisablesAggregation(t *testing.T) {
	prevEnabled := usage.StatisticsEnabled()
	usage.SetStatisticsEnabled(true)
	t.Cleanup(func() {
		usage.SetStatisticsEnabled(prevEnabled)
	})

	stats := usage.GetRequestStatistics()
	stats.SetOnChange(nil)
	stats.ResetAll()
	t.Cleanup(func() {
		stats.SetOnChange(nil)
		stats.ResetAll()
	})

	_ = newConfiguredTestServer(t, func(cfg *proxyconfig.Config) {
		cfg.UsageStatistics.Mode = "off"
		cfg.UsageStatisticsEnabled = true
	})

	if usage.StatisticsEnabled() {
		t.Fatalf("expected usage aggregation to be disabled when mode is off")
	}
}

func TestNewServer_UsageStatisticsModePersistentLoadsSnapshot(t *testing.T) {
	prevEnabled := usage.StatisticsEnabled()
	usage.SetStatisticsEnabled(true)
	t.Cleanup(func() {
		usage.SetStatisticsEnabled(prevEnabled)
	})

	stats := usage.GetRequestStatistics()
	stats.SetOnChange(nil)
	stats.ResetAll()
	t.Cleanup(func() {
		stats.SetOnChange(nil)
		stats.ResetAll()
	})

	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "usage-statistics.json")
	store := usage.NewSnapshotStore(snapshotPath)
	seed := usage.NewRequestStatistics()
	seed.Record(nil, usageTestRecord())
	if err := store.Save(seed.Snapshot()); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	_ = newConfiguredTestServer(t, func(cfg *proxyconfig.Config) {
		cfg.UsageStatistics.Mode = "persistent"
	}, WithUsageStatisticsSnapshotPath(snapshotPath))

	snapshot := usage.GetRequestStatistics().Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("expected restored snapshot, got %d requests", snapshot.TotalRequests)
	}
}

func TestNewServer_UsageStatisticsModePersistentKeepsMalformedSnapshot(t *testing.T) {
	prevEnabled := usage.StatisticsEnabled()
	usage.SetStatisticsEnabled(true)
	t.Cleanup(func() {
		usage.SetStatisticsEnabled(prevEnabled)
	})

	stats := usage.GetRequestStatistics()
	stats.SetOnChange(nil)
	stats.ResetAll()
	t.Cleanup(func() {
		stats.SetOnChange(nil)
		stats.ResetAll()
	})

	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "usage-statistics.json")
	malformed := []byte("{malformed")
	if err := os.WriteFile(snapshotPath, malformed, 0o600); err != nil {
		t.Fatalf("write malformed snapshot: %v", err)
	}

	_ = newConfiguredTestServer(t, func(cfg *proxyconfig.Config) {
		cfg.UsageStatistics.Mode = "persistent"
	}, WithUsageStatisticsSnapshotPath(snapshotPath))

	got, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read malformed snapshot after server start: %v", err)
	}
	if string(got) != string(malformed) {
		t.Fatalf("server start changed malformed snapshot to %q", string(got))
	}
}

func TestNewServer_UsageStatisticsModePersistentSavesMergedInMemorySnapshotImmediately(t *testing.T) {
	prevEnabled := usage.StatisticsEnabled()
	usage.SetStatisticsEnabled(true)
	t.Cleanup(func() {
		usage.SetStatisticsEnabled(prevEnabled)
	})

	stats := usage.GetRequestStatistics()
	stats.SetOnChange(nil)
	stats.ResetAll()
	t.Cleanup(func() {
		stats.SetOnChange(nil)
		stats.ResetAll()
	})

	stats.Record(nil, coreusage.Record{
		Provider: "provider-memory",
		Model:    "model-memory",
		APIKey:   "key-memory",
		Detail: coreusage.Detail{
			TotalTokens: 7,
		},
	})

	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "usage-statistics.json")
	store := usage.NewSnapshotStore(snapshotPath)
	seed := usage.NewRequestStatistics()
	seed.Record(nil, usageTestRecord())
	if err := store.Save(seed.Snapshot()); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	_ = newConfiguredTestServer(t, func(cfg *proxyconfig.Config) {
		cfg.UsageStatistics.Mode = "persistent"
	}, WithUsageStatisticsSnapshotPath(snapshotPath))

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load merged snapshot: %v", err)
	}
	if loaded.TotalRequests != 2 {
		t.Fatalf("expected merged snapshot to be saved with 2 requests, got %d", loaded.TotalRequests)
	}
}

func TestUsageStatisticsModeEndpointAppliesImmediately(t *testing.T) {
	prevEnabled := usage.StatisticsEnabled()
	usage.SetStatisticsEnabled(false)
	t.Cleanup(func() {
		usage.SetStatisticsEnabled(prevEnabled)
	})

	server := newConfiguredTestServer(t, func(cfg *proxyconfig.Config) {
		cfg.UsageStatistics.Mode = "off"
	}, WithLocalManagementPassword("local-test-password"))

	body := strings.NewReader(`{"value":"memory"}`)
	req := httptest.NewRequest(http.MethodPut, "/v0/management/usage-statistics-mode", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Management-Key", "local-test-password")
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !usage.StatisticsEnabled() {
		t.Fatalf("expected usage statistics mode update to apply immediately")
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
