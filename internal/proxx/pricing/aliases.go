package pricing

import (
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// groupProvider is the synthetic registry provider of proxx model groups; groups have no price.
const groupProvider = "proxx-group"

// aliasTargets maps a client-visible openai-compatibility alias to its upstream model name,
// so listings can price an alias (e.g. "m-good") by the model it actually calls ("gpt-4o-mini").
var aliasTargets atomic.Pointer[map[string]string]

// SetAliases rebuilds the alias map from config. For an alias pool, the first upstream wins.
func SetAliases(cfg *config.Config) {
	m := map[string]string{}
	if cfg != nil {
		for _, compat := range cfg.OpenAICompatibility {
			for _, model := range compat.Models {
				alias, name := strings.TrimSpace(model.Alias), strings.TrimSpace(model.Name)
				if alias == "" || name == "" || strings.EqualFold(alias, name) {
					continue
				}
				if _, exists := m[alias]; !exists {
					m[alias] = name
				}
			}
		}
	}
	aliasTargets.Store(&m)
}

// lookupListing resolves a listed model's price, falling back to the upstream model behind
// a configured alias when the alias itself has no price.
func lookupListing(st *state, c *Catalog, provider, id string) (Match, bool) {
	if m, ok := lookup(st.entries, c, provider, id); ok {
		return m, true
	}
	if p := aliasTargets.Load(); p != nil {
		if target, ok := (*p)[id]; ok {
			return lookup(st.entries, c, provider, target)
		}
	}
	return Match{}, false
}
