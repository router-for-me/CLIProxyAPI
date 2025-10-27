package executor

import (
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// MiniMaxExecutor is a thin wrapper that delegates to ClaudeExecutor
// for Anthropic-compatible MiniMax endpoints while exposing provider identifier
// as "minimax" to the core manager and routing layer.
type MiniMaxExecutor struct{ cfg *config.Config }

func NewMiniMaxExecutor(cfg *config.Config) *MiniMaxExecutor { return &MiniMaxExecutor{cfg: cfg} }

func (e *MiniMaxExecutor) Identifier() string { return "minimax" }

func (e *MiniMaxExecutor) PrepareRequest(r *http.Request, a *cliproxyauth.Auth) error {
	// Delegate to ClaudeExecutor (no-op)
	return nil
}

func (e *MiniMaxExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return NewAnthropicCompatExecutor(e.cfg, e.Identifier()).Execute(ctx, auth, req, opts)
}

func (e *MiniMaxExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	return NewAnthropicCompatExecutor(e.cfg, e.Identifier()).ExecuteStream(ctx, auth, req, opts)
}

func (e *MiniMaxExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return NewAnthropicCompatExecutor(e.cfg, e.Identifier()).CountTokens(ctx, auth, req, opts)
}

func (e *MiniMaxExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return NewAnthropicCompatExecutor(e.cfg, e.Identifier()).Refresh(ctx, auth)
}
