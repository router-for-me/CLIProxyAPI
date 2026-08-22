package helps

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ContentHasThinking reports whether a content array carries a thinking or
// redacted_thinking part.
func ContentHasThinking(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		switch strings.TrimSpace(part.Get("type").String()) {
		case "thinking", "redacted_thinking":
			return true
		}
	}
	return false
}

// ThinkingMatchesCachedIgnoringSignature checks that every thinking or
// redacted_thinking part in current matches the corresponding cached part after
// removing signature fields. Non-thinking parts are assumed to be equal by the
// caller (NonThinkingContentParts/CanonicalPartsEqual).
func ThinkingMatchesCachedIgnoringSignature(current, cached gjson.Result) bool {
	if !current.IsArray() || !cached.IsArray() {
		return false
	}
	currentParts := current.Array()
	cachedParts := cached.Array()
	if len(currentParts) != len(cachedParts) {
		return false
	}
	for i, curPart := range currentParts {
		cachedPart := cachedParts[i]
		curType := strings.TrimSpace(curPart.Get("type").String())
		cachedType := strings.TrimSpace(cachedPart.Get("type").String())
		if curType != cachedType {
			return false
		}
		switch curType {
		case "thinking", "redacted_thinking":
			curClean := ThinkingPartWithoutSignature(curPart)
			cachedClean := ThinkingPartWithoutSignature(cachedPart)
			curCanon, ok1 := CanonicalJSON([]byte(curClean))
			cachedCanon, ok2 := CanonicalJSON([]byte(cachedClean))
			if !ok1 || !ok2 || !bytes.Equal(curCanon, cachedCanon) {
				return false
			}
		}
	}
	return true
}

// ThinkingPartWithoutSignature returns a thinking/redacted_thinking part with
// signature fields removed so two parts can be compared ignoring provenance.
func ThinkingPartWithoutSignature(part gjson.Result) string {
	updated := part.Raw
	for _, path := range []string{"signature", "thoughtSignature", "thought_signature", "extra_content.google.thought_signature"} {
		if gjson.Get(updated, path).Exists() {
			updated, _ = sjson.Delete(updated, path)
		}
	}
	return updated
}

// NonThinkingContentParts extracts the canonical non-thinking content parts.
// It returns false when a part cannot be canonicalized or a tool_use part
// is missing an id.
func NonThinkingContentParts(content gjson.Result) ([][]byte, bool) {
	if !content.IsArray() {
		return nil, false
	}
	parts := make([][]byte, 0, len(content.Array()))
	for _, part := range content.Array() {
		switch strings.TrimSpace(part.Get("type").String()) {
		case "thinking", "redacted_thinking":
			continue
		case "tool_use":
			if strings.TrimSpace(part.Get("id").String()) == "" {
				return nil, false
			}
		}
		canonical, ok := CanonicalJSON([]byte(part.Raw))
		if !ok {
			return nil, false
		}
		parts = append(parts, canonical)
	}
	if len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

// CanonicalPartsEqual reports whether two canonical part slices are equal.
func CanonicalPartsEqual(left, right [][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !bytes.Equal(left[i], right[i]) {
			return false
		}
	}
	return true
}

// JSONEqual reports whether two JSON values are equal after canonicalization.
func JSONEqual(left, right []byte) bool {
	canonicalLeft, leftOK := CanonicalJSON(left)
	canonicalRight, rightOK := CanonicalJSON(right)
	return leftOK && rightOK && bytes.Equal(canonicalLeft, canonicalRight)
}

// CanonicalJSON returns a compact, key-ordered JSON representation for value
// equality comparison.
func CanonicalJSON(raw []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, false
	}
	canonical, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, false
	}
	return canonical, true
}
