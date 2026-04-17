package helps

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// OAuthLevers holds the resolved per-request OAuth cloaking lever values.
// Each field is a concrete bool (not a pointer) so callers can use it without nil checks.
type OAuthLevers struct {
	// SanitizeSystemPrompt controls whether the forwarded client system prompt is collapsed
	// to a stub. true (default) = legacy behavior (sanitize). false = pass-through.
	SanitizeSystemPrompt bool
	// RemapToolNames controls whether tool names are remapped to Claude Code canonical names.
	// true (default) = legacy behavior (remap). false = keep original names.
	RemapToolNames bool
	// InjectBillingHeader controls whether the x-anthropic-billing-account header block is
	// injected into the system prompt. true (default) = legacy behavior. false = skip.
	InjectBillingHeader bool
}

// ResolveOAuthLevers computes the final lever values for the current request by consulting
// (in order): the per-ClaudeKey CloakConfig, and the header opt-out mechanism. The header
// opt-out is only considered when cfg.OAuthDisableHeader is non-empty and the request
// presents a matching X-Cliproxy-Cloak-Token value.
//
// Nil cfg (or nil fields) defaults to the legacy behavior (all levers enabled).
func ResolveOAuthLevers(cfg *config.CloakConfig, header http.Header) OAuthLevers {
	// Start from config-level values (nil => legacy true).
	levers := OAuthLevers{
		SanitizeSystemPrompt: resolvePointerBool(cfg, func(c *config.CloakConfig) *bool { return c.OAuthSanitizeSystemPrompt }),
		RemapToolNames:       resolvePointerBool(cfg, func(c *config.CloakConfig) *bool { return c.OAuthRemapToolNames }),
		InjectBillingHeader:  resolvePointerBool(cfg, func(c *config.CloakConfig) *bool { return c.OAuthInjectBillingHeader }),
	}

	// Apply header opt-out only when the shared secret is configured and matches.
	if cfg == nil || strings.TrimSpace(cfg.OAuthDisableHeader) == "" {
		// Fails closed: no secret configured means header opt-out is disabled entirely.
		return levers
	}

	presentedToken := strings.TrimSpace(header.Get("X-Cliproxy-Cloak-Token"))
	if presentedToken == "" || presentedToken != strings.TrimSpace(cfg.OAuthDisableHeader) {
		// Missing or wrong token — ignore opt-out header.
		return levers
	}

	// Token matched; apply opt-out directives from X-Cliproxy-Cloak-Opt-Out.
	optOutHeader := header.Get("X-Cliproxy-Cloak-Opt-Out")
	for _, directive := range strings.Split(optOutHeader, ",") {
		switch strings.ToLower(strings.TrimSpace(directive)) {
		case "all":
			levers.SanitizeSystemPrompt = false
			levers.RemapToolNames = false
			levers.InjectBillingHeader = false
		case "sanitize":
			levers.SanitizeSystemPrompt = false
		case "tool-remap":
			levers.RemapToolNames = false
		case "billing":
			levers.InjectBillingHeader = false
		}
	}

	return levers
}

// resolvePointerBool reads the *bool field from cfg using the provided accessor.
// nil cfg or nil field => returns true (legacy default).
func resolvePointerBool(cfg *config.CloakConfig, accessor func(*config.CloakConfig) *bool) bool {
	if cfg == nil {
		return true
	}
	v := accessor(cfg)
	if v == nil {
		return true
	}
	return *v
}
