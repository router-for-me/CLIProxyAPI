package middleware

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
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
			name:          "small known size json skipped for eager capture in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: 2,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
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
	entries, errReadDir := os.ReadDir(logsDir)
	if errReadDir != nil {
		t.Fatalf("read logs dir: %v", errReadDir)
	}
	var logPath string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			logPath = logsDir + string(os.PathSeparator) + entry.Name()
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

func TestRequestLoggingMiddleware_StreamingResponsesUpstreamSections(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logsDir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, logsDir, "", 10)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger))
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		executorCtx := context.WithValue(context.Background(), "gin", c)
		helps.RecordAPIRequest(executorCtx, cfg, helps.UpstreamRequestLog{
			URL:     "https://api.example.com/v1/responses",
			Method:  http.MethodPost,
			Headers: http.Header{"Content-Type": []string{"application/json"}},
			Body:    []byte(`{"model":"gpt-5-codex","input":[]}`),
		})
		helps.AppendAPIResponseChunk(executorCtx, cfg, []byte("data: {\"type\":\"response.output_item.added\"}\n\n"))
		_, _ = c.Writer.Write([]byte("data: {\"type\":\"response.output_item.added\"}\n\n"))
		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","input":[],"stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusOK)
	}

	entries, errReadDir := os.ReadDir(logsDir)
	if errReadDir != nil {
		t.Fatalf("read logs dir: %v", errReadDir)
	}
	var logPath string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "v1-responses-") && strings.HasSuffix(entry.Name(), ".log") {
			logPath = logsDir + string(os.PathSeparator) + entry.Name()
			break
		}
	}
	if logPath == "" {
		t.Fatal("streaming request log was not created")
	}
	content, errReadLog := os.ReadFile(logPath)
	if errReadLog != nil {
		t.Fatalf("read log file: %v", errReadLog)
	}
	if !bytes.Contains(content, []byte("=== API REQUEST 1 ===")) {
		t.Fatalf("streaming log missing API REQUEST: %s", string(content))
	}
	if !bytes.Contains(content, []byte("=== API RESPONSE 1 ===")) {
		t.Fatalf("streaming log missing API RESPONSE: %s", string(content))
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

func TestRequestLoggingMiddleware_ClientCancellationExclusion(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("499 status does not create error log when request-log is false", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 10)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/responses", func(c *gin.Context) {
			c.AbortWithStatus(clienterror.StatusClientClosedRequest)
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != clienterror.StatusClientClosedRequest {
			t.Fatalf("status = %d, want %d", resp.Code, clienterror.StatusClientClosedRequest)
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil {
			t.Fatalf("read logs dir: %v", errRead)
		}
		if len(entries) != 0 {
			t.Fatalf("expected 0 log files for 499 cancellation in error-only mode, got %d files", len(entries))
		}
	})

	t.Run("context canceled does not create error log when request-log is false", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 10)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/responses", func(c *gin.Context) {
			// Simulate client closing connection mid-flight
			ctx, cancel := context.WithCancel(c.Request.Context())
			cancel()
			c.Request = c.Request.WithContext(ctx)
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil {
			t.Fatalf("read logs dir: %v", errRead)
		}
		if len(entries) != 0 {
			t.Fatalf("expected 0 log files for canceled context in error-only mode, got %d files", len(entries))
		}
	})

	t.Run("400 bad request creates error log when request-log is false", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 10)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/responses", func(c *gin.Context) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid parameter"})
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"bad":"param"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil {
			t.Fatalf("read logs dir: %v", errRead)
		}
		var errorLogCount int
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
				errorLogCount++
			}
		}
		if errorLogCount != 1 {
			t.Fatalf("expected 1 error log file for 400 Bad Request, got %d", errorLogCount)
		}
	})

	t.Run("mid-stream 400 API_RESPONSE_ERROR writes error log with Status 400 in both modes", func(t *testing.T) {
		for _, requestLogEnabled := range []bool{false, true} {
			logsDir := t.TempDir()
			logger := logging.NewFileRequestLogger(requestLogEnabled, logsDir, "", 10)
			cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: requestLogEnabled}}

			router := gin.New()
			router.Use(RequestLoggingMiddleware(logger))
			router.POST("/v1beta/models/gemini-4-argon-high:streamGenerateContent", func(c *gin.Context) {
				c.Header("Content-Type", "text/event-stream")
				executorCtx := context.WithValue(context.Background(), "gin", c)
				helps.RecordAPIRequest(executorCtx, cfg, helps.UpstreamRequestLog{
					URL:    "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse",
					Method: http.MethodPost,
					Body:   []byte(`{"model":"gemini-4-argon-high"}`),
				})
				c.Writer.WriteHeader(http.StatusOK)
				_, _ = c.Writer.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"thought\":true,\"text\":\"thinking\"}]}}]}\n\n"))
				c.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{
					{
						StatusCode: http.StatusBadRequest,
						Error:      errors.New("This request was blocked by Gemini's filters. You can read more about our policies here (https://policies.google.com/terms/generative-ai/use-policy)."),
					},
				})
			})

			req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-4-argon-high:streamGenerateContent?alt=sse", strings.NewReader(`{"contents":[]}`))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)

			entries, errRead := os.ReadDir(logsDir)
			if errRead != nil {
				t.Fatalf("read logs dir: %v", errRead)
			}
			var errorLogPath string
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
					errorLogPath = filepath.Join(logsDir, entry.Name())
				}
			}
			if errorLogPath == "" {
				t.Fatalf("requestLog=%v: expected error-*.log file, got entries=%v", requestLogEnabled, entries)
			}
			content, errReadFile := os.ReadFile(errorLogPath)
			if errReadFile != nil {
				t.Fatalf("read error log: %v", errReadFile)
			}
			if !bytes.Contains(content, []byte("=== API ERROR RESPONSE ===\nHTTP Status: 400")) {
				t.Fatalf("requestLog=%v: missing API ERROR RESPONSE 400 in log:\n%s", requestLogEnabled, string(content))
			}
			if !bytes.Contains(content, []byte("=== RESPONSE ===\nStatus: 400\n")) {
				t.Fatalf("requestLog=%v: expected RESPONSE Status: 400 in log:\n%s", requestLogEnabled, string(content))
			}
		}
	})

	t.Run("499 status logs standard request when request-log is true", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(true, logsDir, "", 10)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/responses", func(c *gin.Context) {
			c.AbortWithStatus(clienterror.StatusClientClosedRequest)
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil {
			t.Fatalf("read logs dir: %v", errRead)
		}
		var standardLogCount int
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
				standardLogCount++
			}
		}
		if standardLogCount != 1 {
			t.Fatalf("expected 1 standard request log file when request-log=true, got %d", standardLogCount)
		}
	})
}

