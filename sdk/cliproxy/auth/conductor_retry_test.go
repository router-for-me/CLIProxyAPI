package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestShouldRetrySchedulerPick_AuthAvailabilityErrorsDoNotRetry(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "auth_not_found",
			err:  &Error{Code: "auth_not_found", Message: "no auth available"},
			want: false,
		},
		{
			name: "auth_unavailable",
			err:  &Error{Code: "auth_unavailable", Message: "no auth available"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRetrySchedulerPick(tt.err); got != tt.want {
				t.Fatalf("shouldRetrySchedulerPick() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadStreamBootstrap_OpenAIResponsesPreambleOnlyDoesNotStartStream(t *testing.T) {
	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("event: response.created\ndata: {\"type\":\"response.created\"}")}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("event: response.in_progress\ndata: {\"type\":\"response.in_progress\"}")}
	close(ch)

	buffered, closed, err := readStreamBootstrap(context.Background(), cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}, ch)
	if err != nil {
		t.Fatalf("readStreamBootstrap error: %v", err)
	}
	if !closed {
		t.Fatal("expected stream to be closed")
	}
	if len(buffered) != 2 {
		t.Fatalf("buffered chunks = %d, want 2", len(buffered))
	}
	if streamBootstrapBufferedStarted(cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, buffered) {
		t.Fatal("expected preamble-only responses stream to remain in bootstrap")
	}
}

func TestReadStreamBootstrap_OpenAIResponsesContentStartsStream(t *testing.T) {
	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("event: response.created\ndata: {\"type\":\"response.created\"}")}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}")}

	buffered, closed, err := readStreamBootstrap(context.Background(), cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}, ch)
	if err != nil {
		t.Fatalf("readStreamBootstrap error: %v", err)
	}
	if closed {
		t.Fatal("expected stream to remain open after content")
	}
	if len(buffered) != 2 {
		t.Fatalf("buffered chunks = %d, want 2", len(buffered))
	}
	if !streamBootstrapBufferedStarted(cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, buffered) {
		t.Fatal("expected content delta to start responses stream")
	}
	close(ch)
}

func TestReadStreamBootstrap_OpenAIResponsesPreambleTimesOut(t *testing.T) {
	previousTimeout := openAIResponsesBootstrapTimeout
	openAIResponsesBootstrapTimeout = 10 * time.Millisecond
	t.Cleanup(func() {
		openAIResponsesBootstrapTimeout = previousTimeout
	})

	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("event: response.created\ndata: {\"type\":\"response.created\"}")}

	buffered, closed, err := readStreamBootstrap(context.Background(), cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}, ch)
	if err == nil {
		t.Fatal("expected bootstrap timeout error")
	}
	if closed {
		t.Fatal("expected stream to remain open on timeout")
	}
	if len(buffered) != 1 {
		t.Fatalf("buffered chunks = %d, want 1", len(buffered))
	}
	authErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if authErr.Code != "empty_stream" {
		t.Fatalf("error code = %q, want empty_stream", authErr.Code)
	}
	close(ch)
}
