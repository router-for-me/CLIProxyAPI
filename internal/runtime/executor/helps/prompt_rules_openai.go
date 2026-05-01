package helps

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openaiPromptFmt implements prompt-rule mutations for the OpenAI Chat
// Completions source format. System prompts live as messages with role=="system"
// (or "developer", which OpenAI treats as system-equivalent on newer models).
// User messages with content that is purely a tool result, an empty list, or
// only image blocks are NOT treated as natural-language for inject/strip.
type openaiPromptFmt struct{}

func (openaiPromptFmt) InjectSystem(payload []byte, content, marker, position string) []byte {
	if len(content) == 0 {
		return payload
	}
	msgs := gjson.GetBytes(payload, "messages")
	if msgs.IsArray() {
		idx, role := openaiFirstSystemLikeIndex(msgs)
		if idx >= 0 {
			return openaiMutateMessageContent(payload, idx, role, content, marker, position, false)
		}
	}
	// No system or developer message. In marker mode there is no anchor to
	// attach to, so the rule is a no-op. In boundary mode we synthesize a fresh
	// system message carrying just content.
	if marker != "" {
		return payload
	}
	newMsg := map[string]any{"role": "system", "content": content}
	raw, err := marshalJSONNoEscape(newMsg)
	if err != nil {
		return payload
	}
	if !msgs.Exists() {
		updated, err := sjson.SetRawBytes(payload, "messages", append(append([]byte("["), raw...), ']'))
		if err != nil {
			return payload
		}
		return updated
	}
	return prependArrayElement(payload, "messages", raw)
}

func (openaiPromptFmt) StripSystem(payload []byte, re *regexp.Regexp) []byte {
	msgs := gjson.GetBytes(payload, "messages")
	if !msgs.IsArray() {
		return payload
	}
	out := payload
	for i, m := range msgs.Array() {
		role := m.Get("role").String()
		if role != "system" && role != "developer" {
			continue
		}
		out = openaiStripMessageContent(out, i, re)
	}
	return out
}

func (openaiPromptFmt) InjectLastUser(payload []byte, content, marker, position string) []byte {
	msgs := gjson.GetBytes(payload, "messages")
	if !msgs.IsArray() {
		return payload
	}
	idx := openaiLastNaturalUserIndex(msgs)
	if idx < 0 {
		return payload
	}
	return openaiMutateMessageContent(payload, idx, "user", content, marker, position, true)
}

func (openaiPromptFmt) StripLastUser(payload []byte, re *regexp.Regexp) []byte {
	msgs := gjson.GetBytes(payload, "messages")
	if !msgs.IsArray() {
		return payload
	}
	idx := openaiLastNaturalUserIndex(msgs)
	if idx < 0 {
		return payload
	}
	return openaiStripMessageContent(payload, idx, re)
}

// openaiFirstSystemLikeIndex returns the index of the first system or developer
// message in the array. Prefers system; falls back to developer.
func openaiFirstSystemLikeIndex(messages gjson.Result) (int, string) {
	devIdx := -1
	for i, m := range messages.Array() {
		role := m.Get("role").String()
		switch role {
		case "system":
			return i, "system"
		case "developer":
			if devIdx < 0 {
				devIdx = i
			}
		}
	}
	if devIdx >= 0 {
		return devIdx, "developer"
	}
	return -1, ""
}

// openaiLastNaturalUserIndex finds the last role=="user" message whose content is
// natural-language: a non-empty string, or an array containing at least one
// {type:"text"|"input_text"} block whose text is non-empty after trim. Messages
// with tool_call_id, role=="tool", or content that is purely image/empty are
// skipped.
func openaiLastNaturalUserIndex(messages gjson.Result) int {
	arr := messages.Array()
	for i := len(arr) - 1; i >= 0; i-- {
		m := arr[i]
		if m.Get("role").String() != "user" {
			continue
		}
		if m.Get("tool_call_id").Exists() {
			continue
		}
		c := m.Get("content")
		if c.Type == gjson.String {
			if strings.TrimSpace(c.String()) != "" {
				return i
			}
			continue
		}
		if c.IsArray() {
			for _, block := range c.Array() {
				blockType := block.Get("type").String()
				if blockType == "text" || blockType == "input_text" {
					if hasNonEmptyText(block, "text") {
						return i
					}
				}
			}
		}
	}
	return -1
}

// openaiMutateMessageContent injects content into messages[idx].content per the
// given position and marker. Handles string, array, null, and missing content
// shapes. role is preserved for future per-role policy.
func openaiMutateMessageContent(payload []byte, idx int, role, content, marker, position string, _ bool) []byte {
	_ = role
	path := fmt.Sprintf("messages.%d.content", idx)
	c := gjson.GetBytes(payload, path)
	// Null or absent content is treated as an empty target string. Boundary
	// mode replaces it with content; marker mode no-ops (no anchor).
	if !c.Exists() || c.Type == gjson.Null {
		newText, mutated := injectIntoText("", content, marker, position)
		if !mutated {
			return payload
		}
		updated, err := sjson.SetBytes(payload, path, newText)
		if err != nil {
			return payload
		}
		return updated
	}
	if c.Type == gjson.String {
		text := c.String()
		newText, mutated := injectIntoText(text, content, marker, position)
		if !mutated {
			return payload
		}
		updated, err := sjson.SetBytes(payload, path, newText)
		if err != nil {
			return payload
		}
		return updated
	}
	if c.IsArray() {
		// We canonicalize new blocks as type="text"; OpenAI Chat Completions
		// accepts that uniformly. Mixed-type arrays remain valid because the
		// scan in blockArrayInject treats both "text" and "input_text" as
		// text-bearing on read.
		return blockArrayInject(payload, path, isOpenAITextBlock, newOpenAITextBlock, content, marker, position)
	}
	// Unknown content shape — leave untouched.
	return payload
}

func isOpenAITextBlock(b gjson.Result) bool {
	t := b.Get("type").String()
	return t == "text" || t == "input_text"
}

func newOpenAITextBlock(content string) ([]byte, error) {
	return marshalJSONNoEscape(map[string]any{"type": "text", "text": content})
}

// openaiStripMessageContent applies the regex to the content of messages[idx],
// handling string and array shapes.
func openaiStripMessageContent(payload []byte, idx int, re *regexp.Regexp) []byte {
	path := fmt.Sprintf("messages.%d.content", idx)
	c := gjson.GetBytes(payload, path)
	if c.Type == gjson.String {
		text := c.String()
		stripped := re.ReplaceAllString(text, "")
		if stripped == text {
			return payload
		}
		updated, err := sjson.SetBytes(payload, path, stripped)
		if err != nil {
			return payload
		}
		return updated
	}
	if c.IsArray() {
		out := payload
		for i, block := range c.Array() {
			t := block.Get("type").String()
			if t != "text" && t != "input_text" {
				continue
			}
			tx := block.Get("text")
			if !tx.Exists() {
				continue
			}
			s := tx.String()
			stripped := re.ReplaceAllString(s, "")
			if stripped == s {
				continue
			}
			if updated, err := sjson.SetBytes(out, fmt.Sprintf("%s.%d.text", path, i), stripped); err == nil {
				out = updated
			}
		}
		return out
	}
	return payload
}

