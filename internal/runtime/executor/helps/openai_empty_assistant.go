package helps

import (
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// DropEmptyAssistantMessages removes assistant messages that carry no content
// and no tool calls from an OpenAI chat-completions payload. Some upstreams
// (e.g. Moonshot behind the Cline gateway) reject the whole request with a 400
// "message ... with role 'assistant' must not be empty" when the conversation
// history contains such a message, which happens when a previous assistant
// turn was interrupted before producing output. The payload is returned
// unchanged when no empty assistant messages are present.
func DropEmptyAssistantMessages(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}
	emptyIdx := make([]int, 0, 2)
	messages.ForEach(func(idx, message gjson.Result) bool {
		if message.Get("role").String() != "assistant" {
			return true
		}
		if toolCalls := message.Get("tool_calls"); toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
			return true
		}
		if !isEmptyOpenAIMessageContent(message.Get("content")) {
			return true
		}
		emptyIdx = append(emptyIdx, int(idx.Int()))
		return true
	})
	if len(emptyIdx) == 0 {
		return payload
	}
	for i := len(emptyIdx) - 1; i >= 0; i-- {
		if updated, err := sjson.DeleteBytes(payload, "messages."+strconv.Itoa(emptyIdx[i])); err == nil {
			payload = updated
		}
	}
	return payload
}

func isEmptyOpenAIMessageContent(content gjson.Result) bool {
	if !content.Exists() || content.Type == gjson.Null {
		return true
	}
	if content.Type == gjson.String {
		return content.String() == ""
	}
	if content.IsArray() {
		empty := true
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "text" && part.Get("text").String() == "" {
				return true
			}
			empty = false
			return false
		})
		return empty
	}
	return false
}
