package executor

import (
	"context"
	"net/http"
	"strings"

	zaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ZAIExecutor is a stateless executor for Z.AI / ZCode (GLM) coding plans.
//
// The coding-plan endpoint speaks the Anthropic Messages protocol, so this
// executor reuses ClaudeExecutor verbatim: it only points the base URL at the
// Z.AI endpoint and lets ClaudeExecutor handle request/response translation from
// any source format (openai, gemini, claude). The minted token is read from
// auth metadata by claudeCreds and sent as an Authorization: Bearer header
// (the host is not api.anthropic.com, so the x-api-key path is never taken).
type ZAIExecutor struct {
	ClaudeExecutor
	cfg *config.Config
}

// NewZAIExecutor creates a new Z.AI executor. Both the embedded ClaudeExecutor
// and the outer struct receive cfg so delegated calls have full configuration.
func NewZAIExecutor(cfg *config.Config) *ZAIExecutor {
	return &ZAIExecutor{ClaudeExecutor: ClaudeExecutor{cfg: cfg, providerKey: "zai"}, cfg: cfg}
}

// Identifier returns the executor identifier used to route auths with type "zai".
func (e *ZAIExecutor) Identifier() string { return "zai" }

// cloneAuthWithBaseURL returns a shallow copy of auth with a deep-copied
// Attributes map carrying the Z.AI coding-plan base URL.
//
// The Auth object is shared across concurrent requests and its Attributes map is
// documented as immutable configuration, so it must never be mutated in place:
// an in-place write races with concurrent readers and triggers a fatal
// "concurrent map read and map write" panic. A base URL already present on the
// auth (or in metadata) takes precedence so deployments can override the endpoint.
func (e *ZAIExecutor) cloneAuthWithBaseURL(auth *cliproxyauth.Auth) *cliproxyauth.Auth {
	if auth == nil {
		return nil
	}
	cloned := *auth
	cloned.Attributes = make(map[string]string, len(auth.Attributes)+2)
	for k, v := range auth.Attributes {
		cloned.Attributes[k] = v
	}

	// Point at the Z.AI coding-plan endpoint unless an override is already set.
	if strings.TrimSpace(cloned.Attributes["base_url"]) == "" {
		base := zaiauth.ZAIAPIBaseURL
		if auth.Metadata != nil {
			if v, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(v) != "" {
				base = strings.TrimSpace(v)
			}
		}
		cloned.Attributes["base_url"] = base
	}

	// The coding-plan endpoint authenticates the token via the x-api-key header
	// (like the official ZCode client) and answers Authorization: Bearer requests
	// with a bot/captcha challenge (HTTP 403, {"code":3007,"msg":"captcha verify
	// failed"}). Force x-api-key so ClaudeExecutor sends the token as expected.
	if strings.TrimSpace(cloned.Attributes["anthropic_auth_scheme"]) == "" {
		cloned.Attributes["anthropic_auth_scheme"] = "x-api-key"
	}

	// Z.AI / BigModel are not Anthropic, so Claude "cloak mode" (Claude Code
	// system-prompt injection, fake user IDs, sensitive-word obfuscation) must not
	// run: it would silently rewrite GLM prompts. ClaudeExecutor defaults to "auto"
	// and cloaks requests from non-Claude clients, so force it off here unless an
	// operator explicitly configured a mode on the credential.
	if strings.TrimSpace(cloned.Attributes["cloak_mode"]) == "" {
		cloned.Attributes["cloak_mode"] = "never"
	}

	return &cloned
}

// Execute performs a non-streaming request against the Z.AI coding-plan endpoint.
func (e *ZAIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.ClaudeExecutor.Execute(ctx, e.cloneAuthWithBaseURL(auth), req, opts)
}

// ExecuteStream performs a streaming request against the Z.AI coding-plan endpoint.
func (e *ZAIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return e.ClaudeExecutor.ExecuteStream(ctx, e.cloneAuthWithBaseURL(auth), req, opts)
}

// CountTokens proxies token counting to the Z.AI coding-plan endpoint.
func (e *ZAIExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.ClaudeExecutor.CountTokens(ctx, e.cloneAuthWithBaseURL(auth), req, opts)
}

// PrepareRequest injects Z.AI credentials into a raw HTTP request. It overrides
// the method promoted from the embedded ClaudeExecutor so requests built through
// the public Manager helpers (PrepareHttpRequest / NewHttpRequest / HttpRequest)
// also pass through cloneAuthWithBaseURL - applying the Z.AI base URL, the
// x-api-key scheme and the no-cloak override instead of Claude's default Bearer
// behavior against api.anthropic.com.
func (e *ZAIExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	return e.ClaudeExecutor.PrepareRequest(req, e.cloneAuthWithBaseURL(auth))
}

// HttpRequest injects Z.AI credentials and executes the request. It overrides the
// promoted ClaudeExecutor method for the same reason as PrepareRequest.
func (e *ZAIExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	return e.ClaudeExecutor.HttpRequest(ctx, e.cloneAuthWithBaseURL(auth), req)
}

// Refresh is a no-op: the Z.AI coding-plan token is long-lived and the OAuth
// flow does not return a refresh token. Re-login is required if it is revoked.
func (e *ZAIExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}
