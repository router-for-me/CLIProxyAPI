package openai

import "github.com/router-for-me/CLIProxyAPI/v6/internal/registry"

func compactOpenAIModelMaps(models []registry.OpenAIModelSummary) []map[string]any {
	out := make([]map[string]any, 0, len(models))
	for _, model := range models {
		entry := map[string]any{
			"id":       model.ID,
			"object":   model.Object,
			"owned_by": model.OwnedBy,
		}
		if model.Created != 0 {
			entry["created"] = model.Created
		}
		out = append(out, entry)
	}
	return out
}
