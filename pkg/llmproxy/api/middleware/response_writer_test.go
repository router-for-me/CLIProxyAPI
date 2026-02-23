package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/logging"
)

type mockLogger struct {
	enabled              bool
	logged               bool
	responseHeaders      map[string][]string
	apiResponseTimestamp time.Time
}

func (m *mockLogger) LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	m.logged = true
	m.responseHeaders = responseHeaders
	m.apiResponseTimestamp = apiResponseTimestamp
	return nil
}

func (m *mockLogger) IsEnabled() bool {
	return m.enabled
}

func (m *mockLogger) LogStreamingRequest(url, method string, headers map[string][]string, body []byte, requestID string) (logging.StreamingLogWriter, error) {
	return &logging.NoOpStreamingLogWriter{}, nil
}

func TestResponseWriterWrapper_Basic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	gw := gin.CreateTestContextOnly(w, gin.Default())

	logger := &mockLogger{enabled: true}
	reqInfo := &RequestInfo{
		URL:    "/test",
		Method: "GET",
		Body:   []byte("req body"),
	}

	wrapper := NewResponseWriterWrapper(gw.Writer, logger, reqInfo)

	// Test Write
	n, err := wrapper.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Errorf("Write failed: n=%d, err=%v", n, err)
	}

	// Test WriteHeader
	wrapper.WriteHeader(http.StatusAccepted)
	if wrapper.statusCode != http.StatusAccepted {
		t.Errorf("expected status 202, got %d", wrapper.statusCode)
	}

	// Test Finalize
	err = wrapper.Finalize(gw)
	if err != nil {
		t.Errorf("Finalize failed: %v", err)
	}
}

func TestResponseWriterWrapper_DetectStreaming(t *testing.T) {
	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{
			Body: []byte(`{"stream": true}`),
		},
	}

	if !wrapper.detectStreaming("text/event-stream") {
		t.Error("expected true for text/event-stream")
	}

	if wrapper.detectStreaming("application/json") {
		t.Error("expected false for application/json even with stream:true in body (per logic)")
	}

	wrapper.requestInfo.Body = []byte(`{}`)
	if wrapper.detectStreaming("") {
		t.Error("expected false for empty content type and no stream hint")
	}
}

func TestResponseWriterWrapper_ForwardsResponseHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	gw := gin.CreateTestContextOnly(w, gin.Default())

	logger := &mockLogger{enabled: true}
	reqInfo := &RequestInfo{
		URL:    "/test",
		Method: "GET",
		Body:   []byte("req body"),
	}

	wrapper := NewResponseWriterWrapper(gw.Writer, logger, reqInfo)
	wrapper.Header().Set("Set-Cookie", "session=abc")
	wrapper.Header().Set("Authorization", "Bearer secret")
	wrapper.Header().Set("X-API-Key", "abc123")

	wrapper.WriteHeader(http.StatusCreated)
	if _, err := wrapper.Write([]byte("ok")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := wrapper.Finalize(gw); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	if !logger.logged {
		t.Fatalf("expected logger to be called")
	}
	if got := logger.responseHeaders["Authorization"]; len(got) != 1 || got[0] != "Bearer secret" {
		t.Fatalf("Authorization should be forwarded, got %#v", got)
	}
	if got := logger.responseHeaders["Set-Cookie"]; len(got) != 1 || got[0] != "session=abc" {
		t.Fatalf("Set-Cookie should be forwarded, got %#v", got)
	}

	var xAPIKey []string
	for key, value := range logger.responseHeaders {
		if strings.EqualFold(key, "X-API-Key") {
			xAPIKey = value
			break
		}
	}
	if len(xAPIKey) != 1 || xAPIKey[0] != "abc123" {
		t.Fatalf("X-API-Key should be forwarded, got %#v", xAPIKey)
	}
}

func TestResponseWriterWrapper_ForwardsAPIResponseTimestamp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	gw := gin.CreateTestContextOnly(w, gin.Default())
	expected := time.Date(2026, time.February, 23, 14, 0, 0, 0, time.UTC)

	logger := &mockLogger{enabled: true}
	reqInfo := &RequestInfo{
		URL:    "/test",
		Method: "GET",
		Body:   []byte("req body"),
	}

	wrapper := NewResponseWriterWrapper(gw.Writer, logger, reqInfo)
	wrapper.WriteHeader(http.StatusAccepted)
	gw.Set("API_RESPONSE_TIMESTAMP", expected)

	if err := wrapper.Finalize(gw); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	if !logger.logged {
		t.Fatalf("expected logger to be called")
	}
	if logger.apiResponseTimestamp.IsZero() {
		t.Fatalf("expected API response timestamp to be forwarded")
	}
	if !logger.apiResponseTimestamp.Equal(expected) {
		t.Fatalf("expected %v, got %v", expected, logger.apiResponseTimestamp)
	}
}
