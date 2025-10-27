package executor

import (
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// GlmAnthropicExecutor is a thin wrapper that delegates to ClaudeExecutor
// for Anthropic-compatible Zhipu endpoints while exposing provider identifier
// as "zhipu" for routing consistency.
type GlmAnthropicExecutor struct {
	cfg *config.Config
}

func NewGlmAnthropicExecutor(cfg *config.Config) *GlmAnthropicExecutor {
	return &GlmAnthropicExecutor{cfg: cfg}
}

func (e *GlmAnthropicExecutor) Identifier() string { return "zhipu" }

func (e *GlmAnthropicExecutor) PrepareRequest(r *http.Request, a *cliproxyauth.Auth) error {
	return nil
}

func (e *GlmAnthropicExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return NewClaudeExecutor(e.cfg).Execute(ctx, auth, req, opts)
}

func (e *GlmAnthropicExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	return NewClaudeExecutor(e.cfg).ExecuteStream(ctx, auth, req, opts)
}

func (e *GlmAnthropicExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return NewClaudeExecutor(e.cfg).CountTokens(ctx, auth, req, opts)
}

func (e *GlmAnthropicExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return NewClaudeExecutor(e.cfg).Refresh(ctx, auth)
}
