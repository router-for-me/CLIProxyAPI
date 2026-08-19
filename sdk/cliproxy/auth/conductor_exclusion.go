package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// Ordered-pool + exclusion integration for the conductor.
//
// This file implements ai-hub-ollw AC#5: excluded models must be absent from
// GetAvailableModels and rejected when requested. Listing-time exclusion is
// already enforced by sdk/cliproxy/service.go applyExcludedModels at auth-
// registration time. This file adds the route-level rejection guard used by
// executeWithOrderedFailover and the legacy executeMixed*Once paths.
//
// The guard is conservative: it only rejects a request when the resolved
// model (after alias resolution) matches a pattern in the global
// oauth-excluded-models config for the request's OAuth channel. Per-credential
// exclusion lists are enforced by the scheduler (the auth's
// supportedModelSetForAuth already drops excluded models), so this is the
// global backstop that stops the request before any upstream call.

// isModelExcludedForRoute reports whether requestedModel matches a pattern in
// the global OAuthExcludedModels config for the given provider channel.
// channel is the OAuthModelAliasChannel (vertex, claude, codex, ...) which is
// also the oauth-excluded-models config key. An empty channel or model means
// "no exclusion" (the call does not apply).
func (m *Manager) isModelExcludedForRoute(channel, requestedModel string) bool {
	if m == nil {
		return false
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" {
		return false
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		return false
	}
	if len(cfg.OAuthExcludedModels) == 0 {
		return false
	}
	patterns := cfg.OAuthExcludedModels[channel]
	if len(patterns) == 0 {
		return false
	}
	parsed := thinking.ParseSuffix(requestedModel)
	candidates := []string{strings.ToLower(strings.TrimSpace(parsed.ModelName))}
	if canonical := strings.ToLower(strings.TrimSpace(requestedModel)); canonical != "" && canonical != candidates[0] {
		candidates = append(candidates, canonical)
	}
	for _, rawPattern := range patterns {
		pattern := strings.ToLower(strings.TrimSpace(rawPattern))
		if pattern == "" {
			continue
		}
		for _, candidate := range candidates {
			if matchExcludedPattern(pattern, candidate) {
				return true
			}
		}
	}
	return false
}

// matchExcludedPattern is a copy of sdk/cliproxy/service.go's matchWildcard
// kept local so the auth package does not depend on the service package.
func matchExcludedPattern(pattern, value string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	if prefix := parts[0]; prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return false
		}
		value = value[len(prefix):]
	}
	if suffix := parts[len(parts)-1]; suffix != "" {
		if !strings.HasSuffix(value, suffix) {
			return false
		}
		value = value[:len(value)-len(suffix)]
	}
	for i := 1; i < len(parts)-1; i++ {
		segment := parts[i]
		if segment == "" {
			continue
		}
		idx := strings.Index(value, segment)
		if idx < 0 {
			return false
		}
		value = value[idx+len(segment):]
	}
	return true
}

// routeGuardExcludedModel returns a permanent Error when the requested model
// is on the global oauth-excluded-models list for the resolved OAuth channel.
// Returns nil when the request should proceed. This is the route-level
// backstop for ai-hub-ollw AC#5.
func (m *Manager) routeGuardExcludedModel(providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	if m == nil || len(providers) == 0 {
		return nil
	}
	requestedModel := strings.TrimSpace(authSelectionModelFromOptions(opts, req.Model))
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(req.Model)
	}
	if requestedModel == "" {
		return nil
	}
	for _, provider := range providers {
		channel := OAuthModelAliasChannel(strings.TrimSpace(provider), "oauth")
		if channel == "" {
			continue
		}
		if m.isModelExcludedForRoute(channel, requestedModel) {
			return &Error{
				Code:    "model_excluded",
				Message: "model " + requestedModel + " is excluded by oauth-excluded-models for provider " + provider,
			}
		}
	}
	return nil
}
