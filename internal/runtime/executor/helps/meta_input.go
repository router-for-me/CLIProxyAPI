package helps

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// DropMetaUnreplayableReasoning removes replayed reasoning items that Meta
// cannot use or would reject.
//
// Meta restores chain of thought only from encrypted_content, so a reasoning
// item with neither encrypted_content nor an id carries nothing to replay.
// Meta also rejects, with HTTP 400, a reasoning item that is not followed by an
// assistant message or a function_call before the next user, system or
// developer message, which a thinking-only assistant turn would produce.
func DropMetaUnreplayableReasoning(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	items := input.Array()
	var drop []bool
	for i, item := range items {
		if itemType, _ := metaInputItemKind(item); itemType != "reasoning" || metaReasoningReplayable(items, i) {
			continue
		}
		if drop == nil {
			drop = make([]bool, len(items))
		}
		drop[i] = true
	}
	if drop == nil {
		return body
	}

	rebuilt := make([]byte, 0, len(input.Raw))
	rebuilt = append(rebuilt, '[')
	written := 0
	for i, item := range items {
		if drop[i] {
			continue
		}
		if written > 0 {
			rebuilt = append(rebuilt, ',')
		}
		rebuilt = append(rebuilt, item.Raw...)
		written++
	}
	rebuilt = append(rebuilt, ']')
	updated, err := sjson.SetRawBytes(body, "input", rebuilt)
	if err != nil {
		return body
	}
	return updated
}

func metaReasoningReplayable(items []gjson.Result, index int) bool {
	item := items[index]
	encryptedContent := item.Get("encrypted_content")
	hasEncryptedContent := encryptedContent.Type == gjson.String && encryptedContent.String() != ""
	if !hasEncryptedContent && strings.TrimSpace(item.Get("id").String()) == "" {
		return false
	}
	for _, next := range items[index+1:] {
		itemType, role := metaInputItemKind(next)
		switch {
		case itemType == "function_call":
			return true
		case itemType == "message" && role == "assistant":
			return true
		case itemType == "message":
			return false
		}
	}
	return true
}

// metaInputItemKind returns an input item's type and role. A Responses input
// item with a role and no type is a message.
func metaInputItemKind(item gjson.Result) (itemType, role string) {
	itemType = strings.TrimSpace(item.Get("type").String())
	role = strings.TrimSpace(item.Get("role").String())
	if itemType == "" && role != "" {
		itemType = "message"
	}
	return itemType, role
}
