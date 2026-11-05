package thinking

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// ClaudeEffortDirective returns the effort of the last Claude mid-conversation
// effort directive in body, lowercased, or "" when there is none or the
// request turns thinking off (thinking.type "disabled" or "between_tools").
//
// A directive is a role=system message carrying output_config.effort
// (mid-conversation-output-config beta). It sets the effort for every later
// reply, so the last one is the effort of the current turn. Clients that send
// directives keep the top-level output_config.effort fixed for prompt caching,
// which makes that value a placeholder rather than the requested effort.
func ClaudeEffortDirective(body []byte) string {
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(ojson.GetBytesNoCopy(body, "thinking.type").String())) {
	case "disabled", "between_tools":
		return ""
	}
	messages := ojson.GetBytesNoCopy(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	var effort string
	messages.ForEach(func(_, message ojson.Result) bool {
		if !strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "system") {
			return true
		}
		value := message.Get("output_config.effort")
		if value.Type == ojson.String {
			if normalized := strings.ToLower(strings.TrimSpace(value.String())); normalized != "" {
				effort = normalized
			}
		}
		return true
	})
	return effort
}

func extractClaudeEffortDirectiveConfig(body []byte) ThinkingConfig {
	switch effort := ClaudeEffortDirective(body); effort {
	case "":
		return ThinkingConfig{}
	case "none":
		return ThinkingConfig{Mode: ModeNone, Budget: 0}
	case "auto":
		return ThinkingConfig{Mode: ModeAuto, Budget: -1}
	default:
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(effort)}
	}
}
