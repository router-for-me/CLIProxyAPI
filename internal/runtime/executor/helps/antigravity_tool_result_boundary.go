package helps

import (
	"encoding/json"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/sjson"
)

// EnsureAntigravityGeminiToolResultBoundary labels a terminal Argon tool-result
// turn as user-side data. Antigravity can accept a pure functionResponse tail,
// emit thoughts, then fail with INVALID_ARGUMENT before producing an answer.
// A role change or empty text alone does not establish the turn boundary. The
// existing function names provide a nonempty label without inventing a user
// instruction, duplicating tool output, or discarding signatures.
func EnsureAntigravityGeminiToolResultBoundary(model string, payload []byte, path string) []byte {
	model = strings.ToLower(model)
	if model != "gemini-4-argon" && !strings.HasPrefix(model, "gemini-4-argon-") {
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
	var labels []string
	var parts [][]byte
	for _, part := range last.Get("parts").Array() {
		if response := part.Get("functionResponse"); response.Exists() {
			name := response.Get("name").String()
			if name == "" {
				return payload
			}
			labels = append(labels, name)
		} else if text := part.Get("text"); !text.Exists() || strings.TrimSpace(text.String()) != "" {
			return payload
		}
		parts = append(parts, []byte(part.Raw))
	}
	if len(labels) == 0 {
		return payload
	}
	label, err := json.Marshal(map[string]string{"text": strings.Join(labels, "\n")})
	if err != nil {
		return payload
	}
	parts = append(parts, label)
	turn, err := sjson.SetBytes([]byte(last.Raw), "role", "user")
	if err != nil {
		return payload
	}
	turn, err = sjson.SetRawBytes(turn, "parts", translatorcommon.JoinRawArray(parts))
	if err != nil {
		return payload
	}
	items := make([][]byte, 0, len(turns))
	for _, content := range turns[:len(turns)-1] {
		items = append(items, []byte(content.Raw))
	}
	items = append(items, turn)
	out, err := sjson.SetRawBytes(payload, path, translatorcommon.JoinRawArray(items))
	if err != nil {
		return payload
	}
	return out
}