func TestCaptureRequestInfo_HeadersDeepCopy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/v1/test", nil)
	req.Header.Set("X-Audit", "original-value")
	c.Request = req

	info, err := captureRequestInfo(c, false)
	if err != nil {
		t.Fatalf("captureRequestInfo failed: %v", err)
	}

	// Mutate the original request header slice in place
	c.Request.Header["X-Audit"][0] = "mutated-value"

	if got := info.Headers["X-Audit"][0]; got != "original-value" {
		t.Fatalf("header slice was aliased: got %q, want %q", got, "original-value")
	}
}

func TestManagementV8RequestsAreNotLogged(t *testing.T) {
	for _, path := range []string{"/v8/management/config", "/v8/management/config.yaml", "/v8/management/config/api-keys/codex", "/v8/management/oauth/auth-url"} {
		if shouldLogRequest(path) {
			t.Errorf("management config request would be logged: %s", path)
		}
	}
}

type spyRequestLogger struct {
	lastLogRequestID       string
	lastStreamingRequestID string
	lastStreamWriter       *testStreamingLogWriter
	underlying             logging.RequestLogger
}

func (s *spyRequestLogger) LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	s.lastLogRequestID = requestID
	if s.underlying != nil {
		return s.underlying.LogRequest(url, method, requestHeaders, body, statusCode, responseHeaders, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline, apiResponseErrors, requestID, requestTimestamp, apiResponseTimestamp)
	}
	return nil
}

func (s *spyRequestLogger) LogStreamingRequest(url, method string, headers map[string][]string, body []byte, requestID string) (logging.StreamingLogWriter, error) {
	s.lastStreamingRequestID = requestID
	if s.underlying != nil {
		return s.underlying.LogStreamingRequest(url, method, headers, body, requestID)
	}
	w := &testStreamingLogWriter{}
	s.lastStreamWriter = w
	return w, nil
}

func (s *spyRequestLogger) IsEnabled() bool {
	return true
}

func TestRequestLoggingMiddleware_PreservesFullUUIDForLoggerAndTruncatesFilename(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logsDir := t.TempDir()
	fileLogger := logging.NewFileRequestLogger(true, logsDir, "", 10)
	spy := &spyRequestLogger{underlying: fileLogger}

	router := gin.New()
	router.Use(logging.GinLogrusLogger())
	router.Use(RequestLoggingMiddleware(spy))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"choices": []string{"hello"}})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"input":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}

	// 1. Verify logger received full UUIDv7
	if spy.lastLogRequestID == "" {
		t.Fatalf("expected non-empty request ID passed to logger")
	}
	parsed, errParse := uuid.Parse(spy.lastLogRequestID)
	if errParse != nil {
		t.Fatalf("request ID passed to logger is not a valid UUID: %q (%v)", spy.lastLogRequestID, errParse)
	}
	if parsed.Version() != 7 {
		t.Fatalf("request ID version = %d, want 7", parsed.Version())
	}

	// 2. Verify on-disk log filename uses the trailing 8 chars, not full UUID
	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("ReadDir logsDir failed: %v", errRead)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 log file on disk, got %d", len(entries))
	}
	fileName := entries[0].Name()
	expectedShortID := spy.lastLogRequestID[len(spy.lastLogRequestID)-8:]
	expectedSuffix := "-" + expectedShortID + ".log"
	if !strings.HasSuffix(fileName, expectedSuffix) {
		t.Fatalf("filename %q does not end with expected short suffix %q", fileName, expectedSuffix)
	}
	if strings.Contains(fileName, spy.lastLogRequestID) {
		t.Fatalf("filename %q should not contain the full UUID", fileName)
	}
}

