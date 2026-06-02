package auth

import (
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// Compact mode values stored in Attributes["compact_mode"].
const (
	// CompactModeAuto follows the global compact-default when deciding eligibility.
	CompactModeAuto = "auto"
	// CompactModeForceOn always treats the credential as compact-capable.
	CompactModeForceOn = "force_on"
	// CompactModeForceOff always excludes the credential from compact routing.
	CompactModeForceOff = "force_off"
)

// NormalizeCompactMode maps arbitrary input to a known compact mode, defaulting to auto.
func NormalizeCompactMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case CompactModeForceOn:
		return CompactModeForceOn
	case CompactModeForceOff:
		return CompactModeForceOff
	default:
		return CompactModeAuto
	}
}

// ApplyCompactAttributes resolves the credential compact mode against the global default
// (defaultAllow: true for compact-default=allow, false for deny) and stores both the raw
// mode (compact_mode) and the resolved boolean (compact_allowed) on the auth.
func ApplyCompactAttributes(auth *Auth, mode string, defaultAllow bool) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	norm := NormalizeCompactMode(mode)
	auth.Attributes["compact_mode"] = norm
	allowed := defaultAllow
	switch norm {
	case CompactModeForceOn:
		allowed = true
	case CompactModeForceOff:
		allowed = false
	}
	if allowed {
		auth.Attributes["compact_allowed"] = "true"
	} else {
		auth.Attributes["compact_allowed"] = "false"
	}
}

// authCompactAllowed reports whether the credential may serve a /responses/compact request.
// A missing attribute means allowed, keeping non-compact and pre-existing auths backward compatible.
func authCompactAllowed(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Attributes == nil {
		return true
	}
	return auth.Attributes["compact_allowed"] != "false"
}

// requireCompactRequest reports whether the request targets the /responses/compact endpoint.
func requireCompactRequest(opts cliproxyexecutor.Options) bool {
	return opts.Alt == cliproxyexecutor.ResponsesCompactAlt
}

// compactCandidateAllowed gates one candidate for a (possibly compact) request.
func compactCandidateAllowed(auth *Auth, requireCompact bool) bool {
	if !requireCompact {
		return true
	}
	return authCompactAllowed(auth)
}

// noCompactAuthError is returned when credentials exist but none allow compact.
func noCompactAuthError() *Error {
	return &Error{
		Code:       "compact_unsupported",
		Message:    "no available credential supports /responses/compact",
		HTTPStatus: http.StatusServiceUnavailable,
	}
}
