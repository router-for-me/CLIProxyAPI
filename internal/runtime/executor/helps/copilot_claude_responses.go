package helps

import (
	"maps"
	"strconv"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
)

// The shared Responses -> Claude translator uses encrypted_content as a
// Claude signature and falls back to private reasoning text when no summary
// exists. Give it only the public summary on this Copilot projection. Keep
// the untouched event for usage, terminal handling and private replay.
func CopilotVisibleResponsesForClaude(data []byte) []byte {
	root := ojson.ParseBytes(data)
	switch root.Get("type").String() {
	case "response.reasoning_text.delta", "response.reasoning_text.done":
		return nil
	}
	var doc *ojson.Doc
	strip := func(path string, item ojson.Result) {
		if item.Get("type").String() != "reasoning" {
			return
		}
		for _, field := range []string{"encrypted_content", "content"} {
			if !item.Get(field).Exists() {
				continue
			}
			if doc == nil {
				doc = ojson.NewDoc(data)
			}
			_ = doc.Delete(path + "." + field)
		}
	}
	strip("item", root.Get("item"))
	for _, path := range []string{"output", "response.output"} {
		for i, item := range root.Get(path).Array() {
			strip(path+"."+strconv.Itoa(i), item)
		}
	}
	if doc == nil {
		return data
	}
	return doc.Bytes()
}

type copilotClaudeResponseAnchor struct {
	prefix        uuid.UUID
	content, next ojson.Result
}

func copilotClaudeResponseAnchors(claude ojson.Result) (uuid.UUID, []copilotClaudeResponseAnchor) {
	if !claude.Exists() {
		return uuid.Nil, nil
	}
	var anchors []copilotClaudeResponseAnchor
	sourcePrefix := uuid.NewSHA1(uuid.NameSpaceURL, []byte("copilot:claude-responses"))
	sourcePrefix = uuid.NewSHA1(sourcePrefix, []byte(claude.Get("system").Raw))
	msgs := claude.Get("messages").Array()
	for i, msg := range msgs {
		if msg.Get("role").String() == "assistant" {
			a := copilotClaudeResponseAnchor{prefix: sourcePrefix, content: msg.Get("content")}
			if i+1 < len(msgs) {
				a.next = msgs[i+1]
			}
			anchors = append(anchors, a)
		}
		sourcePrefix = uuid.NewSHA1(sourcePrefix, []byte(msg.Raw))
	}
	return sourcePrefix, anchors
}

// Do not splice a stored call into a missing, duplicate, or unrelated batch.
// Check both sides of translation: sanitization or shortening of a result ID
// must not accidentally turn it into a match. Leave explicit reasoning alone.
func copilotClaudeResponseBatchMatches(group, tail []ojson.Result, next ojson.Result) bool {
	calls := make(map[string]bool)
	for _, v := range group {
		switch v.Get("type").String() {
		case "reasoning":
			return false
		case "function_call":
			id := v.Get("call_id").String()
			if id == "" || calls[id] || len(v.Map()) != 4 {
				return false
			}
			calls[id] = true
		}
	}
	if len(calls) == 0 {
		return true
	}
	if next.Get("role").String() != "user" {
		return false
	}
	remaining := maps.Clone(calls)
	for _, p := range next.Get("content").Array() {
		if p.Get("type").String() == "tool_result" {
			id := p.Get("tool_use_id").String()
			if !remaining[id] {
				return false
			}
			delete(remaining, id)
		}
	}
	if len(remaining) != 0 {
		return false
	}
	for _, v := range tail {
		if v.Get("type").String() != "function_call_output" {
			break
		}
		id := v.Get("call_id").String()
		if !calls[id] {
			return false
		}
		delete(calls, id)
	}
	return len(calls) == 0
}
