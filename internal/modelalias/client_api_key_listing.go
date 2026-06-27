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
		upstream string
		alias    string
		fork     bool
	}
	forward := make(map[string][]aliasEntry, len(aliases))
	for _, entry := range aliases {
		name := strings.TrimSpace(entry.Name)
		alias := strings.TrimSpace(entry.Alias)
		if name == "" || alias == "" || strings.EqualFold(name, alias) {
			continue
		}
		key := strings.ToLower(name)
		forward[key] = append(forward[key], aliasEntry{upstream: name, alias: alias, fork: entry.Fork})
	}
	if len(forward) == 0 {
		return models
	}

	// Supplier list ids are often global compat aliases; map them back to upstream names for per-key rules.
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

	matchEntries := func(listID string) []aliasEntry {
		listID = strings.TrimSpace(listID)
		if listID == "" {
			return nil
		}
		if entries := forward[strings.ToLower(listID)]; len(entries) > 0 {
			return entries
		}
		if upstreamKey, ok := compatBridge[strings.ToLower(listID)]; ok {
			return forward[upstreamKey]
		}
		return nil
	}

	appendClone := func(out *[]map[string]any, seen map[string]struct{}, base map[string]any, newID string) {
		newID = strings.TrimSpace(newID)
		if newID == "" {
			return
		}
		key := strings.ToLower(newID)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		clone := make(map[string]any, len(base))
		for k, v := range base {
			clone[k] = v
		}
		clone["id"] = newID
		*out = append(*out, clone)
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
		entries := matchEntries(id)
		if len(entries) == 0 {
			idKey := strings.ToLower(id)
			if _, exists := seen[idKey]; exists {
				continue
			}
			seen[idKey] = struct{}{}
			out = append(out, model)
			continue
		}

		for _, entry := range entries {
			if entry.fork {
				appendClone(&out, seen, model, entry.upstream)
			}
			appendClone(&out, seen, model, entry.alias)
		}
	}
	if len(out) == 0 {
		return models
	}
	return out
}
