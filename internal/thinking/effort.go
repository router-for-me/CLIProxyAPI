package thinking

import (
	"fmt"
	"strings"
)

// ExtractEffort extracts a normalized thinking/reasoning effort value from a
// model suffix or request body.
func ExtractEffort(body []byte, model, fromFormat, toFormat string) string {
	fromProvider := normalizeEffortProvider(fromFormat)
	toProvider := normalizeEffortProvider(toFormat)
	provider := toProvider
	if provider == "" {
		provider = fromProvider
	}
	if provider == "" {
		provider = "openai"
	}

	suffix := ParseSuffix(model)
	if suffix.HasSuffix {
		if effort := formatEffort(parseSuffixToConfig(suffix.RawSuffix, provider, model)); effort != "" {
			return effort
		}
	}

	if effort := formatEffort(extractThinkingConfig(body, provider)); effort != "" {
		return effort
	}
	if fromProvider != "" && fromProvider != provider {
		return formatEffort(extractThinkingConfig(body, fromProvider))
	}
	return ""
}

func normalizeEffortProvider(provider string) string {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	if normalized == "openai-response" {
		return "codex"
	}
	return normalized
}

func formatEffort(config ThinkingConfig) string {
	switch config.Mode {
	case ModeNone:
		return "none"
	case ModeAuto:
		return "auto"
	case ModeLevel:
		return strings.ToLower(strings.TrimSpace(string(config.Level)))
	case ModeBudget:
		if config.Budget > 0 {
			return fmt.Sprintf("budget:%d", config.Budget)
		}
	}
	return ""
}
