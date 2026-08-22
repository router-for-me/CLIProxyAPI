package pluginhost

import (
	"context"
	"errors"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestWrapStreamEmptyCompletion_PrefersDetectedErrorOverTerminalEmptiness covers a
// stream whose recognized frames carry no content and whose real provider error
// arrives as an SSE error event that is newline-terminated but never followed by the
// blank separator line. flushData() only runs on that blank line or from Finish(),
// so the detected provider error does not exist yet while the stream is being
// observed; judging emptiness before consulting it would replace a routable
// invalid_api_key with a generic empty_completion.
func TestWrapStreamEmptyCompletion_PrefersDetectedErrorOverTerminalEmptiness(t *testing.T) {
	chunks := make(chan coreexecutor.StreamChunk, 3)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: error\ndata: {\"error\":{\"code\":\"invalid_api_key\",\"message\":\"invalid api key\"}}\n")}
	close(chunks)

	res := wrapStreamEmptyCompletion(context.Background(), &coreexecutor.StreamResult{Chunks: chunks})
	var received []coreexecutor.StreamChunk
	for c := range res.Chunks {
		received = append(received, c)
	}

	if len(received) != 1 {
		t.Fatalf("expected 1 chunk carrying the detected provider error, got %d", len(received))
	}
	if received[0].Err == nil {
		t.Fatalf("expected an error chunk, got payload: %s", string(received[0].Payload))
	}
	var authErr *coreauth.Error
	if !errors.As(received[0].Err, &authErr) {
		t.Fatalf("expected *coreauth.Error, got %v", received[0].Err)
	}
	if authErr.Code != "invalid_api_key" {
		t.Fatalf("expected the provider error to survive terminal emptiness, got code %q (%v)", authErr.Code, received[0].Err)
	}
}
