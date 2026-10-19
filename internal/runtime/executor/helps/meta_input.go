package helps

import (
	"strconv"
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
func DropMetaDanglingReasoning(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	items := input.Array()
	var drop []bool
	for i, item := range items {
		if itemType, _ := metaInputItemKind(item); itemType != "reasoning" || !metaReasoningDangles(items[i+1:]) {
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

// metaReasoningDangles reports whether a non-assistant message comes before
// any assistant message or function_call.
func metaReasoningDangles(rest []gjson.Result) bool {
	for _, next := range rest {
		itemType, role := metaInputItemKind(next)
		switch {
		case itemType == "function_call", itemType == "message" && role == "assistant":
			return false
		case itemType == "message":
			return true
		}
	}
	return false
}

// TagMetaCommentaryPhase marks assistant text that precedes a function_call in
// the same assistant turn with phase "commentary", as Meta labels a preamble it
// emits before a tool call. Meta asks clients to replay that phase: dropping it
// can degrade quality, and text before a function_call is otherwise read as a
// final answer. Messages that already carry a phase are left alone.
func TagMetaCommentaryPhase(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	items := input.Array()
	for i, item := range items {
		itemType, role := metaInputItemKind(item)
		if itemType != "message" || role != "assistant" || item.Get("phase").Exists() {
			continue
		}
		if !metaAssistantTurnCallsTool(items[i+1:]) {
			continue
		}
		if updated, err := sjson.SetBytes(body, "input."+strconv.Itoa(i)+".phase", "commentary"); err == nil {
			body = updated
		}
	}
	return body
}

// metaAssistantTurnCallsTool reports whether a function_call follows before
// the assistant turn ends.
func metaAssistantTurnCallsTool(rest []gjson.Result) bool {
	for _, next := range rest {
		itemType, role := metaInputItemKind(next)
		switch {
		case itemType == "function_call":
			return true
		case itemType == "reasoning", itemType == "message" && role == "assistant":
			continue
		default:
			return false
		}
	}
	return false
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
