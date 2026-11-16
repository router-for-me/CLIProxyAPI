package apicost

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// snapshotProviders are the LiteLLM providers kept in the embedded snapshot.
var snapshotProviders = map[string]bool{
	"anthropic": true,
	"openai":    true,
	"gemini":    true,
	"xai":       true,
	"deepseek":  true,
	"moonshot":  true,
}

// snapshotKey keeps bare model ids and the provider-prefixed ids that
// Catalog.Lookup tries for providers whose LiteLLM entries are prefixed.
func snapshotKey(name string) bool {
	prefix, _, prefixed := strings.Cut(name, "/")
	if !prefixed {
		return true
	}
	switch prefix {
	case "gemini", "xai", "deepseek", "moonshot":
		return !strings.Contains(strings.TrimPrefix(name, prefix+"/"), "/")
	}
	return false
}

// snapshotFields are the LiteLLM fields the calculator reads.
var snapshotFields = []string{
	"litellm_provider",
	"input_cost_per_token",
	"output_cost_per_token",
	"cache_creation_input_token_cost",
	"cache_creation_input_token_cost_above_12hr",
	"cache_read_input_token_cost",
	"input_cost_per_token_above_200k_tokens",
	"output_cost_per_token_above_200k_tokens",
	"cache_creation_input_token_cost_above_200k_tokens",
	"cache_read_input_token_cost_above_200k_tokens",
	"input_cost_per_token_above_272k_tokens",
	"output_cost_per_token_above_272k_tokens",
	"cache_creation_input_token_cost_above_272k_tokens",
	"cache_read_input_token_cost_above_272k_tokens",
	"provider_specific_entry",
}

// FilterLiteLLM reduces a full LiteLLM catalog to the providers and fields
// used by the calculator, for the embedded snapshot.
func FilterLiteLLM(data []byte) ([]byte, error) {
	root := ojson.ParseBytes(data)
	if !root.IsObject() {
		return nil, fmt.Errorf("pricing catalog is not a JSON object")
	}
	type entry struct {
		name  string
		value ojson.Result
	}
	var entries []entry
	root.ForEach(func(key, value ojson.Result) bool {
		name := key.String()
		if !value.IsObject() || !snapshotKey(name) {
			return true
		}
		if !snapshotProviders[value.Get("litellm_provider").String()] {
			return true
		}
		if !value.Get("input_cost_per_token").Exists() || !value.Get("output_cost_per_token").Exists() {
			return true
		}
		entries = append(entries, entry{name: name, value: value})
		return true
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	doc := ojson.NewDocString("{}")
	for _, item := range entries {
		model := ojson.NewDocString("{}")
		for _, field := range snapshotFields {
			if value := item.value.Get(field); value.Exists() {
				if errSet := model.SetRaw(field, value.Raw); errSet != nil {
					return nil, fmt.Errorf("set %s.%s: %w", item.name, field, errSet)
				}
			}
		}
		if errSet := doc.SetRawBytes(ojson.Escape(item.name), model.Bytes()); errSet != nil {
			return nil, fmt.Errorf("set %s: %w", item.name, errSet)
		}
	}
	return doc.Bytes(), nil
}
