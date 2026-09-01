package helps

import "github.com/tidwall/gjson"

// ResponsesInputHasItemType reports whether any Responses input item has the given type.
func ResponsesInputHasItemType(body []byte, itemType string) bool {
	if len(body) == 0 || itemType == "" {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == itemType {
			return true
		}
	}
	return false
}

// ResponsesHasCompactionTrigger reports whether any payload contains a remote
// compaction v2 trigger item.
func ResponsesHasCompactionTrigger(payloads ...[]byte) bool {
	for _, payload := range payloads {
		if ResponsesInputHasItemType(payload, "compaction_trigger") {
			return true
		}
	}
	return false
}

// ResponsesOutputItemCounts returns how many output items match itemType and
// the total number of output items. It accepts a Responses JSON object, a
// response.completed event, or an SSE data line wrapping either.
func ResponsesOutputItemCounts(payload []byte, itemType string) (typed, total int) {
	output := responsesOutputArray(payload)
	if !output.IsArray() {
		return 0, 0
	}
	items := output.Array()
	total = len(items)
	for _, item := range items {
		if item.Get("type").String() == itemType {
			typed++
		}
	}
	return typed, total
}

func responsesOutputArray(payload []byte) gjson.Result {
	trimmed := payload
	if len(trimmed) >= 5 && string(trimmed[:5]) == "data:" {
		trimmed = trimmed[5:]
	}
	if output := gjson.GetBytes(trimmed, "output"); output.Exists() {
		return output
	}
	return gjson.GetBytes(trimmed, "response.output")
}
