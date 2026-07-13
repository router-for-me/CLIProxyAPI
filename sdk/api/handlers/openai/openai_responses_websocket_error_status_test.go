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
