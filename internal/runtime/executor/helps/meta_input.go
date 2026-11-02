package helps

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// DropMetaDanglingReasoning removes reasoning items that reach the next user,
// system or developer message without an assistant message or function_call
// after them. Meta rejects that order with HTTP 400 ("A reasoning item placed
// directly before a user ... message"), and a Claude history produces it from
// an assistant turn holding only a thinking block, for example after an
// interrupted or max_tokens-truncated response.
//
// One reverse pass decides every item from what follows it, so the cost stays
// linear however the history is shaped.
func DropMetaDanglingReasoning(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	items := input.Array()
	var drop []bool
	// dangling: a non-assistant message comes before any assistant message or
	// function_call. Trailing reasoning (nothing after it) is kept.
	dangling := false
	for i := len(items) - 1; i >= 0; i-- {
		itemType, role := metaInputItemKind(items[i])
		switch {
		case itemType == "function_call", itemType == "message" && role == "assistant":
			dangling = false
		case itemType == "message":
			dangling = true
		case itemType == "reasoning" && dangling:
			if drop == nil {
				drop = make([]bool, len(items))
			}
			drop[i] = true
		}
	}
	if drop == nil {
		return body
	}
	return rebuildMetaInput(body, input, items, drop, nil)
}

// TagMetaCommentaryPhase marks assistant text that precedes a function_call in
// the same assistant turn with phase "commentary", as Meta labels a preamble it
// emits before a tool call. Meta asks clients to replay that phase: dropping it
// can degrade quality, and text before a function_call is otherwise read as a
// final answer. Messages that already carry a phase are left alone.
//
// Like DropMetaDanglingReasoning, one reverse pass decides every message, and
// the input array is rebuilt once.
func TagMetaCommentaryPhase(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	items := input.Array()
	var tag []bool
	// toolFollows: a function_call comes later in the same assistant turn.
	// Reasoning and assistant messages keep the turn going; anything else ends it.
	toolFollows := false
	for i := len(items) - 1; i >= 0; i-- {
		itemType, role := metaInputItemKind(items[i])
		switch {
		case itemType == "function_call":
			toolFollows = true
		case itemType == "reasoning":
		case itemType == "message" && role == "assistant":
			if toolFollows && !items[i].Get("phase").Exists() {
				if tag == nil {
					tag = make([]bool, len(items))
				}
				tag[i] = true
			}
		default:
			toolFollows = false
		}
	}
	if tag == nil {
		return body
	}
	return rebuildMetaInput(body, input, items, nil, tag)
}

// rebuildMetaInput writes the input array back once, leaving out items marked
// in drop and setting phase "commentary" on items marked in tag. On any error
// the body is returned unchanged.
func rebuildMetaInput(body []byte, input gjson.Result, items []gjson.Result, drop, tag []bool) []byte {
	rebuilt := make([]byte, 0, len(input.Raw)+32)
	rebuilt = append(rebuilt, '[')
	written := 0
	for i, item := range items {
		if drop != nil && drop[i] {
			continue
		}
		raw := item.Raw
		if tag != nil && tag[i] {
			tagged, err := sjson.Set(raw, "phase", "commentary")
			if err != nil {
				return body
			}
			raw = tagged
		}
		if written > 0 {
			rebuilt = append(rebuilt, ',')
		}
		rebuilt = append(rebuilt, raw...)
		written++
	}
	rebuilt = append(rebuilt, ']')
	updated, err := sjson.SetRawBytes(body, "input", rebuilt)
	if err != nil {
		return body
	}
	return updated
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
