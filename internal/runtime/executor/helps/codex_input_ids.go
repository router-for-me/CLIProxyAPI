package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

const (
	codexInputItemIDLimit                 = 64
	codexMessageItemIDPrefix              = "msg"
	codexReasoningItemIDPrefix            = "rs"
	codexFunctionCallItemIDPrefix         = "fc"
	codexCustomToolCallItemIDPrefix       = "ctc"
	codexCustomToolCallOutputItemIDPrefix = "ctco"
	codexToolSearchCallItemIDPrefix       = "tsc"

	codexInputItemIDOccupied  uint8 = 1 << 0
	codexInputItemIDPreserved uint8 = 1 << 1
	codexInputItemIDSeen      uint8 = 1 << 2
	codexInputItemIDDuplicate uint8 = 1 << 3
)

// SanitizeCodexInputItemIDs normalizes supported input item IDs for Codex, removes encrypted
// reasoning items whose IDs exceed the Codex limit, and deterministically shortens
// other overlong input item IDs.
func SanitizeCodexInputItemIDs(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	doc := ojson.NewDoc(body)
	if !SanitizeCodexInputItemIDsDoc(doc) {
		return body
	}
	return doc.Bytes()
}

// SanitizeCodexInputItemIDsDoc normalizes supported input item IDs for Codex in place on doc,
// removes encrypted reasoning items whose IDs exceed the Codex limit, and deterministically
// shortens other overlong input item IDs.
func SanitizeCodexInputItemIDsDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}

	items := input.Array()
	idStates := make(map[string]uint8, len(items))
	hasDropped := false
	for _, item := range items {
		if shouldDropCodexEncryptedReasoningItem(item) {
			hasDropped = true
			continue
		}
		itemID := item.Get("id")
		if itemID.Type != ojson.String {
			continue
		}
		originalID := itemID.String()
		id := normalizeCodexInputItemID(item, originalID)
		state := idStates[id]
		if state&codexInputItemIDSeen != 0 {
			state |= codexInputItemIDDuplicate
		}
		state |= codexInputItemIDSeen
		if id == originalID {
			state |= codexInputItemIDPreserved
		}
		if len([]rune(id)) <= codexInputItemIDLimit {
			state |= codexInputItemIDOccupied
		}
		idStates[id] = state
	}

	var mapped map[string]string
	var collisionMapped map[string]string
	var dropped map[int]bool
	if hasDropped {
		dropped = make(map[int]bool)
	}
	changed := false
	for idx, item := range items {
		if shouldDropCodexEncryptedReasoningItem(item) {
			dropped[idx] = true
			changed = true
			continue
		}

		itemID := item.Get("id")
		if itemID.Type == ojson.String {
			originalID := itemID.String()
			id := normalizeCodexInputItemID(item, originalID)
			if id != originalID && idStates[id]&(codexInputItemIDPreserved|codexInputItemIDDuplicate) != 0 {
				collisionKey := id + "\x00" + originalID
				if collisionID, ok := collisionMapped[collisionKey]; ok {
					id = collisionID
				} else if idStates[id]&codexInputItemIDPreserved == 0 {
					if collisionMapped == nil {
						collisionMapped = make(map[string]string)
					}
					collisionMapped[collisionKey] = id
					idStates[id] |= codexInputItemIDPreserved
				} else {
					rawNormalizedID := normalizeCodexInputItemIDPrefix(item, originalID)
					var collisionID string
					for attempt := 0; ; attempt++ {
						collisionID = codexInputItemIDWithHashSuffix(rawNormalizedID, attempt)
						if idStates[collisionID]&codexInputItemIDOccupied != 0 {
							continue
						}
						if collisionMapped == nil {
							collisionMapped = make(map[string]string)
						}
						collisionMapped[collisionKey] = collisionID
						idStates[collisionID] |= codexInputItemIDOccupied | codexInputItemIDPreserved
						break
					}
					id = collisionID
				}
			}
			if len([]rune(id)) > codexInputItemIDLimit {
				shortened, ok := mapped[id]
				if !ok {
					shortened = shortenCodexInputItemID(id)
					for attempt := 1; ; attempt++ {
						if idStates[shortened]&codexInputItemIDOccupied == 0 {
							break
						}
						shortened = shortenCodexInputItemIDWithAttempt(id, attempt)
					}
					if mapped == nil {
						mapped = make(map[string]string)
					}
					mapped[id] = shortened
					idStates[shortened] |= codexInputItemIDOccupied
				}
				id = shortened
			}

			if id != originalID {
				if errSet := doc.Set(fmt.Sprintf("input.%d.id", idx), id); errSet == nil {
					changed = true
				}
			}
		}
	}
	if !changed {
		return false
	}
	if hasDropped {
		updatedItems := doc.Get("input").Array()
		rebuilt := make([][]byte, 0, len(updatedItems)-len(dropped))
		for idx, item := range updatedItems {
			if !dropped[idx] {
				rebuilt = append(rebuilt, []byte(item.Raw))
			}
		}
		return doc.SetRawBytes("input", JoinRawJSONArray(rebuilt)) == nil
	}
	return true
}

