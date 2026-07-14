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
