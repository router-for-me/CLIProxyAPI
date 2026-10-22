package executor

import (
	"context"
	"net/http"
	"testing"
)

const codexResponseMetadataBootstrapFrame = `{"type":"response.metadata","response_id":"resp_1","sequence_number":2,"metadata":{"openai_chatgpt_moderation_metadata":{"prompt":{}}}}`

func TestCodexExecutor_BootstrapBuffering_MetadataBeforeCapacityFailsOver(t *testing.T) {
	cases := []struct {
		name    string
		failure string
		status  int
	}{
		{"selected model at capacity", codexCapacityEvent, http.StatusTooManyRequests},
		{"server overloaded", codexOverloadEvent, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, codexResponseMetadataBootstrapFrame, tc.failure)
			defer server.Close()

			req, opts := codexTestRequest()
			result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
			if err == nil || result != nil {
				t.Fatalf("%s after response.metadata must fail before exposing a stream: result=%v, err=%v", tc.name, result, err)
			}
			if got := statusCodeFromTestError(t, err); got != tc.status {
				t.Fatalf("status code = %d, want %d", got, tc.status)
			}
		})
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_MetadataBeforeCapacityFailsOver(t *testing.T) {
	cases := []struct {
		name    string
		failure string
		status  int
	}{
		{"selected model at capacity", codexCapacityEvent, http.StatusTooManyRequests},
		{"server overloaded", codexOverloadEvent, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := codexWebsocketServer(t, codexCreatedEvent, codexInProgressEvent, codexResponseMetadataBootstrapFrame, tc.failure)
			defer server.Close()

			req, opts := codexWebsocketRequest()
			result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
			if err == nil || result != nil {
				t.Fatalf("%s after response.metadata must fail before exposing a stream: result=%v, err=%v", tc.name, result, err)
			}
			if got := statusCodeFromTestError(t, err); got != tc.status {
				t.Fatalf("status code = %d, want %d", got, tc.status)
			}
		})
	}
}
