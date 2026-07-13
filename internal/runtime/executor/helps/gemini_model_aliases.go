package helps

import "strings"

// geminiUpstreamModelAliases maps retired client-facing model IDs to the
// upstream ID Google currently serves. Keep this map small and explicit.
var geminiUpstreamModelAliases = map[string]string{
	// Gemini 3.1 Flash Lite left preview; Vertex/AI Studio return 404 for the
	// old preview resource name (see router-for-me/CLIProxyAPI#4220).
	"gemini-3.1-flash-lite-preview": "gemini-3.1-flash-lite",
}

// CanonicalGeminiUpstreamModel rewrites known retired Gemini model IDs to the
// GA resource name used by Google AI / Vertex publishers/google/models/{id}.
// Unknown IDs are returned unchanged (after trim).
func CanonicalGeminiUpstreamModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return model
	}
	if canonical, ok := geminiUpstreamModelAliases[model]; ok {
		return canonical
	}
	// Also accept "models/<id>" forms that some clients pass through.
	if strings.HasPrefix(model, "models/") {
		id := strings.TrimPrefix(model, "models/")
		if canonical, ok := geminiUpstreamModelAliases[id]; ok {
			return canonical
		}
	}
	return model
}
