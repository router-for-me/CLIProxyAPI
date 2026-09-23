package common

import (
	"strings"

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
				combinedParts = ReorderGeminiUserParts(combinedParts)
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

// ContentHasGeminiFunctionResponse reports whether a Gemini content turn contains any functionResponse part.
func ContentHasGeminiFunctionResponse(content []byte) bool {
	hasFR := false
	gjson.GetBytes(content, "parts").ForEach(func(_, part gjson.Result) bool {
		if part.Get("functionResponse").Exists() || part.Get("function_response").Exists() {
			hasFR = true
			return false
		}
		return true
	})
	return hasFR
}

// ReorderGeminiUserParts reorders parts within a Gemini user turn so that
// text parts (such as prompt text and system reminders) precede functionResponse
// parts. This resolves upstream provider validation failures (such as Google Cloud
// Vertex AI returning 400 "Requests ending with a model turn are not supported" when
// functionResponse is followed by text in the same turn).
func ReorderGeminiUserParts(parts [][]byte) [][]byte {
	hasFR := false
	hasTrailingText := false
	for _, p := range parts {
		isFR := gjson.GetBytes(p, "functionResponse").Exists() || gjson.GetBytes(p, "function_response").Exists()
		if isFR {
			hasFR = true
		} else if hasFR && gjson.GetBytes(p, "text").Exists() {
			hasTrailingText = true
			break
		}
	}
	if !hasFR || !hasTrailingText {
		return parts
	}

	promptParts := make([][]byte, 0, len(parts))
	toolParts := make([][]byte, 0, len(parts))
	for _, p := range parts {
		if gjson.GetBytes(p, "text").Exists() {
			promptParts = append(promptParts, p)
		} else {
			toolParts = append(toolParts, p)
		}
	}
	return append(promptParts, toolParts...)
}

// MergeAdjacentGeminiUserContents merges consecutive user Content turns,
// but leaves turns containing functionResponse unmerged to preserve tool-call/response boundaries.
func MergeAdjacentGeminiUserContents(contents [][]byte) [][]byte {
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
		if !partsResult.IsArray() || partsResult.Raw == "[]" || !partsResult.Get("0").Exists() {
			continue
		}
		if len(merged) > 0 {
			lastIndex := len(merged) - 1
			lastJSON := merged[lastIndex]
			lastRole := gjson.GetBytes(lastJSON, "role").String()
			if lastRole == "user" && role == "user" && !ContentHasGeminiFunctionResponse(lastJSON) && !ContentHasGeminiFunctionResponse(content) {
				lastParts := gjson.GetBytes(lastJSON, "parts").Array()
				currentParts := partsResult.Array()
				combinedParts := make([][]byte, 0, len(lastParts)+len(currentParts))
				for _, p := range lastParts {
					combinedParts = append(combinedParts, []byte(p.Raw))
				}
				for _, p := range currentParts {
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

// ContainsJSONRef reports whether value (recursively) contains a string-valued "$ref" property.
func ContainsJSONRef(value gjson.Result) bool {
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	found := false
	value.ForEach(func(key, child gjson.Result) bool {
		if value.IsObject() && key.String() == "$ref" && child.Type == gjson.String {
			found = true
			return false
		}
		if ContainsJSONRef(child) {
			found = true
			return false
		}
		return true
	})
	return found
}

// SetGeminiFunctionResponseResult sets the Gemini functionResponse result or response field.
// If the result contains a JSON Schema or OpenAPI "$ref" property with a string value, it is
// preserved as opaque JSON text with sjson.SetBytes. This prevents Gemini/Vertex AI from
// interpreting "$ref" as a reference to a media part in function_response.parts and rejecting
// the request with HTTP 400.
// If path points to "functionResponse.response" or "response", a stringified result is placed
// under the ".result" child object to satisfy Gemini's requirement that "response" is an object.
func SetGeminiFunctionResponseResult(part []byte, path string, result gjson.Result) []byte {
	if !result.Exists() {
		part, _ = sjson.SetBytes(part, path, "")
		return part
	}
	if ContainsJSONRef(result) {
		targetPath := path
		if strings.HasSuffix(path, "response") {
			targetPath = path + ".result"
		}
		part, _ = sjson.SetBytes(part, targetPath, result.Raw)
		return part
	}
	part, _ = sjson.SetRawBytes(part, path, []byte(result.Raw))
	return part
}

// SetGeminiFunctionResponseRaw is a convenience wrapper around SetGeminiFunctionResponseResult
// for raw JSON strings.
func SetGeminiFunctionResponseRaw(part []byte, path string, rawJSON string) []byte {
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" {
		part, _ = sjson.SetBytes(part, path, "")
		return part
	}
	return SetGeminiFunctionResponseResult(part, path, gjson.Parse(trimmed))
}
