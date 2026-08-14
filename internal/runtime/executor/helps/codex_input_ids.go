package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexInputItemIDLimit                 = 64
	codexCallIDLimit                      = 64
	codexMessageItemIDPrefix              = "msg"
	codexReasoningItemIDPrefix            = "rs"
	codexFunctionCallItemIDPrefix         = "fc"
	codexCustomToolCallItemIDPrefix       = "ctc"
	codexCustomToolCallOutputItemIDPrefix = "ctco"

	codexInputItemIDOccupied  uint8 = 1 << 0
	codexInputItemIDPreserved uint8 = 1 << 1

	codexCallIDFunctionCall     uint8 = 1 << 1
	codexCallIDFunctionOutput   uint8 = 1 << 2
	codexCallIDCustomToolCall   uint8 = 1 << 3
	codexCallIDCustomToolOutput uint8 = 1 << 4
)

// SanitizeCodexInputItemIDs normalizes supported input item IDs for Codex, removes encrypted
// reasoning items whose IDs exceed the Codex limit, and deterministically shortens
// other overlong input item IDs and call IDs.
func SanitizeCodexInputItemIDs(body []byte) []byte {
	input := util.GetGJSONBytesNoCopy(body, "input")
	if !input.IsArray() {
		return body
	}

	items := input.Array()
	idStates := make(map[string]uint8, len(items))
	var callIDStates map[string]uint8
	var overlongCallIDRoles map[string]uint8
	for _, item := range items {
		if shouldDropCodexEncryptedReasoningItem(item) {
			continue
		}
		callID := item.Get("call_id")
		if callID.Type == gjson.String {
			value := callID.String()
			if utf8.RuneCountInString(value) <= codexCallIDLimit {
				if callIDStates == nil {
					callIDStates = make(map[string]uint8)
				}
				callIDStates[value] |= codexInputItemIDOccupied | codexCallIDRole(item)
			} else {
				if overlongCallIDRoles == nil {
					overlongCallIDRoles = make(map[string]uint8)
				}
				overlongCallIDRoles[value] |= codexCallIDRole(item)
			}
		}

		itemID := item.Get("id")
		if itemID.Type != gjson.String {
			continue
		}
		originalID := itemID.String()
		id := normalizeCodexInputItemID(item, originalID)
		state := idStates[id]
		if id == originalID {
			state |= codexInputItemIDPreserved
		}
		if len([]rune(id)) <= codexInputItemIDLimit {
			state |= codexInputItemIDOccupied
		}
		if state != 0 {
			idStates[id] = state
		}
	}

	var mapped map[string]string
	var collisionMapped map[string]string
	var callIDMapped map[string]string
	rebuilt := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		if shouldDropCodexEncryptedReasoningItem(item) {
			changed = true
			continue
		}

		raw := item.Raw
		itemID := item.Get("id")
		if itemID.Type == gjson.String {
			originalID := itemID.String()
			id := normalizeCodexInputItemID(item, originalID)
			if id != originalID && idStates[id]&codexInputItemIDPreserved != 0 {
				collisionID, ok := collisionMapped[id]
				if !ok {
					for attempt := 0; ; attempt++ {
						collisionID = codexInputItemIDWithHashSuffix(id, attempt)
						if idStates[collisionID]&codexInputItemIDOccupied != 0 {
							continue
						}
						if collisionMapped == nil {
							collisionMapped = make(map[string]string)
						}
						collisionMapped[id] = collisionID
						idStates[collisionID] |= codexInputItemIDOccupied
						break
					}
				}
				id = collisionID
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
				next, errSet := sjson.SetBytes([]byte(raw), "id", id)
				if errSet == nil {
					raw = string(next)
					changed = true
				}
			}
		}

		callID := item.Get("call_id")
		if callID.Type == gjson.String && utf8.RuneCountInString(callID.String()) > codexCallIDLimit {
			originalCallID := callID.String()
			shortened, ok := callIDMapped[originalCallID]
			if !ok {
				for attempt := 0; ; attempt++ {
					shortened = shortenCodexCallIDWithAttempt(originalCallID, attempt)
					shortenedState := callIDStates[shortened]
					if shortenedState&codexInputItemIDOccupied == 0 ||
						(attempt == 0 && codexCallIDRolesArePair(overlongCallIDRoles[originalCallID], shortenedState)) {
						break
					}
				}
				if callIDMapped == nil {
					callIDMapped = make(map[string]string)
				}
				if callIDStates == nil {
					callIDStates = make(map[string]uint8)
				}
				callIDMapped[originalCallID] = shortened
				callIDStates[shortened] |= codexInputItemIDOccupied
			}

			next, errSet := sjson.SetBytes([]byte(raw), "call_id", shortened)
			if errSet == nil {
				raw = string(next)
				changed = true
			}
		}
		rebuilt = append(rebuilt, raw)
	}
	if !changed {
		return body
	}

	updated, errSet := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if errSet != nil {
		return body
	}
	return updated
}

func codexCallIDRole(item gjson.Result) uint8 {
	switch item.Get("type").String() {
	case "function_call":
		return codexCallIDFunctionCall
	case "function_call_output":
		return codexCallIDFunctionOutput
	case "custom_tool_call":
		return codexCallIDCustomToolCall
	case "custom_tool_call_output":
		return codexCallIDCustomToolOutput
	default:
		return 0
	}
}

func codexCallIDRolesArePair(overlongRole, shortenedState uint8) bool {
	shortenedRole := shortenedState &^ codexInputItemIDOccupied
	return (overlongRole == codexCallIDFunctionCall && shortenedRole == codexCallIDFunctionOutput) ||
		(overlongRole == codexCallIDFunctionOutput && shortenedRole == codexCallIDFunctionCall) ||
		(overlongRole == codexCallIDCustomToolCall && shortenedRole == codexCallIDCustomToolOutput) ||
		(overlongRole == codexCallIDCustomToolOutput && shortenedRole == codexCallIDCustomToolCall)
}

func normalizeCodexInputItemID(item gjson.Result, id string) string {
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
	default:
		return id
	}
	if id == "" || strings.HasPrefix(id, prefix) {
		return id
	}
	return prefix + "_" + id
}

func shouldDropCodexEncryptedReasoningItem(item gjson.Result) bool {
	if item.Get("type").String() != "reasoning" {
		return false
	}
	itemID := item.Get("id")
	if itemID.Type != gjson.String || len([]rune(itemID.String())) <= codexInputItemIDLimit {
		return false
	}
	encryptedContent := item.Get("encrypted_content")
	return encryptedContent.Type == gjson.String && encryptedContent.String() != ""
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

func shortenCodexCallIDWithAttempt(callID string, attempt int) string {
	runes := []rune(callID)
	if len(runes) <= codexCallIDLimit {
		return callID
	}
	return codexInputItemIDWithHashSuffixRunes(callID, runes, attempt)
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
	return string(runes[:prefixLength]) + suffix
}
