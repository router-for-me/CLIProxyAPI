package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

func TestShouldSkipMethodForRequestLogging(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
		skip bool
	}{
		{
			name: "nil request",
			req:  nil,
			skip: true,
		},
		{
			name: "post request should not skip",
			req: &http.Request{
				Method: http.MethodPost,
				URL:    &url.URL{Path: "/v1/responses"},
			},
			skip: false,
		},
		{
			name: "plain get should skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/models"},
				Header: http.Header{},
			},
			skip: true,
		},
		{
			name: "responses websocket upgrade should not skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/responses"},
				Header: http.Header{"Upgrade": []string{"websocket"}},
			},
			skip: false,
		},
		{
			name: "codex responses websocket upgrade should not skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/backend-api/codex/responses"},
				Header: http.Header{"Upgrade": []string{"websocket"}},
			},
			skip: false,
		},
		{
			name: "responses get without upgrade should skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/responses"},
				Header: http.Header{},
			},
			skip: true,
		},
	}

	for i := range tests {
		got := shouldSkipMethodForRequestLogging(tests[i].req)
		if got != tests[i].skip {
			t.Fatalf("%s: got skip=%t, want %t", tests[i].name, got, tests[i].skip)
		}
	}
}

func TestRequestAPIKey(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		query  string
		want   string
	}{
		{name: "bearer", header: http.Header{"Authorization": []string{"Bearer bearer-key"}}, want: "bearer-key"},
		{name: "anthropic", header: http.Header{"X-Api-Key": []string{"anthropic-key"}}, want: "anthropic-key"},
		{name: "google", header: http.Header{"X-Goog-Api-Key": []string{"google-key"}}, want: "google-key"},
		{name: "query", query: "key=query-key", want: "query-key"},
		{name: "missing", want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses?"+test.query, nil)
			req.Header = test.header
			if got := requestAPIKey(req); got != test.want {
				t.Fatalf("requestAPIKey() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveRequestLogScope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("plugin key_id metadata", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		c.Request.Header.Set("Authorization", "Bearer cpa_randomsecretvalue1234567890")
		c.Set("userApiKey", "team-a")
		c.Set("accessMetadata", map[string]string{"key_id": "team-a", "provider": "cpa-key-policy"})
		value, asLabel := resolveRequestLogScope(c)
		if value != "team-a" || !asLabel {
			t.Fatalf("resolveRequestLogScope() = (%q, %v), want (team-a, true)", value, asLabel)
		}
	})

	t.Run("principal differs from raw secret", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		c.Request.Header.Set("Authorization", "Bearer cpa_randomsecretvalue1234567890")
		c.Set("userApiKey", "billing-bot")
		value, asLabel := resolveRequestLogScope(c)
		if value != "billing-bot" || !asLabel {
			t.Fatalf("resolveRequestLogScope() = (%q, %v), want (billing-bot, true)", value, asLabel)
		}
	})

	t.Run("native api key principal equals secret", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		c.Request.Header.Set("Authorization", "Bearer native-secret")
		c.Set("userApiKey", "native-secret")
		c.Set("accessMetadata", map[string]string{"source": "authorization"})
		value, asLabel := resolveRequestLogScope(c)
		if value != "native-secret" || asLabel {
			t.Fatalf("resolveRequestLogScope() = (%q, %v), want (native-secret, false)", value, asLabel)
		}
	})
}

func TestRequestLoggingMiddlewareUsesPluginKeyIDDirectory(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logsDir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, logsDir, "", 10)
	plainKey := "cpa_randomsecretvalue1234567890abcdef"

	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		// Simulate AuthMiddleware after a successful cpa-key-policy authenticate.
		c.Set("userApiKey", "team-a")
		c.Set("accessProvider", "plugin:cpa-key-policy:cpa-key-policy")
		c.Set("accessMetadata", map[string]string{
			"key_id":   "team-a",
			"provider": "cpa-key-policy",
		})
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-5.4"}`)))
	request.Header.Set("Authorization", "Bearer "+plainKey)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusOK)
	}

	keyDir := filepath.Join(logsDir, "keys", "team-a")
	entries, errReadDir := os.ReadDir(keyDir)
	if errReadDir != nil {
		t.Fatalf("expected log dir %s: %v", keyDir, errReadDir)
	}
	found := false
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no request log under %s; entries=%v", keyDir, entries)
	}

	// Must not only land under the secret fingerprint directory.
	hashed := filepath.Join(logsDir, "keys", logging.APIKeyLogDirectory(plainKey))
	if hashed == keyDir {
		t.Fatal("hashed directory unexpectedly equals key-id directory")
	}
}

