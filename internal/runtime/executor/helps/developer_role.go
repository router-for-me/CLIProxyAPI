package helps

import "github.com/tidwall/gjson"

const (
	systemRoleJSON = `"system"`
)

type developerRoleReplacement struct {
	index int
	raw   string
}

// NormalizeDeveloperRole converts developer-role messages in a Chat Completions
// payload to system-role messages. Strict OpenAI-compatible providers, including
// DeepSeek's official API, reject the developer role even though modern OpenAI
// APIs support it. System messages preserve the instruction priority that would
// be lost if the messages were demoted to user messages.
func NormalizeDeveloperRole(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}

	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}

	replacements := make([]developerRoleReplacement, 0, 2)
	messages.ForEach(func(_, message gjson.Result) bool {
		role := message.Get("role")
		if role.Exists() && role.String() == "developer" && role.Index >= 0 {
			replacements = append(replacements, developerRoleReplacement{index: role.Index, raw: role.Raw})
		}
		return true
	})
	if len(replacements) == 0 {
		return payload
	}

	delta := 0
	for _, replacement := range replacements {
		delta += len(systemRoleJSON) - len(replacement.raw)
	}
	out := make([]byte, 0, len(payload)+delta)
	last := 0
	for _, replacement := range replacements {
		end := replacement.index + len(replacement.raw)
		if replacement.index < last || end > len(payload) || string(payload[replacement.index:end]) != replacement.raw {
			return payload
		}
		out = append(out, payload[last:replacement.index]...)
		out = append(out, systemRoleJSON...)
		last = end
	}
	out = append(out, payload[last:]...)
	return out
}
