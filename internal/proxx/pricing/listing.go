package pricing

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// modelProvider returns the provider key the registry serves modelID with, which is the
// same key usage records carry, so listed and billed prices resolve identically.
func modelProvider(modelID string) string {
	if providers := registry.GetGlobalRegistry().GetModelProviders(modelID); len(providers) > 0 {
		return providers[0]
	}
	return ""
}

// listingObject builds the public "pricing" object for a match.
func listingObject(m Match) map[string]any {
	obj := map[string]any{"currency": "USD", "unit": "per_1m_tokens", "source": m.Source}
	put := func(key string, v *float64) {
		if v != nil {
			obj[key] = *v
		}
	}
	put("input", m.Price.Input)
	put("output", m.Price.Output)
	put("cache_read", m.Price.CacheRead)
	put("cache_write", m.Price.CacheWrite)
	return obj
}

// AttachPricing adds a "pricing" object to every model map whose price is known.
// idKey is the map key holding the model ID ("id" or Gemini "name"). It is a no-op when
// pricing is disabled. Models are mutated in place; callers pass freshly cloned maps.
func AttachPricing(models []map[string]any, idKey string) []map[string]any {
	st := currentState()
	if st == nil || st.disabled {
		return models
	}
	c := currentCatalog()
	for _, model := range models {
		if model == nil {
			continue
		}
		id, _ := model[idKey].(string)
		id = strings.TrimPrefix(id, "models/")
		if id == "" {
			continue
		}
		provider := modelProvider(id)
		if provider == groupProvider {
			continue
		}
		if m, ok := lookupListing(st, c, provider, id); ok {
			model["pricing"] = listingObject(m)
		}
	}
	return models
}

// ModelPrice is one registry model with its resolved price (management view).
type ModelPrice struct {
	Model      string   `json:"model"`
	Provider   string   `json:"provider"`
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
	Source     string   `json:"source"`
}

// RegistryPrices resolves prices for every model currently in the registry.
// Unknown models are included with null prices and source "unknown".
func RegistryPrices() []ModelPrice {
	st := currentState()
	c := currentCatalog()
	models := registry.GetGlobalRegistry().GetAvailableModels("openai")
	out := make([]ModelPrice, 0, len(models))
	seen := map[string]bool{}
	for _, model := range models {
		id, _ := model["id"].(string)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		provider := modelProvider(id)
		if provider == groupProvider {
			continue
		}
		row := ModelPrice{Model: id, Provider: provider, Source: SourceUnknown}
		if st != nil && !st.disabled {
			if m, ok := lookupListing(st, c, provider, id); ok {
				row.Input, row.Output, row.CacheRead, row.CacheWrite = m.Price.Input, m.Price.Output, m.Price.CacheRead, m.Price.CacheWrite
				row.Source = m.Source
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}
