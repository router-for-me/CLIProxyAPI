package common

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// IsGeminiThoughtPart reports whether a Gemini part contains hidden model thought.
func IsGeminiThoughtPart(part gjson.Result) bool {
	return part.Get("thought").Bool()
}

// MergeAdjacentGeminiContents merges consecutive Content turns with role "user".
// Mid-conversation system messages or consecutive user turns from Claude Code
// have their parts combined into a single "user" turn. "model" turns are intentionally
// left unmerged to preserve thoughtSignature alignment and reasoning replay continuity.
func MergeAdjacentGeminiContents(contents [][]byte) [][]byte {
	if len(contents) <= 1 {
		return contents
	}
	merged := make([][]byte, 0, len(contents))
	for _, content := range contents {
		if len(content) == 0 {
			continue
		}
		role := gjson.GetBytes(content, "role").String()
		partsResult := gjson.GetBytes(content, "parts")
		if !partsResult.IsArray() || len(partsResult.Array()) == 0 {
			continue
		}
		if len(merged) > 0 {
			lastIndex := len(merged) - 1
			lastJSON := merged[lastIndex]
			lastRole := gjson.GetBytes(lastJSON, "role").String()
			if lastRole == "user" && role == "user" {
				lastParts := gjson.GetBytes(lastJSON, "parts").Array()
				combinedParts := make([][]byte, 0, len(lastParts)+len(partsResult.Array()))
				for _, p := range lastParts {
					combinedParts = append(combinedParts, []byte(p.Raw))
				}
				for _, p := range partsResult.Array() {
					combinedParts = append(combinedParts, []byte(p.Raw))
				}
				updated, err := sjson.SetRawBytes(lastJSON, "parts", JoinRawArray(combinedParts))
				if err == nil {
					merged[lastIndex] = updated
					continue
				}
			}
		}
		merged = append(merged, content)
	}
	return merged
}
