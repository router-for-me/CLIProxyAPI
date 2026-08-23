package auth

import (
	"context"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestReadStreamBootstrapFinalizesDetectorAtEOF covers an upstream that closes the
// channel right after an SSE error event whose data line is newline-terminated but
// never followed by the blank separator line. flushData() only runs on that blank
// line or from finish(), so without finalizing the bootstrap state the provider
// error stays buffered, the bootstrap reports a clean close, and the caller gets an
// empty stream instead of a routable failure it can fail over on.
func TestReadStreamBootstrapFinalizesDetectorAtEOF(t *testing.T) {
	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("event: error\ndata: {\"error\":{\"code\":\"invalid_api_key\",\"message\":\"invalid api key\"}}\n")}
	close(ch)

	buffered, closed, err := readStreamBootstrap(context.Background(), ch)
	if err == nil {
		t.Fatalf("readStreamBootstrap() error = nil, want the in-band provider error (closed=%v, buffered=%d)", closed, len(buffered))
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("readStreamBootstrap() error = %v, want the invalid api key provider error", err)
	}
	if closed {
		t.Fatal("closed = true, want false so the caller can fail over")
	}
	if len(buffered) != 0 {
		t.Fatalf("len(buffered) = %d, want 0 when the provider error propagates", len(buffered))
	}
}

func TestReadStreamBootstrapWaitsForUsageAfterStop(t *testing.T) {
	ch := make(chan cliproxyexecutor.StreamChunk, 2)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"completion_tokens\":0}}\n\n")}
	close(ch)

	buffered, closed, err := readStreamBootstrap(context.Background(), ch)
	if err != nil {
		t.Fatalf("readStreamBootstrap() error = %v, want nil", err)
	}
	if !closed {
		t.Fatal("closed = false, want true after terminal usage with zero tokens")
	}
	if len(buffered) != 2 {
		t.Fatalf("len(buffered) = %d, want 2 (stop + usage)", len(buffered))
	}

	ch2 := make(chan cliproxyexecutor.StreamChunk, 2)
	ch2 <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")}
	ch2 <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"completion_tokens\":3}}\n\n")}
	close(ch2)

	buffered2, closed2, err2 := readStreamBootstrap(context.Background(), ch2)
	if err2 != nil {
		t.Fatalf("readStreamBootstrap() error = %v, want nil", err2)
	}
	if closed2 {
		t.Fatal("closed = true, want false after meaningful usage with positive tokens")
	}
	if len(buffered2) != 2 {
		t.Fatalf("len(buffered2) = %d, want 2 (stop + usage)", len(buffered2))
	}
}
