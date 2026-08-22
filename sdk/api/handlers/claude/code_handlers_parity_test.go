package claude

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func TestWriteClaudeDirectErrorPreservesRetryAndRequestMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	handler := &ClaudeCodeAPIHandler{}
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"offline rate limit"}}`)
	msg := &interfaces.ErrorMessage{
		StatusCode:     http.StatusTooManyRequests,
		DirectResponse: true,
		Body:           body,
		Headers: http.Header{
			"Retry-After":  []string{"7"},
			"X-Request-Id": []string{"req_legacy_contract"},
		},
	}

	handler.WriteErrorResponse(c, msg)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "7" {
		t.Fatalf("Retry-After = %q, want 7", got)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "req_legacy_contract" {
		t.Fatalf("X-Request-Id = %q", got)
	}
	if got := recorder.Body.String(); got != string(body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestForwardClaudeStreamRejectsCleanCloseBeforeTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	close(data)
	close(errs)

	NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{}).forwardClaudeStream(c, recorder, func(error) {}, data, errs, false)
	if body := recorder.Body.String(); !strings.Contains(body, "Upstream stream ended before response.completed.") {
		t.Fatalf("body = %q, want incomplete-stream Claude error", body)
	}
}

func TestForwardClaudeStreamAcceptsCleanCloseAfterTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	close(data)
	close(errs)

	NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{}).forwardClaudeStream(c, recorder, func(error) {}, data, errs, true)
	if recorder.Body.Len() != 0 {
		t.Fatalf("body = %q, want no synthetic terminal after message_stop", recorder.Body.String())
	}
}

func TestClaudeStreamTerminalDetectionIgnoresPayloadText(t *testing.T) {
	chunk := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"\\\"type\\\":\\\"message_stop\\\" event: error\"}}\n\n")
	if claudeStreamChunkTerminal(chunk) {
		t.Fatal("payload text must not mark a Claude stream terminal")
	}
}

func TestClaudeStreamTerminalDetectionHandlesMultiEventChunk(t *testing.T) {
	chunk := []byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	if !claudeStreamChunkTerminal(chunk) {
		t.Fatal("message_stop in a multi-event chunk must mark the stream terminal")
	}
}
