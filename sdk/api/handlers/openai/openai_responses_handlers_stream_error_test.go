package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

// TestForwardResponsesStreamExposesOnlyClientErrors pins the SSE side: only
// request-shape failures reach the client. Credential, quota and transport
// failures end the stream silently so the client retries on its own.
func TestForwardResponsesStreamExposesOnlyClientErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		status      int
		message     string
		wantExposed bool
	}{
		{
			name:        "bad request",
			status:      http.StatusBadRequest,
			message:     `{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`,
			wantExposed: true,
		},
		{
			// Observed in production: the same cyber_policy rejection arrives with 502
			// when it is surfaced through the websocket disconnect channel.
			name:        "cyber policy behind bad gateway status",
			status:      http.StatusBadGateway,
			message:     `{"error":{"type":"invalid_request","code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk.","param":null}}`,
			wantExposed: true,
		},
		{
			name:        "context length exceeded behind bad gateway status",
			status:      http.StatusBadGateway,
			message:     `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window."}}`,
			wantExposed: true,
		},
		{name: "conflict", status: http.StatusConflict, message: "conflict", wantExposed: true},
		{name: "message too big", status: http.StatusRequestEntityTooLarge, message: "too large", wantExposed: true},
		{name: "unprocessable entity", status: http.StatusUnprocessableEntity, message: "invalid input", wantExposed: true},
		{name: "authentication", status: http.StatusUnauthorized, message: "invalid credential"},
		{name: "payment required", status: http.StatusPaymentRequired, message: "insufficient credits"},
		{name: "quota error", status: http.StatusTooManyRequests, message: "usage limit reached"},
		{name: "request timeout", status: http.StatusRequestTimeout, message: "upstream timeout"},
		{name: "transport error", status: http.StatusInternalServerError, message: "unexpected EOF"},
		{name: "upstream websocket drop", status: http.StatusInternalServerError,
			message: `{"error":{"message":"websocket: close 1006 (abnormal closure): unexpected EOF","type":"server_error","code":"internal_server_error"}}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
			h := NewOpenAIResponsesAPIHandler(base)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

			flusher, ok := c.Writer.(http.Flusher)
			if !ok {
				t.Fatal("expected gin writer to implement http.Flusher")
			}

			data := make(chan []byte)
			errs := make(chan *interfaces.ErrorMessage, 1)
			errs <- &interfaces.ErrorMessage{StatusCode: tc.status, Error: errors.New(tc.message)}
			close(errs)

			h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
			body := recorder.Body.String()
			exposed := strings.Contains(body, `"type":"error"`)
			if exposed != tc.wantExposed {
				t.Fatalf("error exposed = %t, want %t: %q", exposed, tc.wantExposed, body)
			}
			if exposed && strings.Contains(body, `"error":{`) {
				t.Fatalf("expected streaming error chunk, got HTTP error body: %q", body)
			}
		})
	}
}

func TestForwardResponsesStreamUsesResponseFailedForCodex(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(`{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`),
	}
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("missing response.failed event: %q", body)
	}
	if strings.Contains(body, "event: error") {
		t.Fatalf("unexpected legacy error event for Codex: %q", body)
	}
	if !strings.Contains(body, `"type":"invalid_request"`) || !strings.Contains(body, `"code":"cyber_policy"`) {
		t.Fatalf("missing nested Codex error detail: %q", body)
	}
}

func TestForwardResponsesStreamTerminalErrorFollowsPartialOutputSequence(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	go func() {
		data <- []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_")
		data <- []byte("number\":7,\"delta\":\"partial\"}\n\n")
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New(`{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`),
		}
	}()

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
	body := recorder.Body.String()
	failedIndex := strings.LastIndex(body, "data: ")
	if failedIndex < 0 {
		t.Fatalf("missing terminal payload: %q", body)
	}
	failedLine := strings.SplitN(body[failedIndex+len("data: "):], "\n", 2)[0]
	if got := gjson.Get(failedLine, "sequence_number").Int(); got != 8 {
		t.Fatalf("terminal sequence_number = %d, want 8; body=%q", got, body)
	}
	if strings.Contains(body, "response.completed") || strings.Contains(body, "[DONE]") {
		t.Fatalf("terminal error stream must not emit completed/done: %q", body)
	}
}

func TestForwardChatAsResponsesStreamTerminalErrorFollowsConvertedOutputSequence(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	go func() {
		data <- []byte(`data: {"id":"chat-partial","object":"chat.completion.chunk","created":1773896263,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}`)
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New(`{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`),
		}
	}()

	originalRequest := []byte(`{"model":"test-model"}`)
	var param any
	h.forwardChatAsResponsesStream(c, flusher, func(error) {}, data, errs, c.Request.Context(), "test-model", originalRequest, &param)
	body := recorder.Body.String()
	lastOutputSequence := int64(-1)
	terminalSequence := int64(-1)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if !gjson.Valid(payload) {
			continue
		}
		sequence := gjson.Get(payload, "sequence_number").Int()
		if gjson.Get(payload, "type").String() == "response.failed" {
			terminalSequence = sequence
			continue
		}
		if sequence > lastOutputSequence {
			lastOutputSequence = sequence
		}
	}
	if lastOutputSequence < 0 || terminalSequence <= lastOutputSequence {
		t.Fatalf("terminal sequence_number = %d, want > converted output %d; body=%q", terminalSequence, lastOutputSequence, body)
	}
	if strings.Contains(body, "response.completed") || strings.Contains(body, "[DONE]") {
		t.Fatalf("terminal error stream must not emit completed/done: %q", body)
	}
}

func TestForwardChatAsResponsesStreamUsesResponsesTerminalErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name      string
		status    int
		message   string
		userAgent string
		wantEvent string
		wantType  string
	}{
		{name: "retryable quota error stays silent", status: http.StatusTooManyRequests, message: "usage limit reached"},
		{name: "non-Codex client gets Responses error", status: http.StatusBadRequest, message: `{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`, wantEvent: "event: error", wantType: `"type":"error"`},
		{name: "Codex client gets response failed", status: http.StatusBadRequest, message: `{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`, userAgent: "Codex Desktop/26.803.41515", wantEvent: "event: response.failed", wantType: `"type":"response.failed"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
			h := NewOpenAIResponsesAPIHandler(base)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set("User-Agent", tc.userAgent)

			flusher, ok := c.Writer.(http.Flusher)
			if !ok {
				t.Fatal("expected gin writer to implement http.Flusher")
			}

			data := make(chan []byte)
			errs := make(chan *interfaces.ErrorMessage, 1)
			errs <- &interfaces.ErrorMessage{
				StatusCode: tc.status,
				Error:      errors.New(tc.message),
			}
			close(errs)

			var param any
			h.forwardChatAsResponsesStream(c, flusher, func(error) {}, data, errs, c.Request.Context(), "test-model", nil, &param)
			body := recorder.Body.String()
			if tc.wantEvent == "" {
				if body != "" {
					t.Fatalf("retryable error exposed: %q", body)
				}
				return
			}
			if !strings.Contains(body, tc.wantEvent) || !strings.Contains(body, tc.wantType) {
				t.Fatalf("terminal event = %q, want %q with %q", body, tc.wantEvent, tc.wantType)
			}
		})
	}
}