func TestShouldCaptureRequestBody(t *testing.T) {
	tests := []struct {
		name          string
		loggerEnabled bool
		req           *http.Request
		want          bool
	}{
		{
			name:          "logger enabled always captures",
			loggerEnabled: true,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: -1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: true,
		},
		{
			name:          "nil request",
			loggerEnabled: false,
			req:           nil,
			want:          false,
		},
		{
			name:          "small known size json in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: 2,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: true,
		},
		{
			name:          "large known size skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: maxErrorOnlyCapturedRequestBodyBytes + 1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "unknown size skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: -1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "multipart skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: 1,
				Header:        http.Header{"Content-Type": []string{"multipart/form-data; boundary=abc"}},
			},
			want: false,
		},
	}

	for i := range tests {
		got := shouldCaptureRequestBody(tests[i].loggerEnabled, tests[i].req)
		if got != tests[i].want {
			t.Fatalf("%s: got %t, want %t", tests[i].name, got, tests[i].want)
		}
	}
}

func TestDeferredRequestBodyCaptureDoesNotDrainUnreadBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logger := logging.NewFileRequestLogger(false, t.TempDir(), "", 10)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("remaining-body"))
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/json")
	requestInfo := &RequestInfo{Headers: map[string][]string{"Content-Type": {"application/json"}}}
	capture := attachDeferredRequestBodyCapture(request, logger, requestInfo, false, false)
	if capture == nil {
		t.Fatal("deferred request body capture was not attached")
	}
	defer capture.Cleanup()

	firstByte := make([]byte, 1)
	if _, errRead := request.Body.Read(firstByte); errRead != nil {
		t.Fatalf("read first request byte: %v", errRead)
	}
	captured, marker, errCaptured := capture.Bytes()
	if errCaptured != nil {
		t.Fatalf("read captured body: %v", errCaptured)
	}
	if string(captured) != "r" {
		t.Fatalf("captured body = %q, want %q", string(captured), "r")
	}
	if !strings.Contains(marker, "REQUEST BODY CAPTURE INCOMPLETE") {
		t.Fatalf("capture marker = %q, want incomplete marker", marker)
	}
	remaining, errRemaining := io.ReadAll(capture.body)
	if errRemaining != nil {
		t.Fatalf("read remaining body: %v", errRemaining)
	}
	if string(remaining) != "emaining-body" {
		t.Fatalf("remaining body = %q, want %q", string(remaining), "emaining-body")
	}
}

func TestRequestLoggingMiddlewareCapturesLargeErrorRequestAndDeferredAPIRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logsDir := t.TempDir()
	logger := logging.NewFileRequestLogger(false, logsDir, "", 10)
	payload := append([]byte(`{"marker":"large-error-body","padding":"`), bytes.Repeat([]byte("x"), int(maxErrorOnlyCapturedRequestBodyBytes))...)
	payload = append(payload, []byte(`"}`)...)
	upstreamBody := []byte(`{"model":"upstream-model","input":"translated"}`)

	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger))
	router.POST("/v1/responses", func(c *gin.Context) {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		if !bytes.Equal(body, payload) {
			c.Status(http.StatusInternalServerError)
			return
		}
		executorCtx := context.WithValue(context.Background(), "gin", c)
		helps.RecordAPIRequest(executorCtx, &config.Config{}, helps.UpstreamRequestLog{
			URL:     "https://api.example.com/v1/responses",
			Method:  http.MethodPost,
			Headers: http.Header{"Content-Type": []string{"application/json"}},
			Body:    upstreamBody,
		})
		c.JSON(http.StatusBadRequest, gin.H{"error": "upstream rejected request"})
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	requestLogsDir := filepath.Join(logsDir, "keys", logging.APIKeyLogDirectory(""))
	entries, errReadDir := os.ReadDir(requestLogsDir)
	if errReadDir != nil {
		t.Fatalf("read logs dir: %v", errReadDir)
	}
	var logPath string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			logPath = filepath.Join(requestLogsDir, entry.Name())
			break
		}
	}
	if logPath == "" {
		t.Fatal("forced error log was not created")
	}
	content, errReadLog := os.ReadFile(logPath)
	if errReadLog != nil {
		t.Fatalf("read error log: %v", errReadLog)
	}
	if !bytes.Contains(content, payload) {
		t.Fatal("error log does not contain the complete large request body")
	}
	if !bytes.Contains(content, []byte("=== API REQUEST 1 ===")) {
		t.Fatal("error log does not contain the deferred API request section")
	}
	if !bytes.Contains(content, upstreamBody) {
		t.Fatal("error log does not contain the deferred upstream request body")
	}
}

