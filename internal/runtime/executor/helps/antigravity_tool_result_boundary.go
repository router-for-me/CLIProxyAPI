package helps

import (
	"encoding/json"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/sjson"
)

func isAntigravityGeminiArgonModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	normalized = strings.TrimPrefix(normalized, "models/")
	return normalized == "gemini-4-argon" || strings.HasPrefix(normalized, "gemini-4-argon-")
}

// EnsureAntigravityGeminiToolResultBoundary normalizes a terminal pure
// functionResponse turn for Antigravity Gemini 4 Argon so it uses role "user"
// and carries a non-empty text part naming the answered function(s).
//
// On long multi-turn conversations, Antigravity's gemini-4-argon endpoint can
// accept a request ending in a model-role pure functionResponse turn, stream
// thoughts, and then abort before completion with 400 INVALID_ARGUMENT
// ("Request contains an invalid argument."). Changing the role alone or adding
// an empty text part is insufficient; labeling the terminal turn with the
// existing functionResponse name(s) establishes the user-side boundary without
// inventing instructions, duplicating tool output, or mutating historical turns.
func EnsureAntigravityGeminiToolResultBoundary(model string, payload []byte, path string) []byte {
	if !isAntigravityGeminiArgonModel(model) {
		return payload
	}
	contents := util.GetGJSONBytesNoCopy(payload, path)
	if !contents.IsArray() {
		return payload
	}
	turns := contents.Array()
	if len(turns) == 0 {
		return payload
	}
	last := turns[len(turns)-1]
	if contentHasFunctionResponseMedia(last) {
		return payload
	}
	partsResult := last.Get("parts")
	if !partsResult.IsArray() {
		return payload
	}
	partArray := partsResult.Array()
	if len(partArray) == 0 {
		return payload
	}

	labels := make([]string, 0, len(partArray))
	parts := make([][]byte, 0, len(partArray)+1)
	for _, part := range partArray {
		if response := part.Get("functionResponse"); response.Exists() {
			name := strings.TrimSpace(response.Get("name").String())
			if name == "" {
				return payload
			}
			labels = append(labels, name)
		} else if text := part.Get("text"); !text.Exists() || strings.TrimSpace(text.String()) != "" || part.Get("thought").Bool() || part.Get("functionCall").Exists() {
			return payload
		}
		parts = append(parts, []byte(part.Raw))
	}
	if len(labels) == 0 {
		return payload
	}

	labelPart, errMarshal := json.Marshal(map[string]string{"text": strings.Join(labels, "\n")})
	if errMarshal != nil {
		return payload
	}
	parts = append(parts, labelPart)

	updatedLast, errRole := sjson.SetBytes([]byte(last.Raw), "role", "user")
	if errRole != nil {
		return payload
	}
	updatedLast, errParts := sjson.SetRawBytes(updatedLast, "parts", translatorcommon.JoinRawArray(parts))
	if errParts != nil {
		return payload
	}

	items := make([][]byte, 0, len(turns))
	for _, turn := range turns[:len(turns)-1] {
		items = append(items, []byte(turn.Raw))
	}
	items = append(items, updatedLast)
	out, errSet := sjson.SetRawBytes(payload, path, translatorcommon.JoinRawArray(items))
	if errSet != nil {
		return payload
	}
	return out
}
