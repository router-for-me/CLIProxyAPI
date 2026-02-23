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
	enabled         bool
	logBody         []byte
	apiRequestBody  []byte
	apiResponseBody []byte
}

func (m *mockLogger) LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	m.logBody = append([]byte(nil), body...)
	m.apiRequestBody = append([]byte(nil), apiRequest...)
	m.apiResponseBody = append([]byte(nil), apiResponse...)
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

func TestResponseWriterWrapper_SanitizeAPIAndRequestBodiesBeforeLogging(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gw := httptest.NewRecorder()
	gc := gin.CreateTestContextOnly(gw, gin.Default())

	logger := &mockLogger{enabled: true}
	reqInfo := &RequestInfo{
		URL:    "/v1/chat/completions",
		Method: "POST",
		Body:   []byte(`{"api_key":"sk-secret","nested":{"refresh_token":"refresh-secret"}}`),
	}

	wrapper := NewResponseWriterWrapper(gc.Writer, logger, reqInfo)
	gc.Set("API_REQUEST", []byte(`{"access_token":"api-secret","payload":1}`))
	gc.Set("API_RESPONSE", []byte(`{"refresh_token":"resp-secret"}`))

	if _, err := gc.Writer.Write([]byte("ok")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	gc.Writer.WriteHeader(http.StatusOK)

	if err := wrapper.Finalize(gc); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	if strings.Contains(string(wrapper.extractAPIRequest(gc)), "api-secret") || strings.Contains(string(wrapper.extractAPIResponse(gc)), "resp-secret") {
		t.Fatalf("API payloads must be redacted")
	}
	if strings.Contains(string(logger.logBody), "sk-secret") || strings.Contains(string(logger.logBody), "refresh-secret") {
		t.Fatalf("request body leak detected in logger: %q", logger.logBody)
	}
	if strings.Contains(string(wrapper.extractRequestBody(gc)), "sk-secret") || strings.Contains(string(wrapper.extractRequestBody(gc)), "refresh-secret") {
		t.Fatalf("request body should be redacted on extraction path")
	}
	if !strings.Contains(string(logger.apiRequestBody), "[REDACTED]") {
		t.Fatalf("api request body leak expected redaction: %q", logger.apiRequestBody)
	}
	if !strings.Contains(string(logger.apiResponseBody), "[REDACTED]") {
		t.Fatalf("api response body leak expected redaction: %q", logger.apiResponseBody)
	}
}
