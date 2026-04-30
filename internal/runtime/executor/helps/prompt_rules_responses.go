package helps

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openaiResponsePromptFmt implements prompt-rule mutations for the OpenAI
// Responses source format. System prompt lives in the top-level "instructions"
// string. User messages live in the "input" field, which can be either a bare
// string or an array of input items with role and content blocks.
type openaiResponsePromptFmt struct{}

func (openaiResponsePromptFmt) InjectSystem(payload []byte, content, marker, position string) []byte {
	if len(content) == 0 {
		return payload
	}
	cur := gjson.GetBytes(payload, "instructions")
	if cur.Exists() && cur.Type == gjson.String {
		text := cur.String()
		if containsMarker(text, marker) {
			return payload
		}
		updated, err := sjson.SetBytes(payload, "instructions", applyPosition(text, content, position))
		if err != nil {
			return payload
		}
		return updated
	}
	// Field absent — create with content alone.
	updated, err := sjson.SetBytes(payload, "instructions", content)
	if err != nil {
		return payload
	}
	return updated
}

func (openaiResponsePromptFmt) StripSystem(payload []byte, re *regexp.Regexp) []byte {
	cur := gjson.GetBytes(payload, "instructions")
	if !cur.Exists() || cur.Type != gjson.String {
		return payload
	}
	text := cur.String()
	stripped := re.ReplaceAllString(text, "")
	if stripped == text {
		return payload
	}
	updated, err := sjson.SetBytes(payload, "instructions", stripped)
	if err != nil {
		return payload
	}
	return updated
}

func (openaiResponsePromptFmt) InjectLastUser(payload []byte, content, marker, position string) []byte {
	if len(content) == 0 {
		return payload
	}
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() {
		return payload
	}
	if input.Type == gjson.String {
		text := input.String()
		if containsMarker(text, marker) {
			return payload
		}
		updated, err := sjson.SetBytes(payload, "input", applyPosition(text, content, position))
		if err != nil {
			return payload
		}
		return updated
	}
	if !input.IsArray() {
		return payload
	}
	idx := responsesLastNaturalUserIndex(input)
	if idx < 0 {
		return payload
	}
	return responsesMutateInputItem(payload, idx, content, marker, position)
}

func (openaiResponsePromptFmt) StripLastUser(payload []byte, re *regexp.Regexp) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() {
		return payload
	}
	if input.Type == gjson.String {
		text := input.String()
		stripped := re.ReplaceAllString(text, "")
		if stripped == text {
			return payload
		}
		updated, err := sjson.SetBytes(payload, "input", stripped)
		if err != nil {
			return payload
		}
		return updated
	}
	if !input.IsArray() {
		return payload
	}
	idx := responsesLastNaturalUserIndex(input)
	if idx < 0 {
		return payload
	}
	return responsesStripInputItem(payload, idx, re)
}

// responsesLastNaturalUserIndex finds the last input item with role=="user" that
// has natural-language text content. Skips function_call, function_call_output,
// reasoning, and other non-user-text item types.
func responsesLastNaturalUserIndex(input gjson.Result) int {
	arr := input.Array()
	for i := len(arr) - 1; i >= 0; i-- {
		item := arr[i]
		if item.Get("role").String() != "user" {
			continue
		}
		// Items with explicit non-message types are skipped.
		if t := item.Get("type").String(); t != "" && t != "message" && t != "input_text" {
			continue
		}
		c := item.Get("content")
		if c.Type == gjson.String {
			if strings.TrimSpace(c.String()) != "" {
				return i
			}
			continue
		}
		if c.IsArray() {
			for _, block := range c.Array() {
				bt := block.Get("type").String()
				if (bt == "input_text" || bt == "text") && hasNonEmptyText(block, "text") {
					return i
				}
			}
		}
	}
	return -1
}

func responsesMutateInputItem(payload []byte, idx int, content, marker, position string) []byte {
	path := fmt.Sprintf("input.%d.content", idx)
	c := gjson.GetBytes(payload, path)
	if c.Type == gjson.String {
		text := c.String()
		if containsMarker(text, marker) {
			return payload
		}
		updated, err := sjson.SetBytes(payload, path, applyPosition(text, content, position))
		if err != nil {
			return payload
		}
		return updated
	}
	if !c.IsArray() {
		return payload
	}
	for _, block := range c.Array() {
		bt := block.Get("type").String()
		if (bt == "input_text" || bt == "text") && containsMarker(block.Get("text").String(), marker) {
			return payload
		}
	}
	newBlock, err := marshalJSONNoEscape(map[string]any{"type": "input_text", "text": content})
	if err != nil {
		return payload
	}
	if position == "append" {
		updated, err := sjson.SetRawBytes(payload, path+".-1", newBlock)
		if err != nil {
			return payload
		}
		return updated
	}
	return prependArrayElement(payload, path, newBlock)
}

func responsesStripInputItem(payload []byte, idx int, re *regexp.Regexp) []byte {
	path := fmt.Sprintf("input.%d.content", idx)
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
		bt := block.Get("type").String()
		if bt != "input_text" && bt != "text" {
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
