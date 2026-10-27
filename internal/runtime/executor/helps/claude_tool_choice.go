package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeClaudeToolChoice downgrades forced tool use on Claude 5.5 to auto.
// Use the routed upstream model, not the payload model, and run before thinking
// preparation so forced-mode cleanup cannot discard thinking or signed history.
// Delegated providers keep their own tool-choice contract. User payload rules
// must run afterwards and remain authoritative.
func NormalizeClaudeToolChoice(provider, model string, payload []byte) []byte {
	if provider != "claude" {
		return payload
	}
	model = strings.ToLower(thinking.ParseSuffix(strings.TrimSpace(model)).ModelName)
	if !claude55Model.MatchString(model) {
		return payload
	}
	switch gjson.GetBytes(payload, "tool_choice.type").String() {
	case "any", "tool":
		payload, _ = sjson.SetBytes(payload, "tool_choice.type", "auto")
		payload, _ = sjson.DeleteBytes(payload, "tool_choice.name")
	}
	return payload
}
