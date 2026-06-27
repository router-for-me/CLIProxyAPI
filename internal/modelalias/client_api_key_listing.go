package modelalias

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// ApplyClientAPIKeyModelAliasesToOpenAIMaps rewrites OpenAI-style model maps for a client API key.
func ApplyClientAPIKeyModelAliasesToOpenAIMaps(keys config.ClientAPIKeys, clientKey string, models []map[string]any, compat []config.OpenAICompatibility) []map[string]any {
	aliases := keys.ModelAliasesFor(clientKey)
	if len(aliases) == 0 || len(models) == 0 {
		return models
	}
	type aliasEntry struct {
		alias string
		fork  bool
	}
	forward := make(map[string][]aliasEntry, len(aliases))
	for _, entry := range aliases {
		name := strings.TrimSpace(entry.Name)
		alias := strings.TrimSpace(entry.Alias)
		if name == "" || alias == "" || strings.EqualFold(name, alias) {
			continue
		}
		key := strings.ToLower(name)
		forward[key] = append(forward[key], aliasEntry{alias: alias, fork: entry.Fork})
	}
	if len(forward) == 0 {
		return models
	}

	compatBridge := make(map[string]string)
	for i := range compat {
		if compat[i].Disabled {
			continue
		}
		for _, model := range compat[i].Models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" {
				continue
			}
			if alias == "" {
				alias = name
			}
			compatBridge[strings.ToLower(alias)] = strings.ToLower(name)
		}
	}

	out := make([]map[string]any, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		id, _ := model["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		entries := forward[strings.ToLower(id)]
		if len(entries) == 0 {
			if upstream, ok := compatBridge[strings.ToLower(id)]; ok {
				entries = forward[upstream]
			}
		}
		if len(entries) == 0 {
			idKey := strings.ToLower(id)
			if _, exists := seen[idKey]; exists {
				continue
			}
			seen[idKey] = struct{}{}
			out = append(out, model)
			continue
		}

		keepOriginal := false
		for _, entry := range entries {
			if entry.fork {
				keepOriginal = true
				break
			}
		}
		if keepOriginal {
			idKey := strings.ToLower(id)
			if _, exists := seen[idKey]; !exists {
				seen[idKey] = struct{}{}
				out = append(out, model)
			}
		}

		addedAlias := false
		for _, entry := range entries {
			mappedID := strings.TrimSpace(entry.alias)
			if mappedID == "" || strings.EqualFold(mappedID, id) {
				continue
			}
			aliasKey := strings.ToLower(mappedID)
			if _, exists := seen[aliasKey]; exists {
				continue
			}
			seen[aliasKey] = struct{}{}
			clone := make(map[string]any, len(model))
			for k, v := range model {
				clone[k] = v
			}
			clone["id"] = mappedID
			out = append(out, clone)
			addedAlias = true
		}

		if !keepOriginal && !addedAlias {
			idKey := strings.ToLower(id)
			if _, exists := seen[idKey]; exists {
				continue
			}
			seen[idKey] = struct{}{}
			out = append(out, model)
		}
	}
	if len(out) == 0 {
		return models
	}
	return out
}