func TestRequestLoggingMiddleware_StreamingPreservesFullUUIDForLogger(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logsDir := t.TempDir()
	fileLogger := logging.NewFileRequestLogger(true, logsDir, "", 10)
	spy := &spyRequestLogger{underlying: fileLogger}

	router := gin.New()
	router.Use(logging.GinLogrusLogger())
	router.Use(RequestLoggingMiddleware(spy))
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write([]byte("data: chunk\n\n"))
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}

	if spy.lastStreamingRequestID == "" {
		t.Fatalf("expected non-empty streaming request ID passed to logger")
	}
	parsed, errParse := uuid.Parse(spy.lastStreamingRequestID)
	if errParse != nil {
		t.Fatalf("streaming request ID passed to logger is not a valid UUID: %q (%v)", spy.lastStreamingRequestID, errParse)
	}
	if parsed.Version() != 7 {
		t.Fatalf("streaming request ID version = %d, want 7", parsed.Version())
	}
}

func TestRequestLoggingMiddleware_FinalizesStreamingWriterOnHandlerPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	spy := &spyRequestLogger{}

	router := gin.New()
	router.Use(logging.GinLogrusRecovery())
	router.Use(RequestLoggingMiddleware(spy))
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write([]byte("data: chunk\n\n"))
		panic("handler boom")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if spy.lastStreamWriter == nil {
		t.Fatal("expected streaming log writer to be created before panic")
	}
	if !spy.lastStreamWriter.closed {
		t.Fatal("expected streaming log writer to be closed when handler panics")
	}

	// Verify that a non-streaming handler panicking before writing a status is
	// finalized as 500 so error-only request logging persists the error log.
	logsDir := t.TempDir()
	errorOnlyLogger := logging.NewFileRequestLogger(false, logsDir, "", 10)
	errorRouter := gin.New()
	errorRouter.Use(logging.GinLogrusRecovery())
	errorRouter.Use(RequestLoggingMiddleware(errorOnlyLogger))
	errorRouter.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Status(http.StatusOK)
		panic("pre-status boom")
	})
	reqPreStatus := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4"}`))
	reqPreStatus.Header.Set("Content-Type", "application/json")
	respPreStatus := httptest.NewRecorder()
	errorRouter.ServeHTTP(respPreStatus, reqPreStatus)
	if respPreStatus.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500", respPreStatus.Code)
	}
	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("ReadDir logsDir: %v", errRead)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "error-") {
		t.Fatalf("expected 1 error log file on pre-status panic, got %+v", entries)
	}

	// http.ErrAbortHandler must not stamp a synthetic 500 error log in error-only mode.
	abortLogsDir := t.TempDir()
	abortLogger := logging.NewFileRequestLogger(false, abortLogsDir, "", 10)
	abortRouter := gin.New()
	abortRouter.Use(RequestLoggingMiddleware(abortLogger))
	abortRouter.POST("/v1/chat/completions", func(c *gin.Context) {
		panic(http.ErrAbortHandler)
	})
	func() {
		defer func() { _ = recover() }()
		reqAbort := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4"}`))
		reqAbort.Header.Set("Content-Type", "application/json")
		abortRouter.ServeHTTP(httptest.NewRecorder(), reqAbort)
	}()
	abortEntries, errReadAbort := os.ReadDir(abortLogsDir)
	if errReadAbort != nil {
		t.Fatalf("ReadDir abortLogsDir: %v", errReadAbort)
	}
	if len(abortEntries) != 0 {
		t.Fatalf("expected no error log for http.ErrAbortHandler, got %+v", abortEntries)
	}
}

// streamingZstdBomb is a small zstd body (a stream frame with a 1 MiB window, as zstd makes from a pipe) that expands to
// size bytes of zeros.
func streamingZstdBomb(t *testing.T, size int) []byte {
	t.Helper()
	var out bytes.Buffer
	encoder, errNewWriter := zstd.NewWriter(&out, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithWindowSize(1<<20))
	if errNewWriter != nil {
		t.Fatal(errNewWriter)
	}
	chunk := make([]byte, 1<<20)
	for written := 0; written < size; written += len(chunk) {
		if _, errWrite := encoder.Write(chunk); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	if errClose := encoder.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	return out.Bytes()
}

func captureOf(t *testing.T, body []byte, limit int64) (captured, restored []byte) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Encoding", "zstd")
	if limit != 0 {
		c.Set("maxRequestBodyBytes", limit)
	}
	info, errCapture := captureRequestInfo(c, true)
	if errCapture != nil {
		t.Fatal(errCapture)
	}
	restored, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		t.Fatal(errRead)
	}
	return info.Body, restored
}

