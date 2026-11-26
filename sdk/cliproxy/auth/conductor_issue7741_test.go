package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestIssue7741WrapStreamResultClosesOnCancellationDuringUpstreamStall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	remaining := make(chan cliproxyexecutor.StreamChunk)
	t.Cleanup(func() { close(remaining) })

	manager := NewManager(nil, nil, nil)
	result := manager.wrapStreamResult(
		ctx,
		&Auth{ID: "issue7741-auth", Provider: "issue7741"},
		"issue7741",
		"issue7741-model",
		"issue7741-model",
		nil,
		nil,
		remaining,
		OAuthModelAliasResult{},
		false,
		cliproxyexecutor.Options{},
		1,
	)

	go func() {
		remaining <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.created"}`)}
	}()

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("output closed before forwarding the first upstream chunk")
		}
		if len(chunk.Payload) == 0 {
			t.Fatal("forwarded chunk has an empty payload")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the first upstream chunk")
	}

	cancel()

	select {
	case _, ok := <-result.Chunks:
		if ok {
			t.Fatal("output produced another chunk after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("output did not close when context was canceled during upstream stall")
	}
}
