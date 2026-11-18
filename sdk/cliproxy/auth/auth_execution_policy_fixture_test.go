package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type authUsePolicyFunc func(context.Context, AuthUse) error

func (f authUsePolicyFunc) Check(ctx context.Context, use AuthUse) error { return f(ctx, use) }

type authUseContextKey struct{}

type authUseResultHook struct {
	NoopHook
	results atomic.Int64
}

func (h *authUseResultHook) OnResult(context.Context, Result) { h.results.Add(1) }

type authUseExecutor struct {
	provider string
	mu       sync.Mutex
	inputs   []*Auth
	kinds    []AuthUseKind
	prepares int
	prepare  bool
	onShould func()
	once     sync.Once
	prepared func(*Auth) *Auth
}

func (e *authUseExecutor) Identifier() string { return e.provider }
func (e *authUseExecutor) record(kind AuthUseKind, auth *Auth) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.kinds = append(e.kinds, kind)
	e.inputs = append(e.inputs, auth.Clone())
}
func (e *authUseExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(AuthUseExecute, auth)
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}
func (e *authUseExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(AuthUseCount, auth)
	return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
}
func (e *authUseExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(AuthUseStream, auth)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"ok\":true}\n\n")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}
func (e *authUseExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, errors.New("unexpected fixture refresh")
}
func (e *authUseExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected fixture HTTP call")
}
func (e *authUseExecutor) ShouldPrepareRequestAuth(*Auth) bool {
	if e.onShould != nil {
		e.once.Do(e.onShould)
	}
	return e.prepare
}
func (e *authUseExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.prepares++
	e.mu.Unlock()
	if e.prepared != nil {
		return e.prepared(auth.Clone()), nil
	}
	return auth.Clone(), nil
}
func (e *authUseExecutor) observed() (int, []*Auth) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.prepares, append([]*Auth(nil), e.inputs...)
}

type authUseFixture struct {
	manager *Manager
	exec    *authUseExecutor
	hook    *authUseResultHook
	auth    *Auth
	model   string
	ctx     context.Context
}

func newAuthUseFixture(t *testing.T) *authUseFixture {
	t.Helper()
	hook := &authUseResultHook{}
	exec := &authUseExecutor{provider: "auth-use-fixture"}
	m := NewManager(nil, nil, hook)
	m.RegisterExecutor(exec)
	m.SetRetryConfig(2, 0, 3)
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), authUseContextKey{}, "fixture-context"), 5*time.Second)
	t.Cleanup(cancel)
	a := &Auth{ID: "auth-use-" + strings.ReplaceAll(t.Name(), "/", "-"), Provider: exec.provider, Status: StatusActive,
		Attributes: map[string]string{"source": "fixture"},
		Metadata:   map[string]any{"access_token": "synthetic-a", "nested": map[string]any{"value": "original"}},
	}
	registered, err := m.Register(ctx, a)
	if err != nil {
		t.Fatal("fixture registration failed")
	}
	model := "auth-use-model"
	registry.GetGlobalRegistry().RegisterClient(a.ID, exec.provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	return &authUseFixture{manager: m, exec: exec, hook: hook, auth: registered, model: model, ctx: ctx}
}

func (f *authUseFixture) replace(t *testing.T, token string) {
	t.Helper()
	_, changed, err := f.manager.Modify(f.ctx, f.auth.ID, 0, func(a *Auth) error {
		a.Metadata["access_token"] = token
		return nil
	})
	if err != nil || !changed {
		t.Fatal("fixture replacement failed")
	}
}

func (f *authUseFixture) invoke(kind AuthUseKind, opts cliproxyexecutor.Options) error {
	opts.EnsureMetadata()
	opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey] = "public-fixture-model"
	req := cliproxyexecutor.Request{Model: f.model, Payload: []byte(`{"input":"fixture"}`)}
	switch kind {
	case AuthUseExecute:
		_, err := f.manager.Execute(f.ctx, []string{f.exec.provider}, req, opts)
		return err
	case AuthUseCount:
		_, err := f.manager.ExecuteCount(f.ctx, []string{f.exec.provider}, req, opts)
		return err
	case AuthUseStream:
		opts.Stream = true
		result, err := f.manager.ExecuteStream(f.ctx, []string{f.exec.provider}, req, opts)
		if err != nil {
			return err
		}
		if result == nil || result.Chunks == nil {
			return errors.New("fixture stream missing")
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
		}
		return nil
	}
	return errors.New("fixture operation missing")
}

func requireAuthUseRefusal(t *testing.T, err error) {
	t.Helper()
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusServiceUnavailable ||
		err.Error() != "selected account execution is unavailable" {
		t.Fatal("expected fixed policy refusal")
	}
}

func (f *authUseFixture) requireNoHealthResult(t *testing.T) {
	t.Helper()
	a, ok := f.manager.GetByID(f.auth.ID)
	if !ok || a.LastError != nil || a.Unavailable || !a.NextRetryAfter.IsZero() || a.Failed != 0 || f.hook.results.Load() != 0 {
		t.Fatal("policy refusal changed credential health")
	}
}

var authUsePaths = []AuthUseKind{AuthUseExecute, AuthUseStream, AuthUseCount}
