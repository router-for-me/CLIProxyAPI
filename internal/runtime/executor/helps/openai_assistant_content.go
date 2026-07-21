package helps

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// FlattenAssistantContentArrays rewrites assistant messages whose content is
// an array of text parts into plain-string content. The OpenAI spec allows
// part arrays on assistant messages, but several chat-completions upstreams
// (Moonshot behind the Vercel gateway, for one) parse assistant content as a
// string only and coerce an array to empty, then reject the request with 400
// "the message at position N with role 'assistant' must not be empty" - even
// when the parts hold real text. Flattening text-only part arrays is lossless
// and spec-valid everywhere, so it is applied unconditionally. Arrays that
// contain non-text parts are left untouched. The payload is returned
// unchanged when no assistant part arrays are present.
func FlattenAssistantContentArrays(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}
	type flat struct {
		idx  int
		text string
	}
	flats := make([]flat, 0, 4)
	messages.ForEach(func(idx, message gjson.Result) bool {
		if message.Get("role").String() != "assistant" {
			return true
		}
		content := message.Get("content")
		if !content.IsArray() {
			return true
		}
		var b strings.Builder
		ok := true
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() != "text" {
				ok = false
				return false
			}
			b.WriteString(part.Get("text").String())
			return true
		})
		if ok {
			flats = append(flats, flat{int(idx.Int()), b.String()})
		}
		return true
	})
	if len(flats) == 0 {
		return payload
	}
	for _, f := range flats {
		if updated, err := sjson.SetBytes(payload, "messages."+strconv.Itoa(f.idx)+".content", f.text); err == nil {
			payload = updated
		}
	}
	return payload
}
