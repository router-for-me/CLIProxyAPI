package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// MirasimExecutor is a stateless executor for Mirasim reverse-proxy upstreams
// that speak the Anthropic Messages API with a static Bearer API key.
// It reuses the Claude executor for every request path and inbound source
// format. The Claude header logic selects the auth scheme by upstream URL, so
// a non-Anthropic base URL always sends "Authorization: Bearer <key>" and
// never x-api-key. The only differences from Claude are the provider identity
// and that a base URL is mandatory: unlike Claude there is no default
// api.anthropic.com fallback, because Mirasim relays are third-party
// endpoints chosen by the operator.
type MirasimExecutor struct {
	*ClaudeExecutor
}

// NewMirasimExecutor creates a new Mirasim executor.
func NewMirasimExecutor(cfg *config.Config) *MirasimExecutor {
	return &MirasimExecutor{
		ClaudeExecutor: &ClaudeExecutor{
			cfg:                cfg,
			requestLogProvider: "mirasim",
		},
	}
}

// Identifier returns the executor identifier.
func (e *MirasimExecutor) Identifier() string { return "mirasim" }

// mirasimBaseURL returns the mandatory upstream base URL for a Mirasim credential.
func mirasimBaseURL(auth *cliproxyauth.Auth) (string, error) {
	_, baseURL := claudeCreds(auth)
	if strings.TrimSpace(baseURL) == "" {
		return "", fmt.Errorf("mirasim executor: base_url is required for mirasim credentials")
	}
	return baseURL, nil
}

// Execute performs a non-streaming request to the Mirasim upstream.
func (e *MirasimExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if _, err := mirasimBaseURL(auth); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return e.ClaudeExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream performs a streaming request to the Mirasim upstream.
func (e *MirasimExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if _, err := mirasimBaseURL(auth); err != nil {
		return nil, err
	}
	return e.ClaudeExecutor.ExecuteStream(ctx, auth, req, opts)
}

// CountTokens estimates token count for Mirasim requests. Third-party
// upstreams keep local estimation, matching Claude's custom-base-URL path.
func (e *MirasimExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if _, err := mirasimBaseURL(auth); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return e.ClaudeExecutor.CountTokens(ctx, auth, req, opts)
}
