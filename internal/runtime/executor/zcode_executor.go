package executor

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// zcodeDefaultAnthropicEndpoint is the plain ZCode Anthropic-compatible endpoint.
// The routing helper maps exactly this base to the ultra gateway when a snapshot
// is available; any other pinned base (a custom gateway or a test server) is
// honored verbatim.
const zcodeDefaultAnthropicEndpoint = "https://api.z.ai/api/anthropic"

// zcodeRouter resolves the coding-plan base URL to the ultra gateway. It is an
// interface so tests can inject a stub whose Refresh is a no-op, keeping unit
// tests hermetic (no network).
type zcodeRouter interface {
	Refresh(ctx context.Context) error
	BaseURL(auth *cliproxyauth.Auth, path string) string
}

// ZCodeExecutor forwards Anthropic-format requests to the ZCode coding API,
// presenting the native ZCode client identity headers (stored as header:*
// attributes at login). It delegates the wire protocol to ClaudeExecutor, which
// derives the request URL from auth.Attributes["base_url"] and applies every
// custom header it finds there on both streaming and non-streaming paths.
type ZCodeExecutor struct {
	*ClaudeExecutor
	routes zcodeRouter
}

// NewZCodeExecutor constructs the executor.
func NewZCodeExecutor(cfg *config.Config) *ZCodeExecutor {
	return &ZCodeExecutor{
		ClaudeExecutor: &ClaudeExecutor{
			cfg:                cfg,
			requestLogProvider: "zcode",
		},
		routes: helps.NewZCodeRouteResolver(cfg),
	}
}

// Identifier returns the executor identifier.
func (e *ZCodeExecutor) Identifier() string { return "zcode" }

// RequestToFormat reports the upstream wire format (Anthropic).
func (e *ZCodeExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatClaude
}

// Execute resolves the routed base URL, refreshes the routing snapshot, then
// delegates to ClaudeExecutor with a per-request clone of the auth whose
// attributes carry the resolved base_url and the preserved api_key.
func (e *ZCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	authClone := e.routedAuth(ctx, auth, opts)
	return e.ClaudeExecutor.Execute(ctx, authClone, req, opts)
}

// ExecuteStream mirrors Execute on the streaming path: it resolves the routed
// base URL and delegates to ClaudeExecutor's streaming implementation so the
// ultra-gateway routing and identity headers apply to streaming requests too —
// without this override, method promotion would call ClaudeExecutor.ExecuteStream
// with the raw, un-routed auth and bypass routing entirely.
func (e *ZCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	authClone := e.routedAuth(ctx, auth, opts)
	return e.ClaudeExecutor.ExecuteStream(ctx, authClone, req, opts)
}

// routedAuth refreshes the routing snapshot (fail-open), resolves the routed
// base URL for the request, and returns a per-request clone of the auth carrying
// the resolved base_url and api_key. ClaudeExecutor reads base_url/api_key from
// the passed auth's Attributes and applies the header:* attrs, so a shared helper
// drives both the stream and non-stream paths with identical routing.
func (e *ZCodeExecutor) routedAuth(ctx context.Context, auth *cliproxyauth.Auth, opts cliproxyexecutor.Options) *cliproxyauth.Auth {
	if err := e.routes.Refresh(ctx); err != nil {
		// Fail-open: the requested route stays on the pinned/fallback endpoint.
		log.WithError(err).Debug("zcode executor: route refresh failed, keeping pinned/fallback endpoint")
	}
	apiKey, base := zcodeCreds(auth)
	if base == "" || base == zcodeDefaultAnthropicEndpoint {
		// The routing helper only rewrites the canonical default endpoint to the
		// ultra gateway. A pinned non-default base (custom gateway, test server)
		// is the caller's explicit choice and must not be overridden.
		base = e.routes.BaseURL(auth, requestPathMetadata(opts))
		if base == "" {
			base = zcodeDefaultAnthropicEndpoint
		}
	}
	return withBaseURL(auth, base, apiKey)
}

// requestPathMetadata extracts the inbound request path when available; the
// routing helper ignores it, so this is purely informational.
func requestPathMetadata(opts cliproxyexecutor.Options) string {
	if v, ok := opts.Metadata[cliproxyexecutor.RequestPathMetadataKey].(string); ok {
		return v
	}
	return ""
}

// zcodeCreds extracts the API key and base URL from the auth attributes, falling
// back to the metadata for either when the attribute is absent.
func zcodeCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if a.Metadata != nil {
		if apiKey == "" {
			if v, ok := a.Metadata["api_key"].(string); ok {
				apiKey = v
			}
		}
		if baseURL == "" {
			if v, ok := a.Metadata["base_url"].(string); ok {
				baseURL = v
			}
		}
	}
	return apiKey, baseURL
}

// withBaseURL returns a shallow clone of auth whose Attributes is a fresh map
// with base_url (and api_key, when non-empty) applied. The shared credential is
// never mutated: each request carries its own attribute view.
func withBaseURL(auth *cliproxyauth.Auth, base, apiKey string) *cliproxyauth.Auth {
	if auth == nil {
		return nil
	}
	clone := *auth
	attrs := make(map[string]string, len(auth.Attributes)+2)
	for k, v := range auth.Attributes {
		attrs[k] = v
	}
	attrs["base_url"] = base
	if apiKey != "" {
		attrs["api_key"] = apiKey
	}
	clone.Attributes = attrs
	return &clone
}
