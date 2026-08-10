package openai

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func normalizeResponsesInputItemIDs(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() || !input.IsArray() {
		return payload
	}

	normalized := payload
	changed := false
	for index, item := range input.Array() {
		suffix, localID := strings.CutPrefix(item.Get("id").String(), "item_")
		if !localID || suffix == "" {
			continue
		}

		var prefix string
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call":
			prefix = "fc_"
		case "message":
			prefix = "msg_"
		default:
			continue
		}

		updated, err := sjson.SetBytes(normalized, fmt.Sprintf("input.%d.id", index), prefix+suffix)
		if err != nil {
			return payload
		}
		normalized = updated
		changed = true
	}
	if !changed {
		return payload
	}
	return normalized
}
