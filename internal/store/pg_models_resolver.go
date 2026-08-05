package store

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// ModelsCatalogResolverImpl adapts *ModelsStore to the
// handlers.ModelsCatalogResolver contract. It resolves an internal provider
// key to the official_provider column of the persisted models catalog,
// consulting the in-memory registry to translate the internal key into the
// catalog's (id, provider) coordinate.
//
// It implements OfficialProvider(ctx, providerKey, model) string without
// importing the handlers package (the interface is satisfied structurally).
type ModelsCatalogResolverImpl struct {
	store *ModelsStore
}

// NewModelsCatalogResolver wraps a ModelsStore. Returns nil when the store is
// nil (file-based deployments) so callers can short-circuit.
func NewModelsCatalogResolver(store *ModelsStore) *ModelsCatalogResolverImpl {
	if store == nil {
		return nil
	}
	return &ModelsCatalogResolverImpl{store: store}
}

// openAICompatibleProviderPrefix mirrors util's unexported prefix used to build
// OpenAI-compatible provider keys (e.g. "openai-compatible-opencode").
const openAICompatibleProviderPrefix = "openai-compatible-"

// OfficialProvider resolves an internal provider key (e.g. "claude",
// "openai-compatible-opencode", "gemini", "codex") to the official_provider
// value persisted in the models catalog for the requested model. model is the
// requested model id (may be empty). Returns "" when no catalog row matches.
//
// Resolution:
//  1. Derive the catalog (id, provider) coordinate from the registry for the
//     supplied provider key + model. The catalog's provider column holds the
//     model owner (OwnedBy), not the internal key, so the registry is used to
//     obtain it authoritatively.
//  2. SELECT official_provider FROM models_catalog WHERE id=? AND provider=?.
func (r *ModelsCatalogResolverImpl) OfficialProvider(ctx context.Context, providerKey, model string) string {
	if r == nil || r.store == nil || ctx == nil {
		return ""
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		return ""
	}

	// The catalog's provider column holds the model owner (OwnedBy), e.g.
	// "anthropic" or "opencode" — never the internal "openai-compatible-*"
	// key. Resolve it (and the model id) from the registry.
	modelID, catalogProvider := catalogCoordinate(providerKey, model)
	if modelID == "" || catalogProvider == "" {
		return ""
	}

	official, err := r.store.OfficialProviderByModelAndProvider(ctx, modelID, catalogProvider)
	if err != nil || strings.TrimSpace(official) == "" {
		return ""
	}
	return official
}

// GlobalModelRoute returns the persisted global routing override for model id,
// or nil when none is set. This bridges the store to the handlers.GlobalModelRoute
// contract without the handlers package importing the store (the interface is
// satisfied structurally, mirroring OfficialProvider). The store serves results
// from its in-memory route cache, so request-time reads hit the DB at most once
// per model.
func (r *ModelsCatalogResolverImpl) GlobalModelRoute(ctx context.Context, modelID string) *ModelRoute {
	if r == nil || r.store == nil || ctx == nil {
		return nil
	}
	return r.store.GlobalModelRoute(ctx, strings.TrimSpace(modelID))
}

// catalogCoordinate returns the (model id, provider column value) coordinate
// used by the models catalog for the supplied internal provider key and model.
// The provider column holds the model owner (OwnedBy), derived here via the
// registry. Returns ("", "") when no coordinate can be determined.
func catalogCoordinate(providerKey, model string) (string, string) {
	reg := registry.GetGlobalRegistry()

	// Prefer the explicitly requested model: GetModelInfo carries the owner
	// for that model under this provider key.
	if model != "" {
		if info := reg.GetModelInfo(model, providerKey); info != nil {
			if owner := registry.OwnedByAsProvider(info); owner != "" {
				return model, owner
			}
		}
	}

	// Otherwise take any model registered under the provider key.
	for _, info := range reg.GetAvailableModelsByProvider(providerKey) {
		if info == nil || strings.TrimSpace(info.ID) == "" {
			continue
		}
		if owner := registry.OwnedByAsProvider(info); owner != "" {
			return info.ID, owner
		}
	}

	// Last resort for OpenAI-compatible keys: the suffix equals the compat
	// name, which is the catalog provider column value. There is no model id
	// to key on in this branch, so signal "unresolved".
	if stripped := strings.TrimPrefix(providerKey, openAICompatibleProviderPrefix); stripped != "" && stripped != providerKey {
		return "", stripped
	}
	return "", ""
}
