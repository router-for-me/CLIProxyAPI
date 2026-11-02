package helps

import "github.com/tidwall/gjson"

const (
	developerRoleJSON = `"developer"`
	systemRoleJSON    = `"system"`
)

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

	replacements := make([]int, 0, 2)
	messages.ForEach(func(_, message gjson.Result) bool {
		role := message.Get("role")
		if role.Exists() && role.String() == "developer" && role.Index >= 0 {
			replacements = append(replacements, role.Index)
		}
		return true
	})
	if len(replacements) == 0 {
		return payload
	}

	out := make([]byte, 0, len(payload)+(len(systemRoleJSON)-len(developerRoleJSON))*len(replacements))
	last := 0
	for _, index := range replacements {
		if index < last || index+len(developerRoleJSON) > len(payload) || string(payload[index:index+len(developerRoleJSON)]) != developerRoleJSON {
			return payload
		}
		out = append(out, payload[last:index]...)
		out = append(out, systemRoleJSON...)
		last = index + len(developerRoleJSON)
	}
	out = append(out, payload[last:]...)
	return out
}
