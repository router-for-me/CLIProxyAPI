package auth

import (
	"context"
	"errors"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestWrapStreamResultDoesNotMutateCallerMetadata(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	t.Cleanup(manager.StopAutoRefresh)

	original := map[string]any{"caller": "owned"}
	remaining := make(chan cliproxyexecutor.StreamChunk, 1)
	remaining <- cliproxyexecutor.StreamChunk{Err: errors.New("stream failed after commit")}
	close(remaining)

	result := manager.wrapStreamResult(
		context.Background(),
		&Auth{ID: "auth-b", Provider: "codex"},
		"codex",
		"gpt-5.6",
		"gpt-5.6",
		nil,
		[]cliproxyexecutor.StreamChunk{{Payload: []byte("data: ok\n\n")}},
		remaining,
		OAuthModelAliasResult{},
		false,
		cliproxyexecutor.Options{Metadata: original},
	)
	for range result.Chunks {
	}

	if _, mutated := original[cliproxyexecutor.ProviderOutputCommittedMetadataKey]; mutated {
		t.Fatalf("caller-owned metadata was mutated: %#v", original)
	}
}