// A small zstd body that expands past the capture bound is decoded to at most the bound when no max-request-body-bytes is
// configured (the capture runs before authentication), or to the configured limit when that is smaller; the request body is
// restored for the handler either way. 40 MiB is enough to pass the 32 MiB bound without a heavy fixture.
func TestCaptureRequestInfoBoundsZstdExpansionWithoutAConfiguredLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bomb := streamingZstdBomb(t, 40<<20)
	if int64(len(bomb)) > maxErrorOnlyCapturedRequestBodyBytes {
		t.Fatalf("the probe body is %d bytes, over the 1 MiB capture bound", len(bomb))
	}
	marker := int64(len("\n[DECOMPRESSED REQUEST BODY TRUNCATED]"))
	for _, c := range []struct{ limit, bound int64 }{{0, maxDeferredErrorRequestBodyBytes}, {-1, maxDeferredErrorRequestBodyBytes}, {1 << 40, maxDeferredErrorRequestBodyBytes}, {4096, 4096}} {
		captured, restored := captureOf(t, bomb, c.limit)
		if int64(len(captured)) > c.bound+marker || int64(len(captured)) < c.bound {
			t.Fatalf("limit %d: captured %d bytes, want the bound %d plus the marker", c.limit, len(captured), c.bound)
		}
		if !bytes.HasSuffix(captured, []byte("[DECOMPRESSED REQUEST BODY TRUNCATED]")) {
			t.Fatalf("limit %d: no truncation marker", c.limit)
		}
		if !bytes.Equal(restored, bomb) {
			t.Fatalf("limit %d: the request body was not restored for the handler (%d bytes)", c.limit, len(restored))
		}
	}
}

// The classic bomb (one segment frame that declares its whole content as its window, 49 KB for 512 MiB) is not decoded at all: its
// window is past the bound, so the raw bytes are logged and nothing large is allocated.
func TestCaptureRequestInfoDoesNotDecodeASingleSegmentBomb(t *testing.T) {
	gin.SetMode(gin.TestMode)
	encoder, errNewWriter := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if errNewWriter != nil {
		t.Fatal(errNewWriter)
	}
	bomb := encoder.EncodeAll(make([]byte, 64<<20), nil)
	if int64(len(bomb)) > maxErrorOnlyCapturedRequestBodyBytes {
		t.Fatalf("the probe body is %d bytes", len(bomb))
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	captured, restored := captureOf(t, bomb, 0)
	runtime.ReadMemStats(&after)
	if !bytes.Equal(captured, bomb) || !bytes.Equal(restored, bomb) {
		t.Fatalf("the raw bytes must be logged and restored, captured %d restored %d", len(captured), len(restored))
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 16<<20 {
		t.Fatalf("decoding a window-bomb allocated %d MiB", grown>>20)
	}
}

// A frame that declares a window past the bound is not decoded: the raw bytes are logged and nothing large is allocated.
func TestDecodeCapturedZstdRefusesAnOversizedWindow(t *testing.T) {
	// `printf '{"model":"gpt-5"}' | zstd --long=24`-style frame declaring a 16 MiB window.
	raw, errDecode := hex.DecodeString("28b52ffd0470b900007b226d6f64656c223a226770742d362e312d736f6c227d2a82afeb")
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	got := decodeCapturedRequestBodyForLogWithLimit(raw, "zstd", 1<<20)
	if !bytes.Equal(got, raw) {
		t.Fatalf("an oversized window must leave the raw bytes, got %q", got)
	}
}

func TestResponseWriterLogOnErrorOnlyZeroBufferUntilActionableError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))

	logger := logging.NewFileRequestLogger(false, t.TempDir(), "", 10)
	wrapper := NewResponseWriterWrapper(c.Writer, logger, &RequestInfo{
		URL:    "/v1/messages",
		Method: http.MethodPost,
	})
	wrapper.logOnErrorOnly = true
	wrapper.ginContext = c

	// Normal 200 OK writes (even 4 MiB) must not allocate/buffer any bytes when request-log is disabled.
	largePayload := bytes.Repeat([]byte("a"), 4<<20) // 4 MiB
	if _, err := wrapper.Write(largePayload); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if wrapper.body.Len() != 0 {
		t.Fatalf("wrapper.body.Len() for 200 OK = %d, want 0", wrapper.body.Len())
	}

	// Once an actionable mid-stream error is recorded, subsequent error frame writes are buffered.
	c.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{
		{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New("claude refusal (cyber): blocked"),
		},
	})
	const errFrame = "event: error\ndata: {\"type\":\"error\"}\n\n"
	if _, err := wrapper.WriteString(errFrame); err != nil {
		t.Fatalf("WriteString failed: %v", err)
	}
	if got := wrapper.body.String(); got != errFrame {
		t.Fatalf("wrapper.body.String() = %q, want %q", got, errFrame)
	}
}

