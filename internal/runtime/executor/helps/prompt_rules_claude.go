package helps

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudePromptFmt implements prompt-rule mutations for the Anthropic Messages
// (Claude) source format. The system prompt lives at the top-level "system" field
// which can be either a string or an array of content blocks ({type:"text",text}).
// User messages live in messages[*] where role=="user"; their content can be a
// string or an array of blocks ({type:"text"|"tool_result"|"image"|...}).
//
// Per Codex review §18: messages with role=="user" but content that is purely
// tool_result blocks are NOT considered natural-language user prompts and are
// skipped by inject/strip.
type claudePromptFmt struct{}

func (claudePromptFmt) InjectSystem(payload []byte, content, marker, position string) []byte {
	if len(content) == 0 {
		return payload
	}
	sys := gjson.GetBytes(payload, "system")
	if !sys.Exists() {
		// Marker mode: no anchor exists, no synthesis. Boundary mode: create as
		// plain string to match the simplest Claude shape.
		if marker != "" {
			return payload
		}
		updated, err := sjson.SetBytes(payload, "system", content)
		if err != nil {
			return payload
		}
		return updated
	}
	if sys.Type == gjson.String {
		text := sys.String()
		newText, mutated := injectIntoText(text, content, marker, position)
		if !mutated {
			return payload
		}
		updated, err := sjson.SetBytes(payload, "system", newText)
		if err != nil {
			return payload
		}
		return updated
	}
	if !sys.IsArray() {
		return payload
	}
	return blockArrayInject(payload, "system", isClaudeTextBlock, newClaudeTextBlock, content, marker, position)
}

func (claudePromptFmt) StripSystem(payload []byte, re *regexp.Regexp) []byte {
	sys := gjson.GetBytes(payload, "system")
	if !sys.Exists() {
		return payload
	}
	if sys.Type == gjson.String {
		text := sys.String()
		stripped := re.ReplaceAllString(text, "")
		if stripped == text {
			return payload
		}
		updated, err := sjson.SetBytes(payload, "system", stripped)
		if err != nil {
			return payload
		}
		return updated
	}
	if !sys.IsArray() {
		return payload
	}
	out := payload
	for i, block := range sys.Array() {
		if block.Get("type").String() != "text" {
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
		if updated, err := sjson.SetBytes(out, fmt.Sprintf("system.%d.text", i), stripped); err == nil {
			out = updated
		}
	}
	return out
}

func (claudePromptFmt) InjectLastUser(payload []byte, content, marker, position string) []byte {
	if len(content) == 0 {
		return payload
	}
	msgs := gjson.GetBytes(payload, "messages")
	if !msgs.IsArray() {
		return payload
	}
	idx := claudeLastNaturalUserIndex(msgs)
	if idx < 0 {
		return payload
	}
	return claudeMutateUserContent(payload, idx, content, marker, position)
}

func (claudePromptFmt) StripLastUser(payload []byte, re *regexp.Regexp) []byte {
	msgs := gjson.GetBytes(payload, "messages")
	if !msgs.IsArray() {
		return payload
	}
	idx := claudeLastNaturalUserIndex(msgs)
	if idx < 0 {
		return payload
	}
	return claudeStripUserContent(payload, idx, re)
}

// claudeLastNaturalUserIndex finds the last role=="user" message whose content is
// a non-empty string OR an array containing at least one text block. Messages
// whose content array is purely tool_result/image/document blocks are skipped.
func claudeLastNaturalUserIndex(messages gjson.Result) int {
	arr := messages.Array()
	for i := len(arr) - 1; i >= 0; i-- {
		m := arr[i]
		if m.Get("role").String() != "user" {
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
				if block.Get("type").String() == "text" && hasNonEmptyText(block, "text") {
					return i
				}
			}
		}
	}
	return -1
}

func claudeMutateUserContent(payload []byte, idx int, content, marker, position string) []byte {
	path := fmt.Sprintf("messages.%d.content", idx)
	c := gjson.GetBytes(payload, path)
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
	if !c.IsArray() {
		return payload
	}
	return blockArrayInject(payload, path, isClaudeTextBlock, newClaudeTextBlock, content, marker, position)
}

func isClaudeTextBlock(b gjson.Result) bool {
	return b.Get("type").String() == "text"
}

func newClaudeTextBlock(content string) ([]byte, error) {
	return marshalJSONNoEscape(map[string]any{"type": "text", "text": content})
}

func claudeStripUserContent(payload []byte, idx int, re *regexp.Regexp) []byte {
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
	if !c.IsArray() {
		return payload
	}
	out := payload
	for i, block := range c.Array() {
		if block.Get("type").String() != "text" {
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