func TestAttachRequestLogSourcesUsesLoggerLogsDir(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logsDir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, logsDir, "", 0)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/codex/responses", nil)
	c.Request.Header.Set("Upgrade", "websocket")

	attachRequestLogSources(c, logger, true)
	defer cleanupFileBodySourcesFromContext(c)

	for _, key := range []string{
		logging.WebsocketTimelineSourceContextKey,
		logging.APIWebsocketTimelineSourceContextKey,
	} {
		value, exists := c.Get(key)
		if !exists {
			t.Fatalf("expected %s source to be attached", key)
		}
		source, ok := value.(*logging.FileBodySource)
		if !ok || source == nil {
			t.Fatalf("%s source type = %T", key, value)
		}
		file, errPart := source.CreatePart("probe")
		if errPart != nil {
			t.Fatalf("CreatePart(%s): %v", key, errPart)
		}
		path := file.Name()
		if errClose := file.Close(); errClose != nil {
			t.Fatalf("close part: %v", errClose)
		}
		if !strings.HasPrefix(path, logsDir+string(os.PathSeparator)) {
			t.Fatalf("%s part path %s is not under logs dir %s", key, path, logsDir)
		}
	}
}

func cleanupFileBodySourcesFromContext(c *gin.Context) {
	if c == nil {
		return
	}
	for _, key := range []string{
		logging.WebsocketTimelineSourceContextKey,
		logging.APIWebsocketTimelineSourceContextKey,
	} {
		value, exists := c.Get(key)
		if !exists {
			continue
		}
		if source, ok := value.(*logging.FileBodySource); ok && source != nil {
			_ = source.Cleanup()
		}
	}
}

func TestDecodeCapturedRequestBodyForLogWithLimitTruncatesZstdExpansion(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1024)
	var compressed bytes.Buffer
	encoder, errNewWriter := zstd.NewWriter(&compressed)
	if errNewWriter != nil {
		t.Fatalf("zstd.NewWriter: %v", errNewWriter)
	}
	if _, errWrite := encoder.Write(payload); errWrite != nil {
		t.Fatalf("zstd write: %v", errWrite)
	}
	if errClose := encoder.Close(); errClose != nil {
		t.Fatalf("zstd close: %v", errClose)
	}

	decoded := decodeCapturedRequestBodyForLogWithLimit(compressed.Bytes(), "zstd", 64)
	if len(decoded) > 128 {
		t.Fatalf("limited decoded body length = %d, want bounded output", len(decoded))
	}
	if !bytes.Contains(decoded, []byte("DECOMPRESSED REQUEST BODY TRUNCATED")) {
		t.Fatalf("decoded body = %q, want truncation marker", string(decoded))
	}
}

func TestCaptureRequestInfoDecodesZstdRequestBodyForLog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	payload := []byte(`{"model":"test-model","stream":true}`)
	var compressed bytes.Buffer
	encoder, errNewWriter := zstd.NewWriter(&compressed)
	if errNewWriter != nil {
		t.Fatalf("zstd.NewWriter: %v", errNewWriter)
	}
	if _, errWrite := encoder.Write(payload); errWrite != nil {
		t.Fatalf("zstd write: %v", errWrite)
	}
	if errClose := encoder.Close(); errClose != nil {
		t.Fatalf("zstd close: %v", errClose)
	}
	compressedBytes := compressed.Bytes()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressedBytes))
	req.Header.Set("Content-Encoding", "zstd")
	c.Request = req

	info, errCapture := captureRequestInfo(c, true)
	if errCapture != nil {
		t.Fatalf("captureRequestInfo: %v", errCapture)
	}
	if !bytes.Equal(info.Body, payload) {
		t.Fatalf("logged request body = %q, want %q", string(info.Body), string(payload))
	}

	restoredBody, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		t.Fatalf("read restored request body: %v", errRead)
	}
	if !bytes.Equal(restoredBody, compressedBytes) {
		t.Fatal("request body was not restored with the original compressed bytes")
	}
}
