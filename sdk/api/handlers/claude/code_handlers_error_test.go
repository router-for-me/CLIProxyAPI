package claude

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/tidwall/gjson"
)

func TestClaudeErrorNormalizesContextLimitForClientCompaction(t *testing.T) {
	tests := []struct {
		name    string
		errText string
		want    string
	}{
		{
			name:    "normalized executor code",
			errText: `{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","code":"context_too_large"}}`,
			want:    "Prompt is too long: Your input exceeds the context window of this model. Please adjust your input and try again.",
		},
		{
			name:    "upstream context code",
			errText: `{"error":{"message":"Maximum context length exceeded.","type":"invalid_request_error","code":"context_length_exceeded"}}`,
			want:    "Prompt is too long: Maximum context length exceeded.",
		},
		{
			name:    "recognized message remains unchanged",
			errText: `{"error":{"message":"Prompt is too long: Maximum context length exceeded.","type":"invalid_request_error","code":"context_too_large"}}`,
			want:    "Prompt is too long: Maximum context length exceeded.",
		},
		{
			name:    "unrelated bad request remains unchanged",
			errText: `{"error":{"message":"Invalid request body.","type":"invalid_request_error","code":"invalid_request"}}`,
			want:    "Invalid request body.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &ClaudeCodeAPIHandler{}
			msg := &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      errors.New(tt.errText),
			}

			got := handler.toClaudeError(msg)

			if got.Type != "error" {
				t.Fatalf("type = %q, want error", got.Type)
			}
			if got.Error.Type != "invalid_request_error" {
				t.Fatalf("error.type = %q, want invalid_request_error", got.Error.Type)
			}
			if got.Error.Message != tt.want {
				t.Fatalf("error.message = %q, want %q", got.Error.Message, tt.want)
			}
		})
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
	if got := gjson.GetBytes(body, "error.message").String(); got != "Prompt is too long: Your input exceeds the context window of this model. Please adjust your input and try again." {
		t.Fatalf("error.message = %q; body=%s", got, body)
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

	gotErr, ok := pendingClaudeStreamError(errs)
	if !ok {
		t.Fatal("expected pending stream error")
	}
	if gotErr != wantErr {
		t.Fatalf("pending error = %p, want %p", gotErr, wantErr)
	}
}
