package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
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

func TestCaptureRequestInfoPreservesBodyForDownstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	payload := `{"message":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(payload))
	c.Request = req

	requestInfo, bodyCapture := captureRequestInfo(c, true)
	if bodyCapture == nil {
		t.Fatal("expected request body capture")
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if err := c.Request.Body.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if string(body) != payload {
		t.Fatalf("body = %q, want %q", string(body), payload)
	}
	if string(requestInfo.Body) != payload {
		t.Fatalf("captured body = %q, want %q", string(requestInfo.Body), payload)
	}
	if override := bodyCapture.logBody(); override != nil {
		t.Fatalf("unexpected override: %q", string(override))
	}
}

func TestCaptureRequestInfoSummarizesLargeBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	payload := strings.Repeat("x", int(maxLoggedRequestBodyBytes)+128)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(payload))
	c.Request = req

	requestInfo, bodyCapture := captureRequestInfo(c, true)
	if bodyCapture == nil {
		t.Fatal("expected request body capture")
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if err := c.Request.Body.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if len(body) != len(payload) {
		t.Fatalf("body length = %d, want %d", len(body), len(payload))
	}
	if len(requestInfo.Body) != int(maxLoggedRequestBodyBytes) {
		t.Fatalf("captured preview length = %d, want %d", len(requestInfo.Body), maxLoggedRequestBodyBytes)
	}

	override := string(bodyCapture.logBody())
	if !strings.Contains(override, "truncated=true") {
		t.Fatalf("override = %q, want truncated summary", override)
	}
	if !strings.Contains(override, "complete=true") {
		t.Fatalf("override = %q, want complete summary", override)
	}
	if !strings.Contains(override, "captured_bytes=1048576") {
		t.Fatalf("override = %q, want captured byte count", override)
	}
	if !strings.Contains(override, "observed_bytes=1048704") {
		t.Fatalf("override = %q, want observed byte count", override)
	}

	sum := sha256.Sum256([]byte(payload))
	expectedHash := hex.EncodeToString(sum[:])
	if !strings.Contains(override, "observed_sha256="+expectedHash) {
		t.Fatalf("override = %q, want observed hash %s", override, expectedHash)
	}
}

func TestRequestLoggingMiddlewareSummarizesLargeRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logger := &capturingRequestLogger{enabled: true}
	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Fatalf("handler ReadAll error: %v", err)
		}
		c.String(http.StatusOK, "%d", len(body))
	})

	payload := strings.Repeat("x", int(maxLoggedRequestBodyBytes)+128)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(payload))

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if !strings.Contains(string(logger.lastRequestBody), "truncated=true") {
		t.Fatalf("logged request body = %q, want truncated summary", string(logger.lastRequestBody))
	}
	if strings.Contains(string(logger.lastRequestBody), payload[:64]) {
		t.Fatalf("logged request body should not contain raw payload prefix")
	}
}

type capturingRequestLogger struct {
	enabled         bool
	lastRequestBody []byte
}

func (l *capturingRequestLogger) LogRequest(_ string, _ string, _ map[string][]string, body []byte, _ int, _ map[string][]string, _ []byte, _ []byte, _ []byte, _ []byte, _ []byte, _ []*interfaces.ErrorMessage, _ string, _ time.Time, _ time.Time) error {
	l.lastRequestBody = append([]byte(nil), body...)
	return nil
}

func (l *capturingRequestLogger) LogStreamingRequest(string, string, map[string][]string, []byte, string) (logging.StreamingLogWriter, error) {
	return &testStreamingLogWriter{}, nil
}

func (l *capturingRequestLogger) IsEnabled() bool {
	return l.enabled
}
