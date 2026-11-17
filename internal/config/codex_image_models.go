package config

import "strings"

// ResolveCodexHostedImageBaseModel selects the model whose headers the legacy
// hosted-image builder applies, including its invalid/Responses-Lite fallback.
func ResolveCodexHostedImageBaseModel(cfg *Config, usesResponsesLite func(string) bool) string {
	const fallback = "gpt-5.4-mini"
	if cfg == nil {
		return fallback
	}
	model := strings.TrimSpace(cfg.GPTImage2BaseModel)
	if !strings.HasPrefix(strings.ToLower(model), "gpt-") || usesResponsesLite(model) {
		return fallback
	}
	return model
}

// CodexImageModelUsesDirectEndpoint reports whether an image execution model
// (already stripped of routing prefix and thinking suffix) selects the direct Image API rather than the legacy hosted-image builder.
func CodexImageModelUsesDirectEndpoint(model string) bool {
	model = strings.TrimSpace(model)
	switch strings.ToLower(model) {
	case "gpt-image-1.5", "gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2.5", "gpt-image-2.6-flare", "gpt-image-2.6-sunburst", "gpt-image-2.6":
		return true
	default:
		return false
	}
}
