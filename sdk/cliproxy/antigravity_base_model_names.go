package cliproxy

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const antigravityOAuthModelAliasChannel = "antigravity"

// antigravityLegacyModelIDs are the level-suffixed catalog IDs that were
// published before base names. They stay published next to their base name so
// existing clients keep working. Later catalog entries are published by base
// name only, so this list must not grow; delete an entry to retire its old ID.
var antigravityLegacyModelIDs = map[string]struct{}{
	"gemini-4-neon-xhigh":     {},
	"gemini-4-hydrogen-xhigh": {},
	"gemini-4-argon-high":     {},
	"gemini-4-carbon-high":    {},
	"gemini-3.8-flash-high":   {},
}

// antigravityBaseModelName returns the catalog ID without its trailing
// thinking level, e.g. gemini-4-neon-xhigh -> gemini-4-neon. It returns "" for
// non-Gemini models and for IDs whose last segment is not one of the model's
// own thinking levels, so entries such as gemini-3.8-flash-tiered are untouched.
func antigravityBaseModelName(model *ModelInfo) string {
	if model == nil || model.Thinking == nil {
		return ""
	}
	id := strings.TrimSpace(model.ID)
	if !strings.HasPrefix(strings.ToLower(id), "gemini-") {
		return ""
	}
	idx := strings.LastIndexByte(id, '-')
	if idx <= 0 || idx == len(id)-1 {
		return ""
	}
	suffix := id[idx+1:]
	for _, level := range model.Thinking.Levels {
		if strings.EqualFold(strings.TrimSpace(level), suffix) {
			return id[:idx]
		}
	}
	return ""
}

// antigravityBaseModelAliases derives one alias per Antigravity Gemini model
// whose catalog ID carries a thinking level. The base name routes back to the
// catalog entry and replaces its ID in model listings, except for the legacy
// IDs, which are kept published as well.
func antigravityBaseModelAliases(models []*ModelInfo) []config.OAuthModelAlias {
	if len(models) == 0 {
		return nil
	}
	catalogIDs := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model != nil {
			catalogIDs[strings.ToLower(strings.TrimSpace(model.ID))] = struct{}{}
		}
	}
	var out []config.OAuthModelAlias
	seen := make(map[string]struct{})
	for _, model := range models {
		base := antigravityBaseModelName(model)
		if base == "" {
			continue
		}
		key := strings.ToLower(base)
		if _, exists := catalogIDs[key]; exists {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		id := strings.TrimSpace(model.ID)
		_, legacy := antigravityLegacyModelIDs[strings.ToLower(id)]
		out = append(out, config.OAuthModelAlias{
			Name:         id,
			Alias:        base,
			Fork:         legacy,
			ForceMapping: true,
		})
	}
	return out
}

// withAntigravityBaseModelAliases appends the derived base-name aliases to the
// explicitly configured Antigravity aliases. Explicit aliases win: a derived
// alias is skipped when its upstream model or its alias is already configured.
func withAntigravityBaseModelAliases(explicit []config.OAuthModelAlias) []config.OAuthModelAlias {
	derived := antigravityBaseModelAliases(registry.GetAntigravityModels())
	if len(derived) == 0 {
		return explicit
	}
	taken := make(map[string]struct{}, len(explicit)*2)
	for _, entry := range explicit {
		if name := strings.ToLower(strings.TrimSpace(entry.Name)); name != "" {
			taken[name] = struct{}{}
		}
		if alias := strings.ToLower(strings.TrimSpace(entry.Alias)); alias != "" {
			taken[alias] = struct{}{}
		}
	}
	out := append([]config.OAuthModelAlias(nil), explicit...)
	for _, entry := range derived {
		if _, exists := taken[strings.ToLower(entry.Name)]; exists {
			continue
		}
		if _, exists := taken[strings.ToLower(entry.Alias)]; exists {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// effectiveOAuthModelAlias returns the alias map used for request routing:
// the configured aliases plus the derived Antigravity base-name aliases. The
// configured map is not mutated.
func effectiveOAuthModelAlias(cfg *config.Config) map[string][]config.OAuthModelAlias {
	if cfg == nil {
		return nil
	}
	explicit := cfg.OAuthModelAlias[antigravityOAuthModelAliasChannel]
	merged := withAntigravityBaseModelAliases(explicit)
	if len(merged) == len(explicit) {
		return cfg.OAuthModelAlias
	}
	out := make(map[string][]config.OAuthModelAlias, len(cfg.OAuthModelAlias)+1)
	for channel, aliases := range cfg.OAuthModelAlias {
		out[channel] = aliases
	}
	out[antigravityOAuthModelAliasChannel] = merged
	return out
}
