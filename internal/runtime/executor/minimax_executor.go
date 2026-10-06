package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	minimaxauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/minimax"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// minimaxImageHandlerType routes image requests through the shared OpenAI
// image pipeline, which expects a flat OpenAI-shaped response.
const minimaxImageHandlerType = "openai-image"

// MinimaxExecutor serves the MiniMax platform. Text traffic is delegated to the
// shared Claude executor because MiniMax exposes an Anthropic-compatible
// messages endpoint; image traffic is served directly.
type MinimaxExecutor struct {
	ClaudeExecutor
	cfg *config.Config
}

// NewMinimaxExecutor creates a new MiniMax executor.
func NewMinimaxExecutor(cfg *config.Config) *MinimaxExecutor {
	return &MinimaxExecutor{
		ClaudeExecutor: ClaudeExecutor{
			cfg:                cfg,
			requestLogProvider: "minimax",
			oauthToolAliases:   &claudeOAuthToolAliasStore{},
		},
		cfg: cfg,
	}
}

// Identifier returns the executor identifier.
func (e *MinimaxExecutor) Identifier() string { return "minimax" }

// RequestToFormat reports the upstream request format used after auth selection.
func (e *MinimaxExecutor) RequestToFormat(_ cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	// The image pipeline sends a pre-built OpenAI-shaped payload that must not
	// be re-translated, so echo the source format through untouched.
	if opts.SourceFormat == sdktranslator.FromString(minimaxImageHandlerType) {
		return opts.SourceFormat
	}
	return sdktranslator.FormatClaude
}

// minimaxToken extracts the credential used to authenticate against MiniMax.
func minimaxToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if token := strings.TrimSpace(auth.Attributes["api_key"]); token != "" {
			return token
		}
	}
	return strings.TrimSpace(readAuthMetadataString(auth, "access_token"))
}

// injectMinimaxBaseURL points the shared Claude request builder at the
// Anthropic-compatible MiniMax endpoint. File-backed credentials reload with
// Metadata only, so this must be applied on every request.
func injectMinimaxBaseURL(auth *cliproxyauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["base_url"] = minimaxauth.ResolveClaudeUpstreamURL(auth)
	// MiniMax rejects Authorization: Bearer on its Anthropic-compatible endpoint
	// and requires the credential in x-api-key instead.
	auth.Attributes[claudeForceAPIKeyHeaderAttr] = "true"
}

// PrepareRequest injects MiniMax credentials into the outgoing HTTP request.
// MiniMax expects the credential in x-api-key on its Anthropic-compatible
// endpoint, so this deliberately differs from the Claude executor.
func (e *MinimaxExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	if token := minimaxToken(auth); token != "" {
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", token)
	}
	return nil
}

// HttpRequest injects MiniMax credentials into the request and executes it.
func (e *MinimaxExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("minimax executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming completion request against MiniMax.
func (e *MinimaxExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if isMinimaxImageRequest(opts) {
		return e.executeMinimaxImage(ctx, auth, req, opts)
	}
	injectMinimaxBaseURL(auth)
	return e.ClaudeExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream performs a streaming completion request against MiniMax.
func (e *MinimaxExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if isMinimaxImageRequest(opts) {
		return e.executeMinimaxImageStream(ctx, auth, req, opts)
	}
	injectMinimaxBaseURL(auth)
	return e.ClaudeExecutor.ExecuteStream(ctx, auth, req, opts)
}

// CountTokens counts tokens for a request. MiniMax is not Anthropic's
// first-party origin, so the shared implementation uses local estimation.
func (e *MinimaxExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if isMinimaxImageRequest(opts) {
		return cliproxyexecutor.Response{}, statusErr{
			code: http.StatusNotImplemented,
			msg:  "minimax executor: token counting is not supported for image requests",
		}
	}
	injectMinimaxBaseURL(auth)
	return e.ClaudeExecutor.CountTokens(ctx, auth, req, opts)
}
