package payloadcompress

import (
	"strings"

	"github.com/tidwall/gjson"
)

// segmentKind separates human-authored turns from captured machine output. Engines
// that rewrite prose must never run on tool output.
type segmentKind uint8

const (
	kindUser segmentKind = iota
	kindToolOutput
)

// segment is one compressible JSON string value, identified by its byte span in
// the original body so the whole payload can be rebuilt in a single pass.
type segment struct {
	start int
	end   int
	kind  segmentKind
	value gjson.Result
}

// scanner locates compressible text in a request payload.
//
// It deliberately only handles the shapes that carry bulk conversation text:
// chat messages (`messages[]`) and Responses items (`input[]`). Everything else
// - system prompts, assistant output, tool call arguments, thinking blocks,
// cached prefixes - is never returned, so a transform can never corrupt it.
type scanner struct {
	body []byte
	segs []segment
}

// scan fills s.segs. It reports false when the payload has no recognizable
// conversation shape.
func (s *scanner) scan() bool {
	root := gjson.Parse(bodyString(s.body))

	if input := root.Get("input"); input.Type == gjson.String {
		s.add(input, kindUser)
		return len(s.segs) > 0
	}

	var items gjson.Result
	switch {
	case root.Get("messages").IsArray():
		items = root.Get("messages")
	case root.Get("input").IsArray():
		items = root.Get("input")
	default:
		return false
	}

	// Everything up to the last cache_control breakpoint is a provider-cached
	// prefix. Rewriting it would trade saved input tokens for a cache miss and
	// seconds of time-to-first-token, so those items are left byte-identical.
	cut, idx := -1, 0
	items.ForEach(func(_, item gjson.Result) bool {
		if strings.Contains(item.Raw, "cache_control") {
			cut = idx
		}
		idx++
		return true
	})

	idx = 0
	items.ForEach(func(_, item gjson.Result) bool {
		if idx > cut {
			s.addItem(item)
		}
		idx++
		return true
	})
	return len(s.segs) > 0
}

func (s *scanner) addItem(item gjson.Result) {
	if item.Type == gjson.String {
		s.add(item, kindUser)
		return
	}
	// Responses API tool output items.
	switch item.Get("type").String() {
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
		s.add(item.Get("output"), kindToolOutput)
		s.addParts(item.Get("content"), kindToolOutput)
		return
	}
	switch item.Get("role").String() {
	case "user":
		content := item.Get("content")
		s.add(content, kindUser)
		s.addParts(content, kindUser)
	case "tool":
		content := item.Get("content")
		s.add(content, kindToolOutput)
		s.addParts(content, kindToolOutput)
	}
}

func (s *scanner) addParts(content gjson.Result, kind segmentKind) {
	if !content.IsArray() {
		return
	}
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "text", "input_text", "output_text":
			s.add(part.Get("text"), kind)
		case "tool_result":
			inner := part.Get("content")
			if inner.IsArray() {
				inner.ForEach(func(_, sub gjson.Result) bool {
					if sub.Get("type").String() == "text" {
						s.add(sub.Get("text"), kindToolOutput)
					}
					return true
				})
				return true
			}
			s.add(inner, kindToolOutput)
		}
		return true
	})
}

// add records a string value span. Spans whose offsets do not line up with the
// original bytes are dropped instead of trusted.
func (s *scanner) add(res gjson.Result, kind segmentKind) {
	if res.Type != gjson.String {
		return
	}
	raw := res.Raw
	start := int(res.Index)
	end := start + len(raw)
	if start <= 0 || len(raw) < 4 || end >= len(s.body) {
		return
	}
	if s.body[start] != '"' || s.body[end-1] != '"' {
		return
	}
	s.segs = append(s.segs, segment{start: start, end: end, kind: kind, value: res})
}
