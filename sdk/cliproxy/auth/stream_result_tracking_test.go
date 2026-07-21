package auth

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type streamResultTrackingError struct {
	status int
}

func (e streamResultTrackingError) Error() string {
	return fmt.Sprintf("stream result failed with status %d", e.status)
}

func (e streamResultTrackingError) StatusCode() int { return e.status }

func TestWrapStreamResultTracksResultErrorWithoutHidingPayload(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-result-error", Provider: "codex", Status: StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	payload := []byte(`{"type":"error","error":{"status":429,"message":"rate limited"}}`)
	remaining := make(chan cliproxyexecutor.StreamChunk, 1)
	remaining <- cliproxyexecutor.StreamChunk{
		Payload:   payload,
		ResultErr: streamResultTrackingError{status: http.StatusTooManyRequests},
	}
	close(remaining)

	wrapped := manager.wrapStreamResult(context.Background(), auth, "codex", "gpt-5-codex", nil, nil, remaining, OAuthModelAliasResult{})
	chunk, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped stream closed before payload")
	}
	if chunk.Err != nil || !bytes.Equal(chunk.Payload, payload) {
		t.Fatalf("wrapped chunk = %#v, want original payload without transport error", chunk)
	}
	for range wrapped.Chunks {
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("updated auth is missing")
	}
	if updated.Failed != 1 || updated.Success != 0 {
		t.Fatalf("auth results = success:%d failed:%d, want success:0 failed:1", updated.Success, updated.Failed)
	}
}
