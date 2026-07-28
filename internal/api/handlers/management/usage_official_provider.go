package management

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// officialProviderKey is the dedup map key for resolved official_provider
// lookups: (internal provider key, resolved model id).
type officialProviderKey struct {
	provider string
	model    string
}

// resolveOfficialProvider returns the official_provider label for a single
// (providerKey, model) pair, using cache to dedupe within one request. Returns
// "" when the resolver is unset or no catalog row matches; callers should fall
// back to the raw provider value. The resolver's own errors are swallowed
// internally (it returns "" on miss), so a catalog lookup miss never blocks a
// usage listing — mirroring the best-effort spirit of FillCostBreakdown but
// more lenient (no 500 path).
func (h *Handler) resolveOfficialProvider(ctx context.Context, providerKey, model string, cache map[officialProviderKey]string) string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	resolver := h.modelsCatalogResolver
	h.mu.Unlock()
	if resolver == nil || providerKey == "" {
		return ""
	}
	key := officialProviderKey{providerKey, model}
	if v, ok := cache[key]; ok {
		return v
	}
	official := resolver.OfficialProvider(ctx, providerKey, model)
	cache[key] = official
	return official
}

// FillOfficialProvider populates the OfficialProvider field on every
// UsageEventRow in place, best-effort. The per-row catalog lookup is
// deduplicated per (provider, model) so a page of 25 events for the same model
// costs a single round-trip — mirroring FillCostBreakdown's caching. No-op when
// the resolver is nil (file-only deployments or no PG backend); rows then keep
// an empty OfficialProvider and the dashboard falls back to the raw provider.
func (h *Handler) FillOfficialProvider(ctx context.Context, rows []store.UsageEventRow) {
	if h == nil || len(rows) == 0 {
		return
	}
	cache := make(map[officialProviderKey]string, len(rows))
	for i := range rows {
		r := &rows[i]
		// Skip rows that already carry a resolved value (e.g. when a future
		// caller pre-populates it). Keeps the call idempotent.
		if r.OfficialProvider != "" {
			continue
		}
		r.OfficialProvider = h.resolveOfficialProvider(ctx, r.Provider, r.Model, cache)
	}
}

// FillOfficialProviderErrors mirrors FillOfficialProvider for the errors table
// projection. Best-effort, deduplicated, no-op when the resolver is nil.
func (h *Handler) FillOfficialProviderErrors(ctx context.Context, rows []store.UsageErrorRow) {
	if h == nil || len(rows) == 0 {
		return
	}
	cache := make(map[officialProviderKey]string, len(rows))
	for i := range rows {
		r := &rows[i]
		if r.OfficialProvider != "" {
			continue
		}
		r.OfficialProvider = h.resolveOfficialProvider(ctx, r.Provider, r.Model, cache)
	}
}