func TestRequestLoggingMiddleware_Unrouted404DoesNotEvictErrorDumpRing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("unrouted 404 does not create error log or evict existing error dumps when request-log is false", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 5)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/messages", func(c *gin.Context) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "upstream failure"})
		})
		router.NoRoute(func(c *gin.Context) {
			c.AbortWithStatus(http.StatusNotFound)
		})

		realFailReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5"}`))
		realFailReq.Header.Set("Content-Type", "application/json")
		realFailResp := httptest.NewRecorder()
		router.ServeHTTP(realFailResp, realFailReq)
		if realFailResp.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", realFailResp.Code, http.StatusBadGateway)
		}

		for i := 0; i < 10; i++ {
			probeReq := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(`{"purpose":"user_data"}`))
			probeReq.Header.Set("Content-Type", "application/json")
			probeResp := httptest.NewRecorder()
			router.ServeHTTP(probeResp, probeReq)
			if probeResp.Code != http.StatusNotFound {
				t.Fatalf("probe %d status = %d, want %d", i, probeResp.Code, http.StatusNotFound)
			}
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil {
			t.Fatalf("read logs dir: %v", errRead)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if len(names) != 1 || !strings.HasPrefix(names[0], "error-v1-messages-") {
			t.Fatalf("expected only the original error-v1-messages-*.log dump to survive, got: %v", names)
		}
	})

	t.Run("unrouted 404 does not create error log when request-log is true", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(true, logsDir, "", 5)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/messages", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})
		router.NoRoute(func(c *gin.Context) {
			c.AbortWithStatus(http.StatusNotFound)
		})

		probeReq := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(`{"purpose":"user_data"}`))
		probeReq.Header.Set("Content-Type", "application/json")
		probeResp := httptest.NewRecorder()
		router.ServeHTTP(probeResp, probeReq)
		if probeResp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", probeResp.Code, http.StatusNotFound)
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil && !os.IsNotExist(errRead) {
			t.Fatalf("read logs dir: %v", errRead)
		}
		if len(entries) != 0 {
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			t.Fatalf("expected 0 files for unrouted 404 when request-log=true, got: %v", names)
		}
	})

	t.Run("routed 404 still creates error log when request-log is false", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 5)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/messages", func(c *gin.Context) {
			c.JSON(http.StatusNotFound, gin.H{"error": "model not found"})
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"missing-model"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusNotFound)
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil {
			t.Fatalf("read logs dir: %v", errRead)
		}
		if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "error-v1-messages-") {
			t.Fatalf("expected 1 error-v1-messages-*.log for routed 404, got: %+v", entries)
		}
	})

	t.Run("unrouted streaming 404 does not initialize stream writer or leak temp files when request-log is true", func(t *testing.T) {
		logsDir := t.TempDir()
		fileLogger := logging.NewFileRequestLogger(true, logsDir, "", 5)
		spy := &spyRequestLogger{underlying: fileLogger}

		router := gin.New()
		router.Use(RequestLoggingMiddleware(spy))
		router.NoRoute(func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			c.Writer.WriteHeader(http.StatusNotFound)
			_, _ = c.Writer.Write([]byte("event: error\ndata: not found\n\n"))
		})

		probeReq := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(`{"stream":true}`))
		probeReq.Header.Set("Content-Type", "application/json")
		probeResp := httptest.NewRecorder()
		router.ServeHTTP(probeResp, probeReq)
		if probeResp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", probeResp.Code, http.StatusNotFound)
		}
		if spy.lastStreamingRequestID != "" || spy.lastStreamWriter != nil {
			t.Fatalf("expected LogStreamingRequest not to be called for unrouted 404, got requestID=%q writer=%v", spy.lastStreamingRequestID, spy.lastStreamWriter)
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil && !os.IsNotExist(errRead) {
			t.Fatalf("read logs dir: %v", errRead)
		}
		if len(entries) != 0 {
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			t.Fatalf("expected 0 files/temp dirs for unrouted streaming 404, got: %v", names)
		}
	})

	t.Run("unrouted streaming 200 to uncommitted 404 transition discards stream writer without creating error log", func(t *testing.T) {
		logsDir := t.TempDir()
		fileLogger := logging.NewFileRequestLogger(true, logsDir, "", 5)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(fileLogger))
		router.NoRoute(func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			c.Writer.WriteHeader(http.StatusOK)
			c.Writer.WriteHeader(http.StatusNotFound)
			_, _ = c.Writer.Write([]byte("data: not found\n\n"))
		})

		probeReq := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(`{"stream":true}`))
		probeReq.Header.Set("Content-Type", "application/json")
		probeResp := httptest.NewRecorder()
		router.ServeHTTP(probeResp, probeReq)
		if probeResp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", probeResp.Code, http.StatusNotFound)
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil && !os.IsNotExist(errRead) {
			t.Fatalf("read logs dir: %v", errRead)
		}
		if len(entries) != 0 {
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			t.Fatalf("expected 0 files/temp dirs after discarded streaming 404, got: %v", names)
		}
	})
}

