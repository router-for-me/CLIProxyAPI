package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// ErrorCodeServiceTierUnavailable reports that no credential may serve the requested service tier.
const ErrorCodeServiceTierUnavailable = "service_tier_unavailable"

func requestsCodexUltrafast(opts cliproxyexecutor.Options) bool {
	return strings.EqualFold(serviceTierFromOptions(opts), "ultrafast")
}

// AllowsServiceTier reports whether auth may serve a request for serviceTier.
// Paths that reuse a bound credential without selection must check it.
func AllowsServiceTier(auth *Auth, serviceTier string) bool {
	return !strings.EqualFold(strings.TrimSpace(serviceTier), "ultrafast") || allowsCodexUltrafast(auth)
}

// allowsCodexUltrafast reports whether auth may receive service_tier=ultrafast.
// Codex accounts other than API keys need the ProMax plan; API keys and other
// providers keep their upstream's service-tier contract.
func allowsCodexUltrafast(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || auth.AuthKind() == AuthKindAPIKey {
		return true
	}
	return strings.EqualFold(authAttribute(auth, "plan_type"), "promax")
}

// codexUltrafastUnavailableError explains a selection failure caused only by
// Ultrafast eligibility. Eligible credentials that are cooling down or out of
// quota still fail through normal selection, which reports their retry time.
func (m *Manager) codexUltrafastUnavailableError(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options) error {
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	if !eligibility.ultrafast {
		return nil
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerSet[canonicalSchedulingProvider(provider)] = struct{}{}
	}
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	registryRef := registry.GetGlobalRegistry()
	excluded := false
	m.mu.RLock()
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled || candidate.Status == StatusDisabled {
			continue
		}
		if _, ok := providerSet[canonicalSchedulingProvider(executorKeyFromAuth(candidate))]; !ok {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if strings.TrimSpace(model) != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		if eligibility.allows(candidate) {
			m.mu.RUnlock()
			return nil
		}
		excluded = excluded || !allowsCodexUltrafast(candidate)
	}
	m.mu.RUnlock()
	if !excluded {
		return nil
	}
	message := fmt.Sprintf("service_tier ultrafast is unavailable for model %s: no enabled credential eligible for it serves this model (Codex accounts need plan_type promax); request priority or default instead", model)
	if pinnedAuthID != "" {
		message = fmt.Sprintf("service_tier ultrafast is unavailable for model %s: this session is pinned to a credential that is not eligible for it (Codex accounts need plan_type promax); start a new session or request priority or default instead", model)
	}
	return &Error{Code: ErrorCodeServiceTierUnavailable, Message: message, HTTPStatus: http.StatusBadRequest}
}
