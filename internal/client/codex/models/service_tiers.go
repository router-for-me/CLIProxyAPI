package models

// ApplyUltrafastAvailability exposes Ultrafast when the account pool supports it.
func ApplyUltrafastAvailability(response map[string]any, available bool) {
	models, _ := response["models"].([]map[string]any)
	for _, model := range models {
		tiers, _ := model["service_tiers"].([]any)
		filtered := make([]any, 0, len(tiers)+1)
		for _, raw := range tiers {
			tier, _ := raw.(map[string]any)
			if tier["id"] != "ultrafast" {
				filtered = append(filtered, raw)
			}
		}
		// Empty tiers also cover models served by non-Codex providers.
		if available && len(filtered) > 0 {
			switch codexClientMetadataModelID(stringModelValue(model, "slug")) {
			case "gpt-6.1-sol", "gpt-6-astra":
				filtered = append(filtered, map[string]any{
					"id":          "ultrafast",
					"name":        "Ultrafast",
					"description": "Fastest speed, increased usage",
				})
			}
		}
		model["service_tiers"] = filtered
	}
}
