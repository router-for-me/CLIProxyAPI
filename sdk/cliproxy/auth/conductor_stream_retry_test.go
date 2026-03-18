package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// authAwareStreamExecutor is a test executor that returns different results per auth ID.
type authAwareStreamExecutor struct {
	id string

	mu              sync.Mutex
	streamAuthIDs   []string
	streamErrors    map[string]error    // keyed by auth.ID
	streamPayloads  map[string][]byte   // keyed by auth.ID
	emptyStreamAuth map[string]struct{} // auth IDs that return empty (closed) stream
}

func (e *authAwareStreamExecutor) Identifier() string { return e.id }

func (e *authAwareStreamExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *authAwareStreamExecutor) ExecuteStream(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamAuthIDs = append(e.streamAuthIDs, auth.ID)
	streamErr := e.streamErrors[auth.ID]
	payload := e.streamPayloads[auth.ID]
	_, isEmpty := e.emptyStreamAuth[auth.ID]
	e.mu.Unlock()

	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if streamErr != nil {
		ch <- cliproxyexecutor.StreamChunk{Err: streamErr}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
	}
	if isEmpty {
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
	}
	if payload == nil {
		payload = []byte(auth.ID)
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: payload}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
}

func (e *authAwareStreamExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *authAwareStreamExecutor) CountTokens(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *authAwareStreamExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *authAwareStreamExecutor) StreamAuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamAuthIDs))
	copy(out, e.streamAuthIDs)
	return out
}

func newMultiAuthTestManager(t *testing.T, model string, authIDs []string, executor *authAwareStreamExecutor) *Manager {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	for _, id := range authIDs {
		auth := &Auth{
			ID:       id,
			Provider: executor.id,
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "key-" + id,
				"provider_key": executor.id,
			},
		}
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %s: %v", id, err)
		}
		reg.RegisterClient(id, executor.id, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, id := range authIDs {
			reg.UnregisterClient(id)
		}
	})
	return m
}

func TestExecuteStream_RotatesAuthOnBootstrapError(t *testing.T) {
	t.Parallel()
	model := "test-model"
	executor := &authAwareStreamExecutor{
		id: "testprov",
		streamErrors: map[string]error{
			"auth-1": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"},
		},
		streamPayloads: map[string][]byte{
			"auth-2": []byte("ok-from-auth-2"),
		},
	}
	m := newMultiAuthTestManager(t, model, []string{"auth-1", "auth-2"}, executor)

	streamResult, err := m.ExecuteStream(context.Background(), []string{"testprov"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "ok-from-auth-2" {
		t.Fatalf("payload = %q, want %q", string(payload), "ok-from-auth-2")
	}
	// Verify both auths were attempted (auth-1 failed, auth-2 succeeded).
	got := executor.StreamAuthIDs()
	if len(got) < 2 {
		t.Fatalf("stream auth IDs = %v, want at least 2 attempts", got)
	}
}

func TestExecuteStream_RotatesAuthOnEmptyStream(t *testing.T) {
	t.Parallel()
	model := "test-model"
	executor := &authAwareStreamExecutor{
		id:              "testprov",
		emptyStreamAuth: map[string]struct{}{"auth-1": {}},
		streamPayloads: map[string][]byte{
			"auth-2": []byte("ok-from-auth-2"),
		},
	}
	m := newMultiAuthTestManager(t, model, []string{"auth-1", "auth-2"}, executor)

	streamResult, err := m.ExecuteStream(context.Background(), []string{"testprov"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "ok-from-auth-2" {
		t.Fatalf("payload = %q, want %q", string(payload), "ok-from-auth-2")
	}
	got := executor.StreamAuthIDs()
	if len(got) < 2 {
		t.Fatalf("stream auth IDs = %v, want at least 2 attempts", got)
	}
}

func TestExecuteStream_AllAuthsFailReturnsError(t *testing.T) {
	t.Parallel()
	model := "test-model"
	executor := &authAwareStreamExecutor{
		id: "testprov",
		streamErrors: map[string]error{
			"auth-1": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"},
			"auth-2": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"},
		},
	}
	m := newMultiAuthTestManager(t, model, []string{"auth-1", "auth-2"}, executor)

	_, err := m.ExecuteStream(context.Background(), []string{"testprov"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected error when all auths fail, got nil")
	}
	got := executor.StreamAuthIDs()
	if len(got) < 2 {
		t.Fatalf("stream auth IDs = %v, want at least 2 attempts (both auths tried)", got)
	}
}
