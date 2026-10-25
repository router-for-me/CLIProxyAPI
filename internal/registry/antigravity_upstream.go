package registry

import "strings"

// ResolveAntigravityUpstreamModel chooses a raw entitled model deterministically.
// A nil catalog retains legacy routing while account capabilities are unknown.
func ResolveAntigravityUpstreamModel(modelName string, entitled map[string]struct{}) string {
	model := strings.ToLower(strings.TrimSpace(modelName))
	preferred := modelName
	switch model {
	case "gemini-4-argon", "gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash":
		preferred = model + "-high"
	case "gemini-3.1-pro", "gemini-3-pro":
		preferred = "gemini-pro-agent"
	}
	if entitled == nil {
		return preferred
	}
	if _, ok := entitled[model]; ok {
		return model
	}
	if _, ok := entitled[preferred]; ok {
		return preferred
	}
	canonical := NormalizeAntigravityModelID(model)
	selected := ""
	for raw := range entitled {
		if NormalizeAntigravityModelID(raw) == canonical && (selected == "" || raw < selected) {
			selected = raw
		}
	}
	return selected
}
