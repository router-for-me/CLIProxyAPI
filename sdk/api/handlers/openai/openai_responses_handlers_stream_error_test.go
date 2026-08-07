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
