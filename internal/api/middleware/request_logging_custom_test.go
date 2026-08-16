package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func assertNoRequestLogArtifacts(t *testing.T, logsDir string) {
	t.Helper()
	var artifacts []string
	errWalk := filepath.Walk(logsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path != logsDir {
			artifacts = append(artifacts, path)
		}
		return nil
	})
	if errWalk != nil {
		t.Fatalf("walk logs dir: %v", errWalk)
	}
	if len(artifacts) != 0 {
		t.Fatalf("no-log key created artifacts: %v", artifacts)
	}
}

func TestRequestLoggingMiddlewareNoLogAPIKeyCreatesNoArtifacts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, logsDir, "", 10)
	noLogKey := "cpa_nologsecretvalue1234567890"
	logger.SetNoLogAPIKeys([]string{noLogKey})

	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-5.4"}`)))
	request.Header.Set("Authorization", "Bearer "+noLogKey)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusOK)
	}
	assertNoRequestLogArtifacts(t, logsDir)
}

func TestRequestLoggingMiddlewareNoLogAPIKeySkipsForcedErrorLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	logger := logging.NewFileRequestLogger(false, logsDir, "", 10)
	noLogKey := "cpa_nologsecretvalue1234567890"
	logger.SetNoLogAPIKeys([]string{noLogKey})

	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream unavailable"})
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-5.4"}`)))
	request.Header.Set("Authorization", "Bearer "+noLogKey)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	assertNoRequestLogArtifacts(t, logsDir)
}

func TestRequestLoggingMiddlewareUsesPluginKeyIDDirectoryForStreamingResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, logsDir, "", 10)
	rawKey := "cpa_streamsecretvalue1234567890"

	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger))
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Set("userApiKey", "team-stream")
		c.Set("accessMetadata", map[string]string{"key_id": "team-stream", "provider": "cpa-key-policy"})
		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5.4","stream":true}`)))
	request.Header.Set("Authorization", "Bearer "+rawKey)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusOK)
	}

	keyDir := filepath.Join(logsDir, "keys", "team-stream")
	entries, errRead := os.ReadDir(keyDir)
	if errRead != nil {
		t.Fatalf("read key directory: %v", errRead)
	}
	foundLog := false
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") {
			foundLog = true
		}
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("streaming temp artifact remained: %s", entry.Name())
		}
	}
	if !foundLog {
		t.Fatalf("no streaming log under %s", keyDir)
	}
	rawKeyDir := filepath.Join(logsDir, "keys", logging.APIKeyLogDirectory(rawKey))
	rawKeyEntries, errReadRawKeyDir := os.ReadDir(rawKeyDir)
	if errReadRawKeyDir != nil {
		if !os.IsNotExist(errReadRawKeyDir) {
			t.Fatalf("read raw-key fingerprint directory: %v", errReadRawKeyDir)
		}
	} else if len(rawKeyEntries) != 0 {
		t.Fatalf("raw-key fingerprint directory contains artifacts: %v", rawKeyEntries)
	}
}

type requestLoggerWithoutPrivacyPolicy struct {
	logging.RequestLogger
}

func TestRequestLoggingMiddlewareWithoutPrivacyCheckerFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	logger := &requestLoggerWithoutPrivacyPolicy{RequestLogger: logging.NewFileRequestLogger(true, logsDir, "", 10)}
	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "forced"})
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-5.4"}`)))
	request.Header.Set("Authorization", "Bearer policy-unavailable-key")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	assertNoRequestLogArtifacts(t, logsDir)
}

func TestRequestLoggingMiddlewareTypedNilPolicyFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, logsDir, "", 10)
	var policy *logging.RequestLogPolicy
	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger, policy))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "forced"})
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-5.4"}`)))
	request.Header.Set("Authorization", "Bearer typed-nil-policy-key")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	assertNoRequestLogArtifacts(t, logsDir)
}

func TestRequestLoggingMiddlewareUsesServerOwnedNoLogPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, logsDir, "", 10)
	noLogKey := "server-owned-no-log-key"
	policy := logging.NewRequestLogPolicy([]string{noLogKey})

	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger, policy))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "forced"})
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-5.4"}`)))
	request.Header.Set("Authorization", "Bearer "+noLogKey)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	assertNoRequestLogArtifacts(t, logsDir)
}

func TestRequestLoggingMiddlewareTypedNilLoggerPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	var logger *logging.FileRequestLogger
	handlerCalled := false

	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		handlerCalled = true
		c.JSON(http.StatusAccepted, gin.H{"ok": true})
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-5.4"}`)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if !handlerCalled {
		t.Fatal("handler was not called")
	}
	if response.Code != http.StatusAccepted {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusAccepted)
	}
	if got := strings.TrimSpace(response.Body.String()); got != `{"ok":true}` {
		t.Fatalf("response body = %q, want %q", got, `{"ok":true}`)
	}
	assertNoRequestLogArtifacts(t, logsDir)
}
