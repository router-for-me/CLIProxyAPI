package payloadcompress

import (
	"sort"
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
// It only handles the shapes that carry bulk conversation text: chat messages
// (`messages[]`) and Responses items (`input[]`). Assistant output, tool call
// arguments and thinking blocks are never returned. The system prompt and the
// provider-cached head are returned only when the config allows rewriting them,
// so a transform cannot corrupt anything that must survive verbatim.
type scanner struct {
	body []byte
	segs []segment
	// cachePrefix decides how far the provider-cached head is protected; see
	// Config.CachePrefix. The zero value preserves.
	cachePrefix string
	// includeSystem lifts the system-prompt exclusion; see
	// Config.PreserveSystemPrompt.
	includeSystem bool
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

	cut := -1
	if s.cachePrefix == CachePrefixPreserve {
		// Everything up to the last cache_control breakpoint is a provider-cached
		// prefix. Rewriting it would trade saved input tokens for a cache miss and
		// seconds of time-to-first-token, so those items are left byte-identical.
		idx := 0
		items.ForEach(func(_, item gjson.Result) bool {
			if strings.Contains(item.Raw, "cache_control") {
				cut = idx
			}
			idx++
			return true
		})
	}

	if s.includeSystem {
		s.addSystem(root)
	}

	idx := 0
	items.ForEach(func(_, item gjson.Result) bool {
		if idx > cut {
			s.addItem(item)
		}
		idx++
		return true
	})

	if len(s.segs) > 1 {
		// The system prompt lives at the top level, so its spans do not fall in
		// message order. Sorting keeps the single-pass rebuild valid.
		sort.Slice(s.segs, func(i, j int) bool { return s.segs[i].start < s.segs[j].start })
	}
	return len(s.segs) > 0
}

// addSystem records the top-level system prompt, which arrives outside
// `messages[]` for Claude (a string or parts array) and Gemini (parts).
func (s *scanner) addSystem(root gjson.Result) {
	s.addPromptValue(root.Get("system"), kindUser)
	s.addPromptValue(root.Get("systemInstruction"), kindUser)
	s.addPromptValue(root.Get("system_instruction"), kindUser)
}

// addPromptValue accepts a bare string, an array of parts, or an object carrying
// text/parts: the three shapes a system prompt arrives in.
func (s *scanner) addPromptValue(value gjson.Result, kind segmentKind) {
	switch {
	case value.Type == gjson.String:
		s.add(value, kind)
	case value.IsArray():
		value.ForEach(func(_, part gjson.Result) bool {
			s.addPromptValue(part, kind)
			return true
		})
	case value.IsObject():
		if text := value.Get("text"); text.Type == gjson.String {
			s.add(text, kind)
		}
		if parts := value.Get("parts"); parts.Exists() {
			s.addPromptValue(parts, kind)
		}
	}
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
	case "system":
		if !s.includeSystem {
			return
		}
		content := item.Get("content")
		s.add(content, kindUser)
		s.addParts(content, kindUser)
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
