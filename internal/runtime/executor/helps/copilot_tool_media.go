package helps

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

// RelayChatToolMedia only rewrites media inside actual tool results. In
// particular, a subsequent user image is never treated as a tool attachment.
func (input *CopilotTurnInput) RelayChatToolMedia(doc *ojson.Doc) {
	messages := doc.Get("messages").Array()
	var out, relays [][]byte
	var inBatch bool
	for i, msg := range messages {
		if msg.Get("role").String() != "tool" {
			out = append(out, relays...)
			relays = nil
			inBatch = false
			if out != nil {
				out = append(out, []byte(msg.Raw))
			}
			continue
		}
		if !inBatch {
			input.chatRelays = nil
			inBatch = true
		}
		parts := msg.Get("content")
		if !parts.IsArray() {
			if out != nil {
				out = append(out, []byte(msg.Raw))
			}
			continue
		}
		var remaining [][]byte
		var texts []string
		var media, nonText bool
		for _, part := range parts.Array() {
			typ := part.Get("type").String()
			if typ != "image_url" && typ != "file" {
				remaining = append(remaining, []byte(part.Raw))
				if typ == "text" {
					texts = append(texts, part.Get("text").String())
				} else {
					nonText = true
				}
				continue
			}
			media = true
			relay := ojson.NewDocString(`{"role":"user","content":[]}`)
			if typ == "image_url" {
				caption := part.Get("description").String()
				if caption == "" {
					caption = "Here is an image:"
				}
				_ = relay.Set("content.-1", map[string]string{"type": "text", "text": caption})
			}
			_ = relay.SetRaw("content.-1", part.Raw)
			if typ == "image_url" && part.Get("description").Exists() {
				_ = relay.Delete("content.1.description")
			}
			raw := relay.Bytes()
			relays = append(relays, raw)
			input.chatRelays = append(input.chatRelays, uuid.NewSHA1(uuid.Nil, raw))
		}
		if !media {
			if out != nil {
				out = append(out, []byte(msg.Raw))
			}
			continue
		}
		if out == nil {
			out = make([][]byte, 0, len(messages)+len(relays))
			for _, prior := range messages[:i] {
				out = append(out, []byte(prior.Raw))
			}
		}
		tool := ojson.NewDocString(msg.Raw)
		if nonText {
			common.SetRawArrayItemsDoc(tool, "content", remaining)
		} else {
			_ = tool.Set("content", strings.Join(texts, "\n"))
		}
		out = append(out, tool.Bytes())
	}
	if out != nil {
		common.SetRawArrayItemsDoc(doc, "messages", append(out, relays...))
	}
}
