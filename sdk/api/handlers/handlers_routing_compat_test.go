package handlers_test

import (
	"context"
	"testing"

	basehandlers "github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type spyExec struct {
	id     string
	called *bool
}

func (s spyExec) Identifier() string { return s.id }
func (s spyExec) Execute(ctx context.Context, a *coreauth.Auth, req cliproxyexec.Request, opts cliproxyexec.Options) (cliproxyexec.Response, error) {
	if s.called != nil {
		*s.called = true
	}
	return cliproxyexec.Response{Payload: []byte(`{"ok":true}`)}, nil
}
func (s spyExec) ExecuteStream(ctx context.Context, a *coreauth.Auth, req cliproxyexec.Request, opts cliproxyexec.Options) (<-chan cliproxyexec.StreamChunk, error) {
	if s.called != nil {
		*s.called = true
	}
	ch := make(chan cliproxyexec.StreamChunk)
	close(ch)
	return ch, nil
}
func (s spyExec) Refresh(ctx context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
func (s spyExec) CountTokens(ctx context.Context, a *coreauth.Auth, req cliproxyexec.Request, opts cliproxyexec.Options) (cliproxyexec.Response, error) {
	if s.called != nil {
		*s.called = true
	}
	return cliproxyexec.Response{Payload: []byte(`{"count":1}`)}, nil
}

func newBaseHandlerWithManager(m *coreauth.Manager) *basehandlers.BaseAPIHandler {
	cfg := &sdkconfig.SDKConfig{}
	h := basehandlers.NewBaseAPIHandlers(cfg, m)
	return h
}

func TestRouting_GLM_UsesZhipuExecutor(t *testing.T) {
	m := coreauth.NewManager(nil, nil, coreauth.NoopHook{})
	zCalled := false
	cCalled := false
	m.RegisterExecutor(spyExec{id: "zhipu", called: &zCalled})
	m.RegisterExecutor(spyExec{id: "claude", called: &cCalled})
	ctx := context.Background()
	// Register one zhipu auth
	_, _ = m.Register(ctx, &coreauth.Auth{ID: "z1", Provider: "zhipu", Attributes: map[string]string{"api_key": "k", "base_url": "https://open.bigmodel.cn/api/anthropic"}})

	h := newBaseHandlerWithManager(m)
	// Call ExecuteWithAuthManager via BaseAPIHandler directly
	_, err := h.ExecuteWithAuthManager(ctx, "claude", "glm-4.6", []byte(`{"messages":[]}`), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !zCalled {
		t.Fatalf("expected zhipu executor to be called")
	}
	if cCalled {
		t.Fatalf("did not expect claude executor to be called")
	}
}

func TestRouting_MiniMax_UsesMinimaxExecutor(t *testing.T) {
	m := coreauth.NewManager(nil, nil, coreauth.NoopHook{})
	mmCalled := false
	cCalled := false
	m.RegisterExecutor(spyExec{id: "minimax", called: &mmCalled})
	m.RegisterExecutor(spyExec{id: "claude", called: &cCalled})
	ctx := context.Background()
	// Register one minimax auth
	_, _ = m.Register(ctx, &coreauth.Auth{ID: "m1", Provider: "minimax", Attributes: map[string]string{"api_key": "k", "base_url": "https://api.minimaxi.com/anthropic"}})

	h := newBaseHandlerWithManager(m)
	_, err := h.ExecuteWithAuthManager(ctx, "claude", "MiniMax-M2", []byte(`{"messages":[]}`), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mmCalled {
		t.Fatalf("expected minimax executor to be called")
	}
	if cCalled {
		t.Fatalf("did not expect claude executor to be called")
	}
}
