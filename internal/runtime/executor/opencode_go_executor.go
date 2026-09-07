package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// OpenCodeGoExecutor routes opencode-go traffic to the right wire format per
// model: models whose config carries wire_format "anthropic" execute through
// the shared Claude executor path (/v1/messages, x-api-key); everything else
// (the default, including unknown models) goes through the OpenAI-compat
// path (/chat/completions, Bearer). Both delegate executors read
// base_url/api_key from the auth's attributes, so no credential handling
// lives here. Composition follows the XAIAutoExecutor pattern.
type OpenCodeGoExecutor struct {
	openaiExec *OpenAICompatExecutor
	claudeExec *ClaudeExecutor
	cfg        *config.Config
}

// NewOpenCodeGoExecutor builds the dispatching executor for the
// "opencode-go" channel.
func NewOpenCodeGoExecutor(cfg *config.Config) *OpenCodeGoExecutor {
	return &OpenCodeGoExecutor{
		openaiExec: NewOpenAICompatExecutor("opencode-go", cfg),
		claudeExec: NewClaudeExecutor(cfg),
		cfg:        cfg,
	}
}

func (e *OpenCodeGoExecutor) Identifier() string { return "opencode-go" }

// wireFormat resolves the upstream protocol for a model: "anthropic" only
// when a matching configured model (by alias or name, after stripping any
// thinking/effort suffix) says so; anything else falls back to the openai
// path (the safe majority default).
func (e *OpenCodeGoExecutor) wireFormat(model string) string {
	base := thinking.ParseSuffix(model).ModelName
	for i := range e.cfg.OpenCodeGo {
		for _, m := range e.cfg.OpenCodeGo[i].Models {
			if m.Alias == base || m.Name == base {
				return m.WireFormat
			}
		}
	}
	return ""
}

func (e *OpenCodeGoExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.wireFormat(req.Model) == "anthropic" {
		return e.claudeExec.Execute(ctx, auth, req, opts)
	}
	return e.openaiExec.Execute(ctx, auth, req, opts)
}

func (e *OpenCodeGoExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.wireFormat(req.Model) == "anthropic" {
		return e.claudeExec.ExecuteStream(ctx, auth, req, opts)
	}
	return e.openaiExec.ExecuteStream(ctx, auth, req, opts)
}

func (e *OpenCodeGoExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.wireFormat(req.Model) == "anthropic" {
		return e.claudeExec.CountTokens(ctx, auth, req, opts)
	}
	return e.openaiExec.CountTokens(ctx, auth, req, opts)
}

// Refresh delegates to the Claude executor's refresh, which is a no-op for
// API-key auths but keeps the interface honest for OAuth-shaped rows.
func (e *OpenCodeGoExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return e.claudeExec.Refresh(ctx, auth)
}

// HttpRequest injects credentials based on the request path: anthropic
// upstreams get x-api-key, everything else Bearer. Delegated to the Claude
// executor when the path targets /v1/messages, else to the compat executor.
func (e *OpenCodeGoExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/messages") {
		return e.claudeExec.HttpRequest(ctx, auth, req)
	}
	return e.openaiExec.HttpRequest(ctx, auth, req)
}
