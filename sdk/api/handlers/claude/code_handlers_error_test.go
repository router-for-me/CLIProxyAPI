package claude

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestClaudeErrorExtractsOpenAIStyleUpstreamJSON(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","code":"context_too_large"}}`),
	}

	got := handler.toClaudeError(msg)

	if got.Type != "error" {
		t.Fatalf("type = %q, want error", got.Type)
	}
	if got.Error.Type != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error", got.Error.Type)
	}
	if got.Error.Message != "Your input exceeds the context window of this model. Please adjust your input and try again." {
		t.Fatalf("error.message = %q", got.Error.Message)
	}
}

func TestClaudeErrorExtractsClaudeStyleUpstreamJSON(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."},"request_id":"req_123"}`),
	}

	got := handler.toClaudeError(msg)

	if got.Error.Type != "rate_limit_error" {
		t.Fatalf("error.type = %q, want rate_limit_error", got.Error.Type)
	}
	if got.Error.Message != "This request would exceed your account's rate limit. Please try again later." {
		t.Fatalf("error.message = %q", got.Error.Message)
	}
}

func TestClaudeErrorSummarizesHTMLAndKeepsJSON(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	long := strings.Repeat("y", 400)
	htmlBody := "<html><head><script>secret()</script><title>Bad Gateway</title></head><body><h1>Bad Gateway</h1><p>" + long + "</p></body></html>"
	got := handler.toClaudeError(&interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      errors.New(htmlBody),
	})
	if strings.Contains(got.Error.Message, "<") || strings.Contains(got.Error.Message, "secret()") {
		t.Fatalf("error.message = %q, want plain summary", got.Error.Message)
	}
	if !strings.Contains(got.Error.Message, "Bad Gateway") {
		t.Fatalf("error.message = %q, want visible text", got.Error.Message)
	}
	if len([]rune(got.Error.Message)) > 200 {
		t.Fatalf("error.message length = %d, want <= 200", len([]rune(got.Error.Message)))
	}

	jsonMsg := handler.toClaudeError(&interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(`{"error":{"message":"keep <div> intact","type":"invalid_request_error"}}`),
	})
	if jsonMsg.Error.Message != "keep <div> intact" {
		t.Fatalf("JSON message = %q, want intact", jsonMsg.Error.Message)
	}
	if jsonMsg.Error.Type != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error", jsonMsg.Error.Type)
	}
}

func TestWriteClaudeErrorResponseUsesClaudeEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","code":"context_too_large"}}`),
	}

	handler.WriteErrorResponse(c, msg)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.Bytes()
	if got := gjson.GetBytes(body, "type").String(); got != "error" {
		t.Fatalf("type = %q, want error; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "error.message").String(); got != "Your input exceeds the context window of this model. Please adjust your input and try again." {
		t.Fatalf("error.message = %q; body=%s", got, body)
	}
}

func TestWriteClaudeErrorResponse_IncludesRetryAfterForModelCooldownDefaultSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	handler := &ClaudeCodeAPIHandler{}

	cooldownErr := coreauth.NewManager(nil, nil, nil)
	_ = cooldownErr
	// Create mock model cooldown error
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      coreauth.NewModelCooldownError("claude-sonnet-4-6", "claude", 20*time.Second),
	}

	handler.WriteErrorResponse(c, msg)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "20" {
		t.Fatalf("Retry-After = %q, want 20", got)
	}
}

func TestPendingClaudeStreamErrorUsesBufferedError(t *testing.T) {
	wantErr := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","code":"context_too_large"}}`),
	}
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- wantErr
	close(errs)

	gotErr, ok := handlers.PendingStreamError(errs)
	if !ok {
		t.Fatal("expected pending stream error")
	}
	if gotErr != wantErr {
		t.Fatalf("pending error = %p, want %p", gotErr, wantErr)
	}
}
