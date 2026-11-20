package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// issue7730Executor records which credentials reached Execute/ExecuteStream/CountTokens and
// returns a fixed success payload so a request only fails when no credential is executable.
type issue7730Executor struct {
	mu           sync.Mutex
	executed     []string
	streamed     []string
	counted      []string
	seenMetadata []string
}

func (*issue7730Executor) Identifier() string { return "issue7730" }

func (e *issue7730Executor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executed = append(e.executed, auth.ID)
	if selected, ok := opts.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string); ok {
		e.seenMetadata = append(e.seenMetadata, selected)
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *issue7730Executor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.streamed = append(e.streamed, auth.ID)
	if selected, ok := opts.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string); ok {
		e.seenMetadata = append(e.seenMetadata, selected)
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"ok":true}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*issue7730Executor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (e *issue7730Executor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.counted = append(e.counted, auth.ID)
	if selected, ok := opts.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string); ok {
		e.seenMetadata = append(e.seenMetadata, selected)
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (*issue7730Executor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *issue7730Executor) snapshot() (executed, streamed, counted, metadata []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.executed...), append([]string(nil), e.streamed...), append([]string(nil), e.counted...), append([]string(nil), e.seenMetadata...)
}

// issue7730Manager builds a manager whose "skipped" credential is pickable for the route model
// (registered in the model registry) but resolves zero executable models because its dispatched
// upstream model is disabled in ModelStates. The "healthy" credential serves the route model.
func issue7730Manager(t *testing.T) (*Manager, *issue7730Executor) {
	t.Helper()
	executor := &issue7730Executor{}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("issue7730-aaa", "issue7730", []*registry.ModelInfo{{ID: "route-model"}})
	reg.RegisterClient("issue7730-zzz", "issue7730", []*registry.ModelInfo{{ID: "route-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient("issue7730-aaa")
		reg.UnregisterClient("issue7730-zzz")
	})
	// The skipped credential advertises the route model but its dispatched upstream model is
	// disabled, so preparedExecutionModelsWithAliasForRequest resolves zero executable models.
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "issue7730-aaa",
		Provider: "issue7730",
		Status:   StatusActive,
		Attributes: map[string]string{
			homeUpstreamModelAttributeKey: "disabled-upstream",
		},
		ModelStates: map[string]*ModelState{
			"disabled-upstream": {Status: StatusDisabled},
		},
	}); errRegister != nil {
		t.Fatalf("register skipped auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "issue7730-zzz",
		Provider: "issue7730",
		Status:   StatusActive,
	}); errRegister != nil {
		t.Fatalf("register healthy auth: %v", errRegister)
	}
	return manager, executor
}

func issue7730SelectionRecorder() (map[string]any, *[]string) {
	selected := &[]string{}
	meta := map[string]any{
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
			*selected = append(*selected, authID)
		},
	}
	return meta, selected
}

// TestIssue7730ExecuteDoesNotPublishSkippedAuth ensures executeMixedOnce only publishes the
// selected-auth metadata and callback for a credential that actually enters execution. The
// skipped credential resolves zero executable models and must remain invisible to
// SelectedAuthCallback observers.
func TestIssue7730ExecuteDoesNotPublishSkippedAuth(t *testing.T) {
	manager, executor := issue7730Manager(t)
	meta, selected := issue7730SelectionRecorder()

	_, errExecute := manager.Execute(context.Background(), []string{"issue7730"}, cliproxyexecutor.Request{Model: "route-model"}, cliproxyexecutor.Options{Metadata: meta})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := *selected; len(got) != 1 || got[0] != "issue7730-zzz" {
		t.Fatalf("SelectedAuthCallback notifications = %v, want only [issue7730-zzz]", got)
	}
	executed, _, _, seen := executor.snapshot()
	if len(executed) != 1 || executed[0] != "issue7730-zzz" {
		t.Fatalf("Execute() calls = %v, want only [issue7730-zzz]", executed)
	}
	if len(seen) != 1 || seen[0] != "issue7730-zzz" {
		t.Fatalf("SelectedAuthMetadataKey observed by executor = %v, want only [issue7730-zzz]", seen)
	}
}

// TestIssue7730CountTokensDoesNotPublishSkippedAuth mirrors the execute path for
// executeCountMixedOnce.
func TestIssue7730CountTokensDoesNotPublishSkippedAuth(t *testing.T) {
	manager, executor := issue7730Manager(t)
	meta, selected := issue7730SelectionRecorder()

	_, errCount := manager.ExecuteCount(context.Background(), []string{"issue7730"}, cliproxyexecutor.Request{Model: "route-model"}, cliproxyexecutor.Options{Metadata: meta})
	if errCount != nil {
		t.Fatalf("ExecuteCount() error = %v", errCount)
	}
	if got := *selected; len(got) != 1 || got[0] != "issue7730-zzz" {
		t.Fatalf("SelectedAuthCallback notifications = %v, want only [issue7730-zzz]", got)
	}
	_, _, counted, seen := executor.snapshot()
	if len(counted) != 1 || counted[0] != "issue7730-zzz" {
		t.Fatalf("CountTokens() calls = %v, want only [issue7730-zzz]", counted)
	}
	if len(seen) != 1 || seen[0] != "issue7730-zzz" {
		t.Fatalf("SelectedAuthMetadataKey observed by executor = %v, want only [issue7730-zzz]", seen)
	}
}

// TestIssue7730ExecuteStreamDoesNotPublishSkippedAuth mirrors the execute path for the
// streaming mixed-conductor loop, which repeats the same publish-then-check ordering.
func TestIssue7730ExecuteStreamDoesNotPublishSkippedAuth(t *testing.T) {
	manager, executor := issue7730Manager(t)
	meta, selected := issue7730SelectionRecorder()

	stream, errStream := manager.ExecuteStream(context.Background(), []string{"issue7730"}, cliproxyexecutor.Request{Model: "route-model"}, cliproxyexecutor.Options{Metadata: meta})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for range stream.Chunks {
	}
	if got := *selected; len(got) != 1 || got[0] != "issue7730-zzz" {
		t.Fatalf("SelectedAuthCallback notifications = %v, want only [issue7730-zzz]", got)
	}
	_, streamed, _, seen := executor.snapshot()
	if len(streamed) != 1 || streamed[0] != "issue7730-zzz" {
		t.Fatalf("ExecuteStream() calls = %v, want only [issue7730-zzz]", streamed)
	}
	if len(seen) != 1 || seen[0] != "issue7730-zzz" {
		t.Fatalf("SelectedAuthMetadataKey observed by executor = %v, want only [issue7730-zzz]", seen)
	}
}
