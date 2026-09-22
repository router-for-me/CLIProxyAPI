package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestLooksLikeOpenAIResponsesSSE(t *testing.T) {
	if !looksLikeOpenAIResponsesSSE([]byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n")) {
		t.Fatal("expected OpenAI Responses SSE detection")
	}
	if looksLikeOpenAIResponsesSSE([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")) {
		t.Fatal("expected Claude Messages SSE to not match Responses detector")
	}
	if looksLikeOpenAIResponsesSSE(nil) {
		t.Fatal("empty payload should not match")
	}
}

func TestStartExecutorStreamRunsOrchestrationAfterRPCReturns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan string, 1)
	req := rpcExecutorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{Stream: true},
		StreamID:        "stream-1",
		HostCallbackID:  "callback-1",
	}

	raw, errStart := startExecutorStream(req, func(ctx context.Context, exec pluginapi.ExecutorRequest, hostCallbackID, pluginStreamID string) error {
		if hostCallbackID != "callback-1" || pluginStreamID != "stream-1" {
			t.Errorf("runner ids = %q/%q, want callback-1/stream-1", hostCallbackID, pluginStreamID)
		}
		close(started)
		<-release
		return nil
	}, func(streamID, errMsg string) {
		closed <- streamID + "|" + errMsg
	})
	if errStart != nil {
		t.Fatalf("startExecutorStream() error = %v", errStart)
	}
	if !strings.Contains(string(raw), "text/event-stream") {
		t.Fatalf("response does not include stream headers: %s", raw)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("orchestration did not start")
	}
	select {
	case got := <-closed:
		t.Fatalf("stream closed before orchestration finished: %q", got)
	default:
	}

	close(release)
	select {
	case got := <-closed:
		if got != "stream-1|" {
			t.Fatalf("close call = %q, want stream-1|", got)
		}
	case <-time.After(time.Second):
		t.Fatal("stream was not closed after orchestration finished")
	}
}