func normalizeCodexInputItemID(item ojson.Result, id string) string {
	normalized := normalizeCodexInputItemIDPrefix(item, id)
	if len([]rune(normalized)) > codexInputItemIDLimit {
		return normalized
	}
	return sanitizeCodexInputItemIDChars(normalized)
}

func normalizeCodexInputItemIDPrefix(item ojson.Result, id string) string {
	var prefix string
	switch item.Get("type").String() {
	case "message":
		prefix = codexMessageItemIDPrefix
	case "reasoning":
		prefix = codexReasoningItemIDPrefix
	case "function_call":
		prefix = codexFunctionCallItemIDPrefix
	case "custom_tool_call":
		prefix = codexCustomToolCallItemIDPrefix
	case "custom_tool_call_output":
		prefix = codexCustomToolCallOutputItemIDPrefix
	case "tool_search_call":
		if after, ok := strings.CutPrefix(id, "ts_"); ok && after != "" {
			return codexToolSearchCallItemIDPrefix + "_" + after
		}
		prefix = codexToolSearchCallItemIDPrefix
	default:
		return id
	}
	if id == "" || strings.HasPrefix(id, prefix) {
		return id
	}
	return prefix + "_" + id
}

func sanitizeCodexInputItemIDChars(id string) string {
	if id == "" || isPortableToolCallID(id) {
		return id
	}
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func shouldDropCodexEncryptedReasoningItem(item ojson.Result) bool {
	if item.Get("type").String() != "reasoning" {
		return false
	}
	itemID := item.Get("id")
	if itemID.Type != ojson.String || len([]rune(itemID.String())) <= codexInputItemIDLimit {
		return false
	}
	encryptedContent := item.Get("encrypted_content")
	return encryptedContent.Type == ojson.String && encryptedContent.String() != ""
}

func shortenCodexInputItemID(id string) string {
	return shortenCodexInputItemIDWithAttempt(id, 0)
}

func shortenCodexInputItemIDWithAttempt(id string, attempt int) string {
	runes := []rune(id)
	if len(runes) <= codexInputItemIDLimit {
		return id
	}
	return codexInputItemIDWithHashSuffixRunes(id, runes, attempt)
}

func codexInputItemIDWithHashSuffix(id string, attempt int) string {
	return codexInputItemIDWithHashSuffixRunes(id, []rune(id), attempt)
}

func codexInputItemIDWithHashSuffixRunes(id string, runes []rune, attempt int) string {
	hashInput := id
	if attempt > 0 {
		hashInput += "\x00" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(hashInput))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLength := codexInputItemIDLimit - len(suffix)
	if len(runes) < prefixLength {
		prefixLength = len(runes)
	}
	return sanitizeCodexInputItemIDChars(string(runes[:prefixLength])) + suffix
}
