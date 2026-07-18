package openai

import (
	"net/http"
	"testing"
)

func TestResponsesWebsocketErrorMessageFromPayloadMapsConnectionLimitStatus(t *testing.T) {
	errMsg := responsesWebsocketErrorMessageFromPayload([]byte(`{"type":"error","code":"websocket_connection_limit_reached","message":"too many websocket connections"}`))
	if errMsg == nil {
		t.Fatal("error message is nil")
	}
	if errMsg.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusTooManyRequests)
	}
	if !shouldReleaseResponsesWebsocketPinnedAuth(errMsg) {
		t.Fatal("connection-limit error should release pinned auth")
	}
}

func TestResponsesWebsocketErrorMessageFromPayloadMapsStringConnectionLimitStatus(t *testing.T) {
	errMsg := responsesWebsocketErrorMessageFromPayload([]byte(`{"type":"error","error":"websocket_connection_limit_reached"}`))
	if errMsg == nil {
		t.Fatal("error message is nil")
	}
	if errMsg.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusTooManyRequests)
	}
	if !shouldReleaseResponsesWebsocketPinnedAuth(errMsg) {
		t.Fatal("string connection-limit error should release pinned auth")
	}
}

func TestResponsesWebsocketErrorMessageFromPayloadUsesTopLevelErrorType(t *testing.T) {
	errMsg := responsesWebsocketErrorMessageFromPayload([]byte(`{"type":"error","error_type":"invalid_request_error","message":"bad request"}`))
	if errMsg == nil {
		t.Fatal("error message is nil")
	}
	if errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusBadRequest)
	}
}

func TestResponsesWebsocketErrorMessageFromPayloadMapsUsageLimitStatus(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "nested type", payload: `{"type":"error","error":{"type":"usage_limit_reached","message":"usage limit reached"}}`},
		{name: "top-level code", payload: `{"type":"error","code":"usage_limit_reached","message":"usage limit reached"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errMsg := responsesWebsocketErrorMessageFromPayload([]byte(test.payload))
			if errMsg == nil {
				t.Fatal("error message is nil")
			}
			if errMsg.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusTooManyRequests)
			}
			if !shouldReleaseResponsesWebsocketPinnedAuth(errMsg) {
				t.Fatal("usage-limit error should release pinned auth")
			}
		})
	}
}
