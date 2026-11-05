package wsrelay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestDecodeErrorStatus(t *testing.T) {
	tests := []struct {
		name       string
		status     float64
		wantStatus int
	}{
		{name: "upstream error", status: http.StatusUnauthorized, wantStatus: http.StatusUnauthorized},
		{name: "lower error boundary", status: http.StatusBadRequest, wantStatus: http.StatusBadRequest},
		{name: "upper error boundary", status: 599, wantStatus: 599},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{"error": "invalid api key", "status": test.status}
			err := decodeError(payload)
			var statusCoder interface{ StatusCode() int }
			if !errors.As(err, &statusCoder) {
				t.Fatalf("decodeError() does not expose status: %v", err)
			}
			if statusCoder.StatusCode() != test.wantStatus {
				t.Fatalf("StatusCode() = %d, want %d", statusCoder.StatusCode(), test.wantStatus)
			}
			wantMessage := fmt.Sprintf("invalid api key (status=%d)", test.wantStatus)
			if err.Error() != wantMessage {
				t.Fatalf("Error() = %q, want %q", err.Error(), wantMessage)
			}
		})
	}

	withoutValidStatus := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{name: "success", payload: map[string]any{"error": "invalid api key", "status": float64(http.StatusOK)}, want: "invalid api key"},
		{name: "out of range", payload: map[string]any{"error": "invalid api key", "status": float64(600)}, want: "invalid api key"},
		{name: "fractional", payload: map[string]any{"error": "invalid api key", "status": 401.5}, want: "invalid api key"},
		{name: "missing", payload: map[string]any{"error": "invalid api key"}, want: "invalid api key"},
		{name: "nil payload", want: "wsrelay: unknown error"},
	}
	for _, test := range withoutValidStatus {
		t.Run(test.name, func(t *testing.T) {
			err := decodeError(test.payload)
			var statusCoder interface{ StatusCode() int }
			if errors.As(err, &statusCoder) {
				t.Fatalf("decodeError() unexpectedly exposes status %d", statusCoder.StatusCode())
			}
			if err.Error() != test.want {
				t.Fatalf("Error() = %q, want %q", err.Error(), test.want)
			}
		})
	}
}

func TestReceiveNonStreamRejectsRelayCloseBeforeTerminalEvent(t *testing.T) {
	responses := make(chan Message, 2)
	responses <- Message{Type: MessageTypeStreamStart, Payload: map[string]any{"status": float64(http.StatusOK)}}
	responses <- Message{Type: MessageTypeStreamChunk, Payload: map[string]any{"data": "partial"}}
	close(responses)

	response, err := receiveNonStream(t.Context(), responses)
	if err == nil {
		t.Fatalf("receiveNonStream() response = %+v, want incomplete relay response failure", response)
	}
	if err.Error() != "wsrelay: connection closed during response" {
		t.Fatalf("receiveNonStream() error = %q", err)
	}
	if response != nil {
		t.Fatalf("receiveNonStream() response = %+v, want nil", response)
	}
}

func TestReceiveNonStreamAcceptsExplicitTerminalEvent(t *testing.T) {
	responses := make(chan Message, 3)
	responses <- Message{Type: MessageTypeStreamStart, Payload: map[string]any{"status": float64(http.StatusAccepted)}}
	responses <- Message{Type: MessageTypeStreamChunk, Payload: map[string]any{"data": "complete"}}
	responses <- Message{Type: MessageTypeStreamEnd}
	close(responses)

	response, err := receiveNonStream(t.Context(), responses)
	if err != nil {
		t.Fatalf("receiveNonStream() error = %v", err)
	}
	if response.Status != http.StatusAccepted {
		t.Fatalf("receiveNonStream() status = %d, want %d", response.Status, http.StatusAccepted)
	}
	if string(response.Body) != "complete" {
		t.Fatalf("receiveNonStream() body = %q, want complete", response.Body)
	}
}

func TestReceiveNonStreamReturnsCanceledContextWhenResponseCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	responses := make(chan Message)
	close(responses)

	response, err := receiveNonStream(ctx, responses)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("receiveNonStream() error = %v, want context.Canceled", err)
	}
	if response != nil {
		t.Fatalf("receiveNonStream() response = %+v, want nil", response)
	}
}
