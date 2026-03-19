package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
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
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
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

func TestCORSMiddleware_RestrictsOrigins(t *testing.T) {
	testCases := []struct {
		name            string
		method          string
		origin          string
		wantStatus      int
		wantAllowOrigin string
		wantAllowHeader string
	}{
		{
			name:            "no origin keeps headers empty",
			method:          http.MethodGet,
			wantStatus:      http.StatusOK,
			wantAllowOrigin: "",
			wantAllowHeader: "",
		},
		{
			name:            "localhost origin is allowed",
			method:          http.MethodGet,
			origin:          "http://localhost:3000",
			wantStatus:      http.StatusOK,
			wantAllowOrigin: "http://localhost:3000",
			wantAllowHeader: corsAllowedHeaders,
		},
		{
			name:            "loopback preflight is allowed",
			method:          http.MethodOptions,
			origin:          "http://127.0.0.1:5173",
			wantStatus:      http.StatusNoContent,
			wantAllowOrigin: "http://127.0.0.1:5173",
			wantAllowHeader: corsAllowedHeaders,
		},
		{
			name:            "remote origin is stripped",
			method:          http.MethodGet,
			origin:          "https://evil.example",
			wantStatus:      http.StatusOK,
			wantAllowOrigin: "",
			wantAllowHeader: "",
		},
		{
			name:            "remote preflight is denied",
			method:          http.MethodOptions,
			origin:          "https://evil.example",
			wantStatus:      http.StatusForbidden,
			wantAllowOrigin: "",
			wantAllowHeader: "",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			engine.Use(corsMiddleware())
			engine.Any("/resource", func(c *gin.Context) {
				c.Header("Access-Control-Allow-Origin", "*")
				c.Header("Access-Control-Allow-Headers", "*")
				c.Header("Access-Control-Allow-Methods", "*")
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(tc.method, "/resource", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}

			rr := httptest.NewRecorder()
			engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if got := rr.Header().Get("Access-Control-Allow-Origin"); got != tc.wantAllowOrigin {
				t.Fatalf("allow origin = %q, want %q", got, tc.wantAllowOrigin)
			}
			if got := rr.Header().Get("Access-Control-Allow-Headers"); got != tc.wantAllowHeader {
				t.Fatalf("allow headers = %q, want %q", got, tc.wantAllowHeader)
			}
			if tc.wantAllowOrigin != "" {
				if got := rr.Header().Get("Access-Control-Allow-Methods"); got != corsAllowedMethods {
					t.Fatalf("allow methods = %q, want %q", got, corsAllowedMethods)
				}
			}
		})
	}
}

func TestOAuthCallbackWriteErrorsAreNotIgnored(t *testing.T) {
	testCases := []struct {
		name       string
		authDir    string
		state      string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "successful callback persists state",
			authDir:    "use-server-auth-dir",
			state:      "oauth-callback-success",
			wantStatus: http.StatusOK,
			wantBody:   "Authentication successful",
		},
		{
			name:       "write failure returns server error",
			authDir:    "",
			state:      "oauth-callback-write-error",
			wantStatus: http.StatusInternalServerError,
			wantBody:   "Authentication callback failed",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			if tc.authDir != "use-server-auth-dir" {
				server.cfg.AuthDir = tc.authDir
			}

			managementHandlers.RegisterOAuthSession(tc.state, "anthropic")

			req := httptest.NewRequest(
				http.MethodGet,
				"/anthropic/callback?state="+url.QueryEscape(tc.state)+"&code=test-code",
				nil,
			)
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if body := rr.Body.String(); !strings.Contains(body, tc.wantBody) {
				t.Fatalf("body = %q, want substring %q", body, tc.wantBody)
			}
			if tc.wantStatus == http.StatusOK {
				callbackPath := filepath.Join(server.cfg.AuthDir, ".oauth-anthropic-"+tc.state+".oauth")
				if _, err := os.Stat(callbackPath); err != nil {
					t.Fatalf("expected callback file %s: %v", callbackPath, err)
				}
			}
		})
	}
}

func TestGeminiCompatibleRoutesAcceptQueryKeyCompatibility(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1beta/models?key=test-key", nil)
	req.RemoteAddr = "198.51.100.21:1234"

	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "\"models\"") {
		t.Fatalf("body = %q, want models payload", body)
	}
}

func TestOpenAIRoutesStillRejectQueryKeyOnly(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/models?key=test-key", nil)
	req.RemoteAddr = "198.51.100.22:1234"

	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
}

func TestProtectedRoutesRejectNilAccessManager(t *testing.T) {
	server := newTestServerWithAccessManager(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "198.51.100.20:1234"

	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "authentication service unavailable") {
		t.Fatalf("body = %q, want authentication service unavailable", body)
	}
}

func TestProtectedRoutesRateLimitAuthenticationFailures(t *testing.T) {
	apiAuthFailureLimiter.ResetAll()
	t.Cleanup(apiAuthFailureLimiter.ResetAll)

	server := newTestServer(t)
	remoteAddr := "198.51.100.30:1234"

	for attempt := 1; attempt < authFailureLimit; attempt++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set("Authorization", "Bearer wrong-key")

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want %d; body=%s", attempt, rr.Code, http.StatusUnauthorized, rr.Body.String())
		}
	}

	rateLimitedReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rateLimitedReq.RemoteAddr = remoteAddr
	rateLimitedReq.Header.Set("Authorization", "Bearer wrong-key")

	rateLimitedResp := httptest.NewRecorder()
	server.engine.ServeHTTP(rateLimitedResp, rateLimitedReq)

	if rateLimitedResp.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limited status = %d, want %d; body=%s", rateLimitedResp.Code, http.StatusTooManyRequests, rateLimitedResp.Body.String())
	}
	if retryAfter := rateLimitedResp.Header().Get("Retry-After"); retryAfter == "" {
		t.Fatal("expected Retry-After header on rate-limited response")
	}

	successReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	successReq.RemoteAddr = remoteAddr
	successReq.Header.Set("Authorization", "Bearer test-key")

	successResp := httptest.NewRecorder()
	server.engine.ServeHTTP(successResp, successReq)

	if successResp.Code != http.StatusOK {
		t.Fatalf("success status = %d, want %d; body=%s", successResp.Code, http.StatusOK, successResp.Body.String())
	}

	badReqAfterSuccess := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	badReqAfterSuccess.RemoteAddr = remoteAddr
	badReqAfterSuccess.Header.Set("Authorization", "Bearer wrong-key")

	badRespAfterSuccess := httptest.NewRecorder()
	server.engine.ServeHTTP(badRespAfterSuccess, badReqAfterSuccess)

	if badRespAfterSuccess.Code != http.StatusUnauthorized {
		t.Fatalf("post-success bad status = %d, want %d; body=%s", badRespAfterSuccess.Code, http.StatusUnauthorized, badRespAfterSuccess.Body.String())
	}
}

func newTestServerWithAccessManager(t *testing.T, accessManager *sdkaccess.Manager) *Server {
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
	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}
