package helps

import "github.com/tidwall/gjson"

// roleReplace records the byte index of a "developer" role value in the raw JSON.
type roleReplace struct{ idx int }

// NormalizeDeveloperRole converts any "developer" message role to "system"
// in the given JSON payload's "messages" array. This is needed for OpenAI-compatible
// providers (e.g. DeepSeek) that don't recognize the "developer" role introduced
// by the OpenAI Chat Completions API.
//
// The conversion uses gjson to locate role values, then performs a single-pass
// byte copy with replacements. This avoids per-message allocations and ensures
// one output buffer regardless of the number of developer messages.
func NormalizeDeveloperRole(payload []byte) []byte {
	messagesResult := gjson.GetBytes(payload, "messages")
	if !messagesResult.IsArray() {
		return payload
	}

	// Collect byte positions of "developer" role value opening quotes.
	// gjson.Result.Index points to the first byte of the value, which for a
	// string is the opening '"'.
	var replacements []roleReplace
	messagesArr := messagesResult.Array()
	for i := range messagesArr {
		role := messagesArr[i].Get("role")
		if role.String() != "developer" {
			continue
		}
		replacements = append(replacements, roleReplace{idx: role.Index})
	}
	if len(replacements) == 0 {
		return payload
	}

	// Each "developer" (11 bytes including quotes) becomes "system" (8 bytes).
	// Net delta: -3 bytes per replacement.
	const oldMark, newMark = `"developer"`, `"system"`
	const delta = len(newMark) - len(oldMark) // -3
	newSize := len(payload) + delta*len(replacements)

	out := make([]byte, newSize)
	dst := out
	processedUpTo := 0

	for _, r := range replacements {
		// Copy bytes from after previous replacement up to this role value.
		n := copy(dst, payload[processedUpTo:r.idx])
		dst = dst[n:]

		// Write replacement string.
		n = copy(dst, newMark)
		dst = dst[n:]

		// Advance past the original role value.
		processedUpTo = r.idx + len(oldMark)
	}

	// Copy trailing bytes after the last replacement.
	copy(dst, payload[processedUpTo:])

	return out
}