func TestRequestLoggingMiddlewareErrorLogKeepsUpstreamResponseWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	readSingleErrorLog := func(t *testing.T, logsDir string) string {
		t.Helper()
		entries, errReadDir := os.ReadDir(logsDir)
		if errReadDir != nil {
			t.Fatalf("read logs dir: %v", errReadDir)
		}
		var errorLogPaths []string
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
				errorLogPaths = append(errorLogPaths, filepath.Join(logsDir, entry.Name()))
			}
		}
		if len(errorLogPaths) != 1 {
			t.Fatalf("error log count = %d, want 1 (entries=%v)", len(errorLogPaths), entries)
		}
		content, errReadFile := os.ReadFile(errorLogPaths[0])
		if errReadFile != nil {
			t.Fatalf("read error log: %v", errReadFile)
		}
		return string(content)
	}

	t.Run("non-stream WriteErrorResponse keeps per-attempt upstream status masked headers body and error", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 10)
		cfg := &config.Config{}
		baseHandler := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, nil)
		largeAttemptTwoBody := "data: {\"type\":\"response.created\",\"id\":\"resp_2\"}\n\n" + strings.Repeat("x", 70*1024)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/chat/completions", func(c *gin.Context) {
			executorCtx := context.WithValue(context.Background(), "gin", c)

			helps.RecordAPIRequest(executorCtx, cfg, helps.UpstreamRequestLog{
				URL:     "https://cli-chat-proxy.grok.com/v1/responses",
				Method:  http.MethodPost,
				Headers: http.Header{"Authorization": []string{"Bearer super-secret-request-token"}},
				Body:    []byte(`{"model":"grok-4.7","stream":true}`),
			})
			helps.RecordAPIResponseMetadata(executorCtx, cfg, http.StatusBadGateway, http.Header{
				"Content-Type":  []string{"text/event-stream"},
				"Authorization": []string{"Bearer super-secret-response-token"},
				"X-Request-Id":  []string{"xai-req-1"},
			})
			helps.AppendAPIResponseChunk(executorCtx, cfg, []byte("event: response.created"))
			helps.AppendAPIResponseChunk(executorCtx, cfg, []byte(`data: {"type":"response.created","id":"resp_1"}`))
			helps.RecordAPIResponseError(executorCtx, cfg, errors.New("unexpected EOF"))

			helps.RecordAPIRequest(executorCtx, cfg, helps.UpstreamRequestLog{
				URL:    "https://cli-chat-proxy.grok.com/v1/responses",
				Method: http.MethodPost,
				Body:   []byte(`{"model":"grok-4.7","stream":true,"attempt":2}`),
			})
			helps.RecordAPIResponseMetadata(executorCtx, cfg, http.StatusOK, http.Header{
				"Content-Type": []string{"text/event-stream"},
				"Set-Cookie":   []string{"session=super-secret-cookie-value"},
			})
			helps.AppendAPIResponseChunk(executorCtx, cfg, []byte(largeAttemptTwoBody))

			baseHandler.WriteErrorResponse(c, &interfaces.ErrorMessage{
				StatusCode: http.StatusRequestTimeout,
				Error:      errors.New("xai stream error: stream disconnected before response.completed or response.incomplete"),
			})
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-4.7","stream":false}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusRequestTimeout {
			t.Fatalf("response status = %d, want %d", resp.Code, http.StatusRequestTimeout)
		}

		logText := readSingleErrorLog(t, logsDir)
		apiRequestIdx := strings.Index(logText, "=== API REQUEST 1 ===")
		apiResponseIdx := strings.Index(logText, "=== API RESPONSE 1 ===")
		downstreamResponseIdx := strings.Index(logText, "=== RESPONSE ===")
		if apiRequestIdx == -1 || apiResponseIdx == -1 || downstreamResponseIdx == -1 {
			t.Fatalf("missing expected log sections (req=%d, apiResp=%d, resp=%d):\n%s", apiRequestIdx, apiResponseIdx, downstreamResponseIdx, logText)
		}
		if apiRequestIdx >= apiResponseIdx || apiResponseIdx >= downstreamResponseIdx {
			t.Fatalf("unexpected section order (req=%d, apiResp=%d, resp=%d):\n%s", apiRequestIdx, apiResponseIdx, downstreamResponseIdx, logText)
		}

		apiResponseSection := logText[apiResponseIdx:downstreamResponseIdx]
		downstreamSection := logText[downstreamResponseIdx:]
		for _, want := range []string{
			"=== API RESPONSE 1 ===",
			"Status: 502",
			"Authorization: Bearer supe...oken",
			"X-Request-Id: xai-req-1",
			"event: response.created\ndata: {\"type\":\"response.created\",\"id\":\"resp_1\"}",
			"Error: unexpected EOF",
			"=== API RESPONSE 2 ===",
			"Status: 200",
			"data: {\"type\":\"response.created\",\"id\":\"resp_2\"}",
			"[API RESPONSE BODY TRUNCATED: captured first ",
		} {
			if !strings.Contains(apiResponseSection, want) {
				t.Fatalf("API RESPONSE section missing %q:\n%s", want, apiResponseSection)
			}
		}
		for _, secret := range []string{"super-secret-response-token", "super-secret-cookie-value"} {
			if strings.Contains(logText, secret) {
				t.Fatalf("error log leaked sensitive header value %q:\n%s", secret, logText)
			}
		}
		if strings.Contains(apiResponseSection, `"code":"request_timeout"`) {
			t.Fatalf("API RESPONSE section should contain upstream attempts rather than proxy synthesized error JSON:\n%s", apiResponseSection)
		}
		if !strings.Contains(downstreamSection, "Status: 408") || !strings.Contains(downstreamSection, "xai stream error: stream disconnected before response.completed or response.incomplete") {
			t.Fatalf("downstream RESPONSE section missing final proxy error:\n%s", downstreamSection)
		}
	})

	t.Run("mid-stream SSE error keeps upstream response in error log", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 10)
		cfg := &config.Config{}

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/chat/completions", func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			executorCtx := context.WithValue(context.Background(), "gin", c)
			helps.RecordAPIRequest(executorCtx, cfg, helps.UpstreamRequestLog{
				URL:    "https://cli-chat-proxy.grok.com/v1/responses",
				Method: http.MethodPost,
				Body:   []byte(`{"model":"grok-4.7","stream":true}`),
			})
			helps.RecordAPIResponseMetadata(executorCtx, cfg, http.StatusOK, http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"xai-stream-1"},
			})
			helps.AppendAPIResponseChunk(executorCtx, cfg, []byte(`data: {"type":"response.failed","response":{"error":{"message":"upstream quota exceeded"}}}`))
			helps.RecordAPIResponseError(executorCtx, cfg, errors.New("upstream quota exceeded"))

			c.Writer.WriteHeader(http.StatusOK)
			_, _ = c.Writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
			c.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{
				{
					StatusCode: http.StatusTooManyRequests,
					Error:      errors.New("upstream quota exceeded"),
				},
			})
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-4.7","stream":true}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		logText := readSingleErrorLog(t, logsDir)
		for _, want := range []string{
			"=== API REQUEST 1 ===",
			"=== API RESPONSE 1 ===",
			"Status: 200",
			"X-Request-Id: xai-stream-1",
			`data: {"type":"response.failed","response":{"error":{"message":"upstream quota exceeded"}}}`,
			"Error: upstream quota exceeded",
		} {
			if !strings.Contains(logText, want) {
				t.Fatalf("mid-stream error log missing %q:\n%s", want, logText)
			}
		}
	})

	t.Run("rejected upstream websocket upgrade keeps handshake request and response in error log", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 10)
		cfg := &config.Config{}

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/responses", func(c *gin.Context) {
			executorCtx := context.WithValue(context.Background(), "gin", c)
			helps.RecordAPIWebsocketUpgradeRejection(
				executorCtx,
				cfg,
				helps.UpstreamRequestLog{
					URL:     "https://chatgpt.com/backend-api/codex/responses",
					Method:  http.MethodGet,
					Headers: http.Header{"Authorization": []string{"Bearer super-secret-ws-token"}},
				},
				http.StatusForbidden,
				http.Header{
					"Content-Type": []string{"application/json"},
					"Set-Cookie":   []string{"cf_clearance=super-secret-cf-cookie"},
				},
				[]byte(`{"error":{"message":"websocket handshake rejected"}}`),
			)
			c.JSON(http.StatusForbidden, gin.H{"error": "websocket handshake rejected"})
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","input":"hi"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		logText := readSingleErrorLog(t, logsDir)
		for _, want := range []string{
			"=== API REQUEST 1 ===",
			"Upstream URL: https://chatgpt.com/backend-api/codex/responses",
			"=== API RESPONSE 1 ===",
			"Status: 403",
			`{"error":{"message":"websocket handshake rejected"}}`,
		} {
			if !strings.Contains(logText, want) {
				t.Fatalf("websocket rejection error log missing %q:\n%s", want, logText)
			}
		}
		for _, secret := range []string{"super-secret-ws-token", "super-secret-cf-cookie"} {
			if strings.Contains(logText, secret) {
				t.Fatalf("websocket rejection error log leaked secret %q:\n%s", secret, logText)
			}
		}
	})

	t.Run("successful request with request-log off writes no log file", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 10)
		cfg := &config.Config{}

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/chat/completions", func(c *gin.Context) {
			executorCtx := context.WithValue(context.Background(), "gin", c)
			helps.RecordAPIRequest(executorCtx, cfg, helps.UpstreamRequestLog{
				URL:    "https://cli-chat-proxy.grok.com/v1/responses",
				Method: http.MethodPost,
				Body:   []byte(`{"model":"grok-4.7"}`),
			})
			helps.RecordAPIResponseMetadata(executorCtx, cfg, http.StatusOK, http.Header{
				"Content-Type": []string{"text/event-stream"},
			})
			helps.AppendAPIResponseChunk(executorCtx, cfg, []byte(`data: {"type":"response.completed"}`))
			c.JSON(http.StatusOK, gin.H{"id": "chatcmpl_1"})
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-4.7"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("response status = %d, want %d", resp.Code, http.StatusOK)
		}
		entries, errReadDir := os.ReadDir(logsDir)
		if errReadDir != nil && !os.IsNotExist(errReadDir) {
			t.Fatalf("read logs dir: %v", errReadDir)
		}
		if len(entries) != 0 {
			t.Fatalf("expected 0 log files for successful request with request-log=false, got: %v", entries)
		}
	})
}

