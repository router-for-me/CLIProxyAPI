package common

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// IsGeminiThoughtPart reports whether a Gemini part contains hidden model thought.
func IsGeminiThoughtPart(part gjson.Result) bool {
	return part.Get("thought").Bool()
}

// MergeAdjacentGeminiContents merges consecutive user Content turns.
// Mid-conversation system messages in Claude requests are downgraded to user
// reminder turns. When followed or preceded by other user turns or tool results,
// their parts are merged into a single user turn.
// Consecutive model turns are strictly kept unmerged to avoid shifting part
// indices and breaking cryptographic thought signatures and reasoning replay.
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
