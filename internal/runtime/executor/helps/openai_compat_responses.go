package helps

import "github.com/router-for-me/CLIProxyAPI/v8/internal/config"

// ShouldUseResponsesAPIForModel resolves the upstream protocol using the same
// upstream-name-before-alias precedence as other per-model compatibility flags.
func ShouldUseResponsesAPIForModel(compat *config.OpenAICompatibility, upstreamModel, requestedModel string) bool {
	if compat == nil {
		return false
	}
	for _, name := range []string{upstreamModel, requestedModel} {
		name = normalizeOpenAICompatibilityModelName(name)
		if name == "" {
			continue
		}
		for _, alias := range []bool{false, true} {
			for _, model := range compat.Models {
				candidate := model.Name
				if alias {
					candidate = model.Alias
				}
				if name == normalizeOpenAICompatibilityModelName(candidate) {
					return model.UseResponsesAPI
				}
			}
		}
	}
	return false
}
