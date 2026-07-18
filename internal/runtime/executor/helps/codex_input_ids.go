package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexInputItemIDLimit = 64

// SanitizeCodexInputItemIDs deterministically shortens overlong Responses input item IDs.
func SanitizeCodexInputItemIDs(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}

	items := input.Array()
	occupied := make(map[string]struct{}, len(items))
	for _, item := range items {
		itemID := item.Get("id")
		if itemID.Type != gjson.String {
			continue
		}
		id := itemID.String()
		if len([]rune(id)) <= codexInputItemIDLimit {
			occupied[id] = struct{}{}
		}
	}

	mapped := make(map[string]string, len(items))
	updated := body
	for index, item := range items {
		itemID := item.Get("id")
		if itemID.Type != gjson.String {
			continue
		}
		id := itemID.String()
		if len([]rune(id)) <= codexInputItemIDLimit {
			continue
		}

		shortened, ok := mapped[id]
		if !ok {
			shortened = shortenCodexInputItemID(id)
			for attempt := 1; ; attempt++ {
				if _, exists := occupied[shortened]; !exists {
					break
				}
				shortened = shortenCodexInputItemIDWithAttempt(id, attempt)
			}
			mapped[id] = shortened
			occupied[shortened] = struct{}{}
		}

		next, errSet := sjson.SetBytes(updated, "input."+strconv.Itoa(index)+".id", shortened)
		if errSet == nil {
			updated = next
		}
	}
	return updated
}

func shortenCodexInputItemID(id string) string {
	return shortenCodexInputItemIDWithAttempt(id, 0)
}

func shortenCodexInputItemIDWithAttempt(id string, attempt int) string {
	runes := []rune(id)
	if len(runes) <= codexInputItemIDLimit {
		return id
	}

	hashInput := id
	if attempt > 0 {
		hashInput += "\x00" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(hashInput))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLength := codexInputItemIDLimit - len(suffix)
	return string(runes[:prefixLength]) + suffix
}
