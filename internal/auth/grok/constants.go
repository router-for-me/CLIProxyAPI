// Package grok provides authentication and token management for xAI Grok via OAuth.
// Constants are taken verbatim from opencode's xai plugin (packages/opencode/src/plugin/xai.ts)
// since xAI rejects loopback OAuth from non-allowlisted clients; we MUST reuse the shared
// Grok-CLI client_id and the registered redirect_uri.
package grok

import "time"

// OAuth endpoints (xAI).
const (
	AuthorizeURL    = "https://auth.x.ai/oauth2/authorize"
	TokenURL        = "https://auth.x.ai/oauth2/token"
	DeviceAuthURL   = "https://auth.x.ai/oauth2/device/code"
	DeviceCodeGrant = "urn:ietf:params:oauth:grant-type:device_code"

	// ClientID is the shared Grok-CLI public OAuth client. xAI rejects custom
	// client_ids on loopback redirects for non-allowlisted apps, so all
	// SuperGrok-OAuth-enabled CLIs (opencode, grok-cli, cliproxyapi) share this.
	ClientID = "b1a00492-073a-47ea-816f-4c329264a828"

	// RedirectURI is the registered loopback for the Grok-CLI client. The
	// host:port pair is part of the registration with xAI; mismatches are
	// rejected at the authorize step.
	RedirectURI       = "http://127.0.0.1:56121/callback"
	OAuthCallbackHost = "127.0.0.1"
	OAuthCallbackPort = 56121
	OAuthCallbackPath = "/callback"

	// Scope is the OAuth scope set required for Grok API access via SuperGrok.
	Scope = "openid profile email offline_access grok-cli:access api:access"

	// AuthorizeReferrer identifies CLIProxyAPI to xAI's OAuth server. If xAI
	// rejects this value, the documented fallback is to use "opencode" (the
	// original referrer for the shared Grok-CLI client).
	AuthorizeReferrer = "cliproxyapi"

	// AuthorizePlan must be "generic" — without it, accounts.x.ai rejects
	// loopback OAuth from non-allowlisted clients (per opencode plugin
	// source comment).
	AuthorizePlan = "generic"

	// APIBaseURL is the upstream Grok REST endpoint after OAuth (used by the
	// executor, not by this package directly).
	APIBaseURL = "https://api.x.ai/v1"
)

// AccessTokenRefreshSkew is how long before the stored expiry we proactively
// refresh the access token. Matches opencode's ACCESS_TOKEN_REFRESH_SKEW_MS = 120s.
const AccessTokenRefreshSkew = 120 * time.Second

// Device-code polling bounds (RFC 8628). Match opencode defaults.
const (
	DeviceCodeDefaultInterval   = 5 * time.Second
	DeviceCodeMinInterval       = 1 * time.Second
	DeviceCodeSlowDownIncrement = 5 * time.Second
	DeviceCodeDefaultExpires    = 5 * time.Minute
	DeviceCodePollSafetyMargin  = 3 * time.Second
)
