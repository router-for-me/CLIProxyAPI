package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	clipoauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// PoolsideBaseURL is the Poolside inference host WITHOUT the /v1 suffix:
// ClaudeExecutor appends "/v1/messages" itself, so a base carrying /v1 would
// produce https://inference.poolside.ai/v1/v1/messages (upstream 404 — the
// runtime failure observed with the released -dc9/-dc10 binaries). The full
// service surface is https://inference.poolside.ai/v1/*.
const PoolsideBaseURL = "https://inference.poolside.ai"

// PoolsideExecutor is a stateless executor for Poolside AI's OpenAI-compatible
// inference endpoint. It reuses ClaudeExecutor for protocol translation (the
// gateway speaks the Anthropic Messages protocol as its native wire format) and
// points the base URL at Poolside, sending the API key as a Bearer token.
type PoolsideExecutor struct {
	ClaudeExecutor
	cfg *config.Config
}

// NewPoolsideExecutor creates a new Poolside executor.
func NewPoolsideExecutor(cfg *config.Config) *PoolsideExecutor {
	return &PoolsideExecutor{
		ClaudeExecutor: ClaudeExecutor{
			cfg:                     cfg,
			requestLogProvider:      constant.Poolside,
			upstreamModelNormalizer: normalizePoolsideUpstreamModel,
		},
		cfg: cfg,
	}
}

// Identifier returns the provider identifier used to route auths with type "poolside".
func (e *PoolsideExecutor) Identifier() string { return constant.Poolside }

// ProviderKey overrides the provider identity used for usage attribution.
func (e *PoolsideExecutor) ProviderKey() string { return constant.Poolside }

// RequestToFormat reports the upstream protocol (Claude Messages) used by Poolside.
func (e *PoolsideExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatClaude
}

// cloneAuthWithBaseURL returns a shallow copy of auth carrying the Poolside base URL.
func (e *PoolsideExecutor) cloneAuthWithBaseURL(auth *clipoauth.Auth) *clipoauth.Auth {
	if auth == nil {
		return nil
	}
	cloned := *auth
	cloned.Attributes = make(map[string]string, len(auth.Attributes)+1)
	for k, v := range auth.Attributes {
		cloned.Attributes[k] = v
	}
	if strings.TrimSpace(cloned.Attributes["base_url"]) == "" {
		cloned.Attributes["base_url"] = PoolsideBaseURL
	}
	return &cloned
}

// PrepareRequest injects Poolside credentials into the outgoing HTTP request.
func (e *PoolsideExecutor) PrepareRequest(req *http.Request, auth *clipoauth.Auth) error {
	if req == nil {
		return nil
	}
	token, ok := poolsideCreds(auth)
	if ok && strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return nil
}

// HttpRequest injects credentials and executes the request.
func (e *PoolsideExecutor) HttpRequest(ctx context.Context, auth *clipoauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errPoolsideRequestNil
	}
	if ctx == nil {
		ctx = req.Context()
	}
	clonedAuth := e.cloneAuthWithBaseURL(auth)
	if err := e.PrepareRequest(req.WithContext(ctx), clonedAuth); err != nil {
		return nil, err
	}
	return e.ClaudeExecutor.HttpRequest(ctx, clonedAuth, req)
}

// Execute performs a non-streaming request against the Poolside endpoint.
func (e *PoolsideExecutor) Execute(ctx context.Context, auth *clipoauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	return e.ClaudeExecutor.Execute(ctx, e.cloneAuthWithBaseURL(auth), req, opts)
}

// ExecuteStream performs a streaming request against the Poolside endpoint.
func (e *PoolsideExecutor) ExecuteStream(ctx context.Context, auth *clipoauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return e.ClaudeExecutor.ExecuteStream(ctx, e.cloneAuthWithBaseURL(auth), req, opts)
}

// CountTokens delegates to the Claude endpoint count_tokens path.
func (e *PoolsideExecutor) CountTokens(ctx context.Context, auth *clipoauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.ClaudeExecutor.CountTokens(ctx, e.cloneAuthWithBaseURL(auth), req, opts)
}

// Refresh is a no-op: Poolside uses static API keys.
func (e *PoolsideExecutor) Refresh(_ context.Context, auth *clipoauth.Auth) (*clipoauth.Auth, error) {
	return auth, nil
}

// poolsideCreds extracts the API key from auth.
func poolsideCreds(auth *clipoauth.Auth) (string, bool) {
	if auth == nil {
		return "", false
	}
	if token := strings.TrimSpace(auth.Attributes["api_key"]); token != "" {
		return token, true
	}
	if auth.Metadata == nil {
		return "", false
	}
	if v, ok := auth.Metadata["api_key"]; ok {
		if token, ok := v.(string); ok && strings.TrimSpace(token) != "" {
			return token, true
		}
	}
	return "", false
}

func normalizePoolsideUpstreamModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

var errPoolsideRequestNil = statusErr{code: http.StatusInternalServerError, msg: "opencode executor: request is nil"}
