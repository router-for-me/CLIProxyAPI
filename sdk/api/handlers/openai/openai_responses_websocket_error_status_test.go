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

func TestResponsesWebsocketErrorMessageFromPayloadMapsBadRequestType(t *testing.T) {
	errMsg := responsesWebsocketErrorMessageFromPayload([]byte(`{"type":"error","error":{"type":"bad_request_error","message":"bad request"}}`))
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

func TestResponsesWebsocketErrorMessageFromPayloadMapsAuthStatus(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		wantStatus int
	}{
		{name: "authentication type", payload: `{"type":"error","error":{"type":"authentication_error","message":"expired token"}}`, wantStatus: http.StatusUnauthorized},
		{name: "invalid API key", payload: `{"type":"error","code":"invalid_api_key"}`, wantStatus: http.StatusUnauthorized},
		{name: "unauthorized", payload: `{"type":"error","code":"unauthorized"}`, wantStatus: http.StatusUnauthorized},
		{name: "permission type", payload: `{"type":"error","error":{"type":"permission_error","message":"access denied"}}`, wantStatus: http.StatusForbidden},
		{name: "forbidden", payload: `{"type":"error","code":"forbidden"}`, wantStatus: http.StatusForbidden},
		{name: "permission denied", payload: `{"type":"error","code":"permission_denied"}`, wantStatus: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errMsg := responsesWebsocketErrorMessageFromPayload([]byte(test.payload))
			if errMsg == nil {
				t.Fatal("error message is nil")
			}
			if errMsg.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", errMsg.StatusCode, test.wantStatus)
			}
			if !shouldReleaseResponsesWebsocketPinnedAuth(errMsg) {
				t.Fatal("auth error should release pinned auth")
			}
		})
	}
}

func TestResponsesWebsocketErrorMessageFromPayloadMapsNotFoundStatus(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "nested type", payload: `{"type":"error","error":{"type":"not_found_error","message":"model not found"}}`},
		{name: "nested code", payload: `{"type":"error","error":{"code":"model_not_found","message":"model not found"}}`},
		{name: "top-level code", payload: `{"type":"error","code":"not_found","message":"resource not found"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errMsg := responsesWebsocketErrorMessageFromPayload([]byte(test.payload))
			if errMsg == nil {
				t.Fatal("error message is nil")
			}
			if errMsg.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusNotFound)
			}
		})
	}
}