func TestRequestLoggingMiddlewareIsolatesDeferredResponseAcrossStreamCancelAndGinContextReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	readSingleErrorLog := func(t *testing.T, logsDir string) string {
		t.Helper()
		entries, errReadDir := os.ReadDir(logsDir)
		if errReadDir != nil {
			t.Fatalf("read logs dir: %v", errReadDir)
		}
		var errorLogPaths []string
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
				errorLogPaths = append(errorLogPaths, filepath.Join(logsDir, entry.Name()))
			}
		}
		if len(errorLogPaths) != 1 {
			t.Fatalf("error log count = %d, want 1 (entries=%v)", len(errorLogPaths), entries)
		}
		content, errReadFile := os.ReadFile(errorLogPaths[0])
		if errReadFile != nil {
			t.Fatalf("read error log: %v", errReadFile)
		}
		return string(content)
	}

	t.Run("late stream producer after cancel does not pollute reused Gin context", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 10)
		cfg := &config.Config{}
		baseHandler := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, nil)

		const cancelledTurns = 8
		turn2Active := make(chan struct{})
		var producers sync.WaitGroup

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/chat/completions", func(c *gin.Context) {
			if c.GetHeader("X-Turn") == "2" {
				cliCtx, cliCancel := baseHandler.GetContextWithCancel(nil, c, context.Background())
				defer cliCancel()
				helps.RecordAPIRequest(cliCtx, cfg, helps.UpstreamRequestLog{
					URL:    "https://api.example.com/turn2",
					Method: http.MethodPost,
					Body:   []byte(`{"turn":2}`),
				})
				helps.RecordAPIResponseMetadata(cliCtx, cfg, http.StatusBadGateway, http.Header{
					"Content-Type": []string{"application/json"},
				})
				helps.AppendAPIResponseChunk(cliCtx, cfg, []byte(`{"error":"turn2-upstream-failure"}`))
				close(turn2Active)
				producers.Wait()
				baseHandler.WriteErrorResponse(c, &interfaces.ErrorMessage{
					StatusCode: http.StatusBadGateway,
					Error:      errors.New("turn2 failed"),
				})
				return
			}

			cliCtx, cliCancel := baseHandler.GetContextWithCancel(nil, c, context.Background())
			helps.RecordAPIRequest(cliCtx, cfg, helps.UpstreamRequestLog{
				URL:    "https://api.example.com/turn1",
				Method: http.MethodPost,
				Body:   []byte(`{"turn":1}`),
			})
			helps.RecordAPIResponseMetadata(cliCtx, cfg, http.StatusOK, http.Header{
				"Content-Type": []string{"text/event-stream"},
			})
			helps.AppendAPIResponseChunk(cliCtx, cfg, []byte(`data: {"turn":1,"early":true}`))
			producerStarted := make(chan struct{})
			producers.Add(1)
			go func(streamCtx context.Context) {
				defer producers.Done()
				close(producerStarted)
				<-turn2Active
				helps.RecordAPIRequest(streamCtx, cfg, helps.UpstreamRequestLog{
					URL:    "https://api.example.com/turn1-late-req",
					Method: http.MethodPost,
					Body:   []byte(`{"turn":1,"late":true}`),
				})
				helps.RecordAPIResponseMetadata(streamCtx, cfg, http.StatusInternalServerError, http.Header{
					"X-Turn1-Late": []string{"leaked"},
				})
				helps.AppendAPIResponseChunk(streamCtx, cfg, []byte("turn1-late-stream-chunk"))
				helps.RecordAPIResponseError(streamCtx, cfg, errors.New("turn1-late-stream-error"))
			}(cliCtx)
			<-producerStarted
			cliCancel()
			c.Status(clienterror.StatusClientClosedRequest)
		})

		for range cancelledTurns {
			req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`))
			req1.Header.Set("Content-Type", "application/json")
			req1.Header.Set("X-Turn", "1")
			router.ServeHTTP(httptest.NewRecorder(), req1)
		}

		req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":false}`))
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("X-Turn", "2")
		resp2 := httptest.NewRecorder()
		router.ServeHTTP(resp2, req2)
		if resp2.Code != http.StatusBadGateway {
			t.Fatalf("turn 2 status = %d, want %d", resp2.Code, http.StatusBadGateway)
		}

		logText := readSingleErrorLog(t, logsDir)
		for _, want := range []string{
			"https://api.example.com/turn2",
			`{"error":"turn2-upstream-failure"}`,
		} {
			if !strings.Contains(logText, want) {
				t.Fatalf("turn 2 error log missing %q:\n%s", want, logText)
			}
		}
		for _, leaked := range []string{
			"turn1-late-req",
			"X-Turn1-Late",
			"turn1-late-stream-chunk",
			"turn1-late-stream-error",
		} {
			if strings.Contains(logText, leaked) {
				t.Fatalf("turn 2 error log leaked stale turn 1 data %q:\n%s", leaked, logText)
			}
		}
	})

	t.Run("concurrent stream producer during Finalize is race-free", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 10)
		cfg := &config.Config{}
		baseHandler := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, nil)

		producerStarted := make(chan struct{})
		producerDone := make(chan struct{})

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/chat/completions", func(c *gin.Context) {
			cliCtx, cliCancel := baseHandler.GetContextWithCancel(nil, c, context.Background())
			defer cliCancel()
			helps.RecordAPIRequest(cliCtx, cfg, helps.UpstreamRequestLog{
				URL:    "https://api.example.com/v1/responses",
				Method: http.MethodPost,
				Body:   []byte(`{"stream":true}`),
			})
			helps.RecordAPIResponseMetadata(cliCtx, cfg, http.StatusOK, http.Header{
				"Content-Type": []string{"text/event-stream"},
			})
			go func(streamCtx context.Context) {
				defer close(producerDone)
				close(producerStarted)
				for range 32 {
					helps.AppendAPIResponseChunk(streamCtx, cfg, []byte(`data: {"delta":"chunk"}`))
				}
				helps.RecordAPIResponseError(streamCtx, cfg, errors.New("stream aborted"))
			}(cliCtx)
			<-producerStarted
			c.Header("Content-Type", "text/event-stream")
			c.Writer.WriteHeader(http.StatusOK)
			c.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{
				{
					StatusCode: http.StatusBadGateway,
					Error:      errors.New("mid-stream abort"),
				},
			})
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(httptest.NewRecorder(), req)
		<-producerDone

		logText := readSingleErrorLog(t, logsDir)
		if !strings.Contains(logText, "=== API RESPONSE 1 ===") {
			t.Fatalf("concurrent error log missing API RESPONSE 1:\n%s", logText)
		}
	})
}
