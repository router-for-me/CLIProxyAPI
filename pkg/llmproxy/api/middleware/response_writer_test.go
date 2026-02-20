package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/logging"
)

type mockLogger struct {
	enabled bool
}

func (m *mockLogger) LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
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
