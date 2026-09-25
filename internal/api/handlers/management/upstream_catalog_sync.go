package management

import (
	"context"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/upstreamsync"
)

// syncCatalogFromUpstreamProviders mirrors the model definitions carried by
// normalized upstream_providers rows into models_catalog, synchronously, on
// the request path. It exists so that adding (or renaming a model of) a
// provider in the Upstream Providers editor is reflected in the Models Catalog
// immediately, rather than after the two stacked asynchronous hops that
// otherwise populate the catalog (config-reload goroutine → registry
// RegisterClient → PGSync.OnModelsRegistered). Those async hops still run
// afterward (via applyUpstreamProviders) for live routing/availability; this
// call only guarantees the catalog rows exist deterministically.
//
// The mapping intentionally mirrors the registry's OpenAI-compatibility model
// build (sdk/cliproxy/service_models.buildOpenAICompatibilityConfigModels) so
// the catalog row created here matches the one the async registry hook would
// later upsert (same id, provider, owned_by, type) — the upsert is then a
// no-op conflict update rather than a duplicate. The management package cannot
// import sdk/cliproxy (would pull a heavy dependency), so the small mapping is
// duplicated here in terms of store.UpstreamProviderModel directly.
//
// Best-effort: errors are logged and never returned to the caller, mirroring
// the existing RenameProvider catalog-repoint behavior (an upstream save must
// not fail because the catalog mirror hiccupped). Returns the number of model
// rows queued for upsert (0 when PG is not configured).
func (h *Handler) syncCatalogFromUpstreamProviders(ctx context.Context) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	upstream := h.pgUpstreamProviders
	models := h.pgModels
	h.mu.Unlock()
	if upstream == nil || models == nil {
		// PG not configured (file-only deployments) — nothing to mirror.
		return 0
	}

	providers, err := upstream.List(ctx)
	if err != nil {
		log.WithError(err).Warn("upstream-catalog-sync: list upstream providers failed")
		return 0
	}

	rows := make([]store.StoredModel, 0, 32)
	now := time.Now().Unix()
	for _, p := range providers {
		if p.Disabled || len(p.Models) == 0 {
			continue
		}
		// Only providers that own their catalog rows by upstream name verbatim
		// (OpenAI-compatibility + the API-key compat variants) are mirrored
		// here. Built-in OAuth providers (claude/gemini/codex/xai OAuth) get
		// their catalog from the registry's embedded definitions + live auth,
		// not from the upstream_providers models[] list, so syncing them from
		// here would double-key against the wrong owner. See OwnedByAsProvider.
		if !mirrorsCatalogByUpstreamName(p.ProviderType) {
			continue
		}
		providerName := strings.TrimSpace(p.Name)
		if providerName == "" {
			// API-key providers (gemini/codex/xai/claude/vertex) key their
			// catalog off the fixed owner string, not the (often empty)
			// upstream name; skip them — the registry/seed path owns those.
			continue
		}
		for _, m := range p.Models {
			stored := upstreamModelToCatalog(m, providerName, now)
			if stored.ID == "" {
				continue
			}
			rows = append(rows, stored)
		}
	}
	if len(rows) == 0 {
		return 0
	}
	if err := models.UpsertModels(ctx, rows); err != nil {
		log.WithError(err).
			WithField("rows", len(rows)).
			Warn("upstream-catalog-sync: upsert models failed; catalog will be refreshed by the next registry sync")
		return 0
	}
	log.WithField("rows", len(rows)).Debug("upstream-catalog-sync: mirrored upstream-providers models into models_catalog")
	return len(rows)
}

// mirrorsCatalogByUpstreamName reports whether a provider_type's catalog rows
// are keyed by the upstream name verbatim (OwnedBy = Name). OpenAI-compatibility
// providers always are (buildOpenAICompatibilityConfigModels sets
// OwnedBy = compat.Name). The API-key compat variants are included for
// completeness, but in practice they carry no explicit Models[] in config and
// are seeded by the registry — the Name-empty guard above skips them anyway.
func mirrorsCatalogByUpstreamName(providerType string) bool {
	switch providerType {
	case upstreamsync.TypeOpenAICompatibility,
		upstreamsync.TypeVertexAPIKey,
		upstreamsync.TypeGeminiAPIKey,
		upstreamsync.TypeCodexAPIKey,
		upstreamsync.TypeXAIAPIKey,
		upstreamsync.TypeMetaAPIKey,
		upstreamsync.TypeClaudeAPIKey,
		upstreamsync.TypeInteractionsAPIKey:
		return true
	}
	return false
}

// upstreamModelToCatalog builds a models_catalog row from one upstream
// provider model entry. id = alias when present (mirroring the registry's
// buildConfiguredModelInfo alias-preference), otherwise the model name;
// provider/owned_by/type are the upstream provider name verbatim (matching the
// OwnedBy invariant relied on by RenameProvider). displayName falls back to
// the alias/name when unset, matching the registry behavior.
func upstreamModelToCatalog(m store.UpstreamProviderModel, providerName string, now int64) store.StoredModel {
	name := strings.TrimSpace(m.Name)
	alias := strings.TrimSpace(m.Alias)
	if alias == "" {
		alias = name
	}
	modelType := "openai-compatibility"
	if m.Image {
		modelType = registry.OpenAIImageModelType
	}
	displayName := strings.TrimSpace(m.DisplayName)
	if displayName == "" {
		displayName = alias
	}
	return store.StoredModel{
		ID:               alias,
		Provider:         providerName,
		OfficialProvider: providerName,
		Object:           "model",
		Created:          now,
		OwnedBy:          providerName,
		Type:             modelType,
		DisplayName:      displayName,
		Name:             name,
		InputModalities:  m.InputModalities,
		OutputModalities: m.OutputModalities,
		Thinking:         m.Thinking,
	}
}
