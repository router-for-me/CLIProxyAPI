package executor

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestValidateRemoteCompactionV2Response(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		eventType string
		wantErr   string
	}{
		{
			name:      "completed event with one compaction item",
			payload:   `{"type":"response.completed","response":{"status":"completed","output":[{"type":"compaction","encrypted_content":"opaque"}]}}`,
			eventType: "response.completed",
		},
		{
			name:      "completed event missing response status fails closed",
			payload:   `{"type":"response.completed","response":{"output":[{"type":"compaction","encrypted_content":"opaque"}]}}`,
			eventType: "response.completed",
			wantErr:   "missing completed status",
		},
		{
			name:      "completed event does not infer status from top level",
			payload:   `{"type":"response.completed","status":"completed","response":{"output":[{"type":"compaction","encrypted_content":"opaque"}]}}`,
			eventType: "response.completed",
			wantErr:   "missing completed status",
		},
		{
			name:      "nested incomplete status takes precedence",
			payload:   `{"type":"response.completed","status":"completed","response":{"status":"incomplete","output":[{"type":"compaction","encrypted_content":"opaque"}]}}`,
			eventType: "response.completed",
			wantErr:   "response.incomplete",
		},
		{
			name:      "wrong response status fails closed",
			payload:   `{"type":"response.completed","response":{"status":"failed","output":[{"type":"compaction","encrypted_content":"opaque"}]}}`,
			eventType: "response.completed",
			wantErr:   "got failed",
		},
		{
			name:      "two compaction output items fail closed",
			payload:   `{"type":"response.completed","response":{"status":"completed","output":[{"type":"compaction","encrypted_content":"one"},{"type":"compaction","encrypted_content":"two"}]}}`,
			eventType: "response.completed",
			wantErr:   "got 2 from 2 output items",
		},
		{
			name:      "zero output items fail closed",
			payload:   `{"type":"response.completed","response":{"status":"completed","output":[]}}`,
			eventType: "response.completed",
			wantErr:   "got 0 from 0 output items",
		},
		{
			name:      "function call output is not compaction",
			payload:   `{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","name":"foo","arguments":"{}"}]}}`,
			eventType: "response.completed",
			wantErr:   "got 0 from 1 output items",
		},
		{
			name:      "incomplete event is never successful",
			payload:   `{"type":"response.incomplete","response":{"status":"incomplete","output":[{"type":"compaction","encrypted_content":"opaque"}]}}`,
			eventType: "response.incomplete",
			wantErr:   "response.incomplete",
		},
		{
			name:    "incomplete response status is never successful",
			payload: `{"id":"resp_1","object":"response","status":"incomplete","output":[{"type":"compaction","encrypted_content":"opaque"}]}`,
			wantErr: "response.incomplete",
		},
		{
			name:      "nested incomplete response status is never successful",
			payload:   `{"type":"response.incomplete","response":{"status":"incomplete","output":[{"type":"compaction","encrypted_content":"opaque"}]}}`,
			eventType: "response.completed",
			wantErr:   "response.incomplete",
		},
		{
			name:      "completed event with ordinary output fails",
			payload:   `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message"}]}}`,
			eventType: "response.completed",
			wantErr:   "got 0 from 1 output items",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRemoteCompactionV2Response([]byte(test.payload), test.eventType)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateRemoteCompactionV2Response() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateRemoteCompactionV2Response() succeeded, want error")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
			var status interface{ StatusCode() int }
			if !errors.As(err, &status) || status.StatusCode() != http.StatusBadGateway {
				t.Fatalf("error status = %T/%v, want HTTP %d", err, status, http.StatusBadGateway)
			}
			var requestErr cliproxyexecutor.RequestScopedError
			if !errors.As(err, &requestErr) || requestErr == nil || !requestErr.IsRequestScoped() {
				t.Fatalf("error = %T/%v, want request-scoped error", err, err)
			}
		})
	}
}

func TestRemoteCompactionV2EventType(t *testing.T) {
	if got := remoteCompactionV2EventType([]byte(`data: {"type":"response.incomplete","response":{"status":"incomplete"}}\n\n`)); got != "response.incomplete" {
		t.Fatalf("event type = %q, want response.incomplete", got)
	}
	if got := remoteCompactionV2EventType([]byte(`{"type":"response.completed","response":{"status":"completed"}}`)); got != "response.completed" {
		t.Fatalf("event type = %q, want response.completed", got)
	}
	for _, eventType := range []string{"response.failed", "response.done", "error"} {
		t.Run(eventType, func(t *testing.T) {
			payload := []byte(`{"type":"` + eventType + `","status":"completed","response":{"status":"completed","output":[{"type":"compaction","encrypted_content":"opaque"}]}}`)
			got := remoteCompactionV2EventType(payload)
			if got != eventType {
				t.Fatalf("event type = %q, want %q", got, eventType)
			}
			if err := validateRemoteCompactionV2Response(payload, got); err == nil {
				t.Fatalf("validateRemoteCompactionV2Response() succeeded for %s", eventType)
			} else if !strings.Contains(err.Error(), "expected response.completed") {
				t.Fatalf("error = %v, want terminal event rejection", err)
			}
		})
	}
}
