package claude

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestClaudeErrorTypeFromStatus(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, "invalid_request_error"},
		{http.StatusRequestTimeout, "timeout_error"},
		{http.StatusTooManyRequests, "rate_limit_error"},
		{http.StatusInternalServerError, "api_error"},
		{http.StatusGatewayTimeout, "timeout_error"},
	} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			if got := claudeErrorTypeFromStatus(tt.status); got != tt.want {
				t.Fatalf("error.type = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClaudeIncompleteStreamError(t *testing.T) {
	const message = "stream error: stream disconnected before completion: stream closed before response.completed"
	for _, streaming := range []bool{false, true} {
		name := "JSON"
		if streaming {
			name = "committed SSE"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
			msg := &interfaces.ErrorMessage{
				StatusCode: http.StatusRequestTimeout,
				Error:      errors.New(message),
			}
			wantStatus := http.StatusRequestTimeout
			if streaming {
				c.Header("Content-Type", "text/event-stream")
				_, _ = c.Writer.Write([]byte("event: message_start\ndata: {}\n\n"))
				c.Writer.Flush()
				errs := make(chan *interfaces.ErrorMessage, 1)
				errs <- msg
				close(errs)
				handler.forwardClaudeStream(c, c.Writer, func(err error) {
					if err != msg.Error {
						t.Errorf("cancel error = %v, want %v", err, msg.Error)
					}
				}, nil, errs)
				wantStatus = http.StatusOK
			} else {
				handler.WriteErrorResponse(c, msg)
			}
			if recorder.Code != wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, wantStatus)
			}
			body := recorder.Body.String()
			if streaming {
				const prefix = "event: error\ndata: "
				if strings.Count(body, prefix) != 1 {
					t.Fatalf("expected one terminal error event; body=%s", body)
				}
				_, body, _ = strings.Cut(body, prefix)
				body = strings.TrimSpace(body)
			}
			if got := gjson.Get(body, "type").String(); got != "error" {
				t.Errorf("type = %q, want error", got)
			}
			if got := gjson.Get(body, "error.type").String(); got != "timeout_error" {
				t.Errorf("error.type = %q, want timeout_error", got)
			}
			if got := gjson.Get(body, "error.message").String(); got != message {
				t.Errorf("error.message = %q, want %q", got, message)
			}
		})
	}
}

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

type responseBodyOnlyClaudeError struct {
	body []byte
}

func (e responseBodyOnlyClaudeError) Error() string        { return "wrapped upstream error" }
func (e responseBodyOnlyClaudeError) ResponseBody() []byte { return e.body }

func TestClaudeErrorMarksWrappedMissingThreadForClientReplay(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusNotFound,
		Error:      responseBodyOnlyClaudeError{body: []byte(`{"type":"error","error":{"type":"not_found_error","message":"No thread state was found for the requested previous_message_id."}}`)},
	}

	body, errMarshal := json.Marshal(handler.toClaudeError(msg))
	if errMarshal != nil {
		t.Fatalf("marshal Claude error: %v", errMarshal)
	}
	if got := gjson.GetBytes(body, "error.details.error_code").String(); got != "thread_not_found" {
		t.Fatalf("error.details.error_code = %q, want thread_not_found; body=%s", got, body)
	}
}

func TestClaudeErrorMarksMissingThreadForClientReplay(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusNotFound,
		Error:      errors.New(`{"type":"error","error":{"type":"not_found_error","message":"No thread state was found for the requested previous_message_id. Replay the full conversation with thread create to start a new Thread."}}`),
	}

	body, errMarshal := json.Marshal(handler.toClaudeError(msg))
	if errMarshal != nil {
		t.Fatalf("marshal Claude error: %v", errMarshal)
	}
	if got := gjson.GetBytes(body, "error.details.error_code").String(); got != "thread_not_found" {
		t.Fatalf("error.details.error_code = %q, want thread_not_found; body=%s", got, body)
	}
}

func TestWriteClaudeDirectErrorMarksMissingThreadForClientReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode:     http.StatusNotFound,
		DirectResponse: true,
		Body:           []byte(`{"type":"error","error":{"type":"not_found_error","message":"No thread state was found for the requested previous_message_id. Replay the full conversation with thread create to start a new Thread."}}`),
	}

	handler.WriteErrorResponse(c, msg)

	if got := gjson.GetBytes(recorder.Body.Bytes(), "error.details.error_code").String(); got != "thread_not_found" {
		t.Fatalf("error.details.error_code = %q, want thread_not_found; body=%s", got, recorder.Body.Bytes())
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
