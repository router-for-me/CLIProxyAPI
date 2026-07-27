package handlers

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/net/context"
)

const (
	openAICompatibleProviderPrefix = "openai-compatible-"
	openAICompatProviderAttrKey    = "provider_key"
	openAICompatNameAttrKey        = "compat_name"
	openAICompatibilityProvider    = "openai-compatibility"
)

// officialProviderNames resolves each provider key to its official, operator-
// facing name (as persisted in the models catalog when available) and returns
// the joined, comma-separated list. Empty results fall back to the original
// key so the error always shows something meaningful.
func officialProviderNames(h *BaseAPIHandler, ctx context.Context, providers []string, model string) []string {
	if len(providers) == 0 {
		return providers
	}
	out := make([]string, 0, len(providers))
	for _, key := range providers {
		out = append(out, officialProviderName(h, ctx, key, model))
	}
	return out
}

// officialProviderName resolves a single provider key to its official name.
// Resolution order: PG-backed models catalog (when wired) → OpenAI-compat
// auth record label → registry OwnedBy → openai-compatible- suffix → raw key.
func officialProviderName(h *BaseAPIHandler, ctx context.Context, key, model string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	lowerKey := strings.ToLower(key)

	// 1. Persisted models catalog (official_provider column). This is the
	// source of truth surfaced by the dashboard, so it takes precedence.
	if h != nil && h.ModelsCatalogStore != nil {
		resolverCtx := ctx
		if resolverCtx == nil {
			resolverCtx = context.Background()
		}
		if name := strings.TrimSpace(h.ModelsCatalogStore.OfficialProvider(resolverCtx, lowerKey, model)); name != "" {
			return name
		}
	}

	// 2. OpenAI-compatible providers: read the compat_name (official name)
	// directly from the auth record carrying this provider key.
	if h != nil && h.AuthManager != nil {
		if name := officialNameFromAuthManager(h.AuthManager, lowerKey); name != "" {
			return name
		}
	}

	// 3. Registry OwnedBy — prefer the requested model, then any model
	// registered for this provider key.
	reg := registry.GetGlobalRegistry()
	if model != "" {
		if info := reg.GetModelInfo(model, lowerKey); info != nil {
			if name := registry.OwnedByAsProvider(info); name != "" {
				return name
			}
		}
	}
	for _, info := range reg.GetAvailableModelsByProvider(lowerKey) {
		if name := registry.OwnedByAsProvider(info); name != "" {
			return name
		}
	}

	// 4. Fallback: openai-compatible- suffix (the configured compat name),
	// then the raw key.
	if stripped := strings.TrimPrefix(lowerKey, openAICompatibleProviderPrefix); stripped != "" && stripped != lowerKey {
		return stripped
	}
	return lowerKey
}

// officialNameFromAuthManager surfaces the human-readable compat name for an
// OpenAI-compatible provider key from the auth records carrying it. Returns
// "" when the auth manager has no mapping or the key is not an openai-compat
// provider key.
func officialNameFromAuthManager(am *coreauth.Manager, key string) string {
	if am == nil || key == "" {
		return ""
	}
	if !strings.HasPrefix(key, openAICompatibleProviderPrefix) && key != openAICompatibilityProvider {
		return ""
	}
	wantKey := strings.ToLower(key)
	for _, a := range am.List() {
		if a == nil {
			continue
		}
		// Match by the provider_key attribute (already normalized to the
		// "openai-compatible-<name>" form by the watcher synthesizer).
		if attrKey := strings.ToLower(strings.TrimSpace(a.Attributes[openAICompatProviderAttrKey])); attrKey == wantKey {
			if name := strings.TrimSpace(a.Attributes[openAICompatNameAttrKey]); name != "" {
				return name
			}
		}
		// Fall back to the openai-compatibility provider path, where the
		// official name is the auth Label when no compat_name is set.
		if wantKey == openAICompatibilityProvider &&
			strings.EqualFold(strings.TrimSpace(a.Provider), openAICompatibilityProvider) {
			if name := strings.TrimSpace(a.Label); name != "" {
				return name
			}
		}
	}
	return ""
}
