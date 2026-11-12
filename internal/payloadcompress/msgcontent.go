package payloadcompress

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// Shared message-content helpers for the body-level engines, mirroring
// upstream messageContent.ts. They operate on one message's raw JSON so the
// rest of the body stays byte-identical.

// msgIsTextBlock reports whether a content part carries text that may be
// rewritten: a string `text` and no type, or type text/input_text.
func msgIsTextBlock(part ojson.Result) bool {
	if part.Get("text").Type != ojson.String {
		return false
	}
	if !part.Get("type").Exists() {
		return true
	}
	switch part.Get("type").String() {
	case "text", "input_text":
		return true
	}
	return false
}

func msgIsToolResultBlock(part ojson.Result) bool {
	return part.Get("type").String() == "tool_result"
}

// msgText extracts the text of a message content value: the string itself, or
// the text parts of an array joined by newlines.
func msgText(content ojson.Result) string {
	if content.Type == ojson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	for _, part := range content.Array() {
		if msgIsTextBlock(part) {
			if text := part.Get("text").String(); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// msgMapText rewrites every text block of a message. It returns the new raw
// message and whether anything changed.
func msgMapText(raw string, transform func(text string, index int) string) (string, bool) {
	item := ojson.NewDocString(raw)
	content := item.Get("content")
	if content.Type == ojson.String {
		next := transform(content.String(), 0)
		if next == content.String() {
			return "", false
		}
		if err := item.Set("content", next); err != nil {
			return "", false
		}
		return item.String(), true
	}
	if !content.IsArray() {
		return "", false
	}
	parts := content.Array()
	out := make([]string, 0, len(parts))
	changed := false
	index := 0
	for _, part := range parts {
		if !msgIsTextBlock(part) {
			out = append(out, part.Raw)
			continue
		}
		next := transform(part.Get("text").String(), index)
		index++
		if next == part.Get("text").String() {
			out = append(out, part.Raw)
			continue
		}
		block := ojson.NewDocString(part.Raw)
		if err := block.Set("text", next); err != nil {
			out = append(out, part.Raw)
			continue
		}
		out = append(out, block.String())
		changed = true
	}
	if !changed {
		return "", false
	}
	if err := item.SetRaw("content", "["+strings.Join(out, ",")+"]"); err != nil {
		return "", false
	}
	return item.String(), true
}

// msgReplaceTextItem mirrors upstream replaceTextContent on one raw message.
func msgReplaceTextItem(item ojson.Result, newText string) string {
	sub := ojson.NewDocString(item.Raw)
	content := sub.Get("content")
	if content.Type == ojson.String || !content.IsArray() {
		if err := sub.Set("content", newText); err != nil {
			return item.Raw
		}
		return sub.String()
	}
	parts := content.Array()
	out := make([]string, 0, len(parts)+1)
	replaced := false
	hasToolResult := false
	for _, part := range parts {
		if msgIsToolResultBlock(part) {
			hasToolResult = true
		}
		if !msgIsTextBlock(part) {
			out = append(out, part.Raw)
			continue
		}
		if !replaced {
			replaced = true
			block := ojson.NewDocString(part.Raw)
			if err := block.Set("text", newText); err != nil {
				return item.Raw
			}
			out = append(out, block.String())
			continue
		}
		if text := part.Get("text").String(); text != "" && !strings.Contains(newText, text) {
			out = append(out, part.Raw)
		}
	}
	if !replaced {
		block := `{"type":"text","text":` + string(appendJSONString(nil, newText)) + `}`
		if hasToolResult {
			out = append(out, block)
		} else {
			out = append([]string{block}, out...)
		}
	}
	if err := sub.SetRaw("content", "["+strings.Join(out, ",")+"]"); err != nil {
		return item.Raw
	}
	return sub.String()
}

// bodyMessageItems locates a chat `messages` array. Only the chat shape is
// supported by the body-level engines, like upstream.
func bodyMessageItems(body []byte) (*ojson.Doc, []ojson.Result, bool) {
	doc := ojson.NewDocString(string(body))
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return nil, nil, false
	}
	return doc, messages.Array(), true
}

// bodyConvItems locates the conversation array the engines rewrite: chat
// `messages` or Responses `input`. Gemini `contents` is excluded because
// upstream's body adapter maps only these two shapes.
func bodyConvItems(body []byte) (*ojson.Doc, string, []ojson.Result, bool) {
	doc := ojson.NewDocString(string(body))
	for _, path := range []string{"messages", "input"} {
		if arr := doc.Get(path); arr.IsArray() {
			return doc, path, arr.Array(), true
		}
	}
	return nil, "", nil, false
}

// setBodyMessages writes the rewritten message raws back.
func setBodyMessages(doc *ojson.Doc, raws []string) ([]byte, bool) {
	return setBodyArray(doc, "messages", raws)
}

// setBodyArray writes raws back to the conversation array at path.
func setBodyArray(doc *ojson.Doc, path string, raws []string) ([]byte, bool) {
	if err := doc.SetRaw(path, "["+strings.Join(raws, ",")+"]"); err != nil {
		return nil, false
	}
	return []byte(doc.String()), true
}
