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
	// kindUserLatest is the newest user turn when earlier turns exist. Caveman
	// leaves it alone: clients append memory and quoted history to it.
	kindUserLatest
	kindSystem
	kindToolOutput
	kindToolCurrent
	// kindToolBlocks is text in a tool message's content array: tool output for
	// rtk, but upstream lite caps only string tool content.
	kindToolBlocks
)

// segment is one compressible JSON string value, identified by its byte span in
// the original body so the whole payload can be rebuilt in a single pass.
type segment struct {
	start int
	end   int
	kind  segmentKind
	value gjson.Result
	// current marks a segment from this request's own turn. Dedup never replaces
	// it: a marker where the model expected its fresh read makes it read again.
	current bool
	// command is the shell command that produced this tool output, when the
	// payload carries the call pairing. Empty for prose and for tool results
	// whose originating call is not in the body.
	command string
	// toolName is the tool that produced this tool output, empty when unknown.
	toolName string
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
	// toolCommands maps a tool call id to the shell command it ran, read from
	// the payload's own call records. It is only ever read, never rewritten: a
	// tool call's arguments stay byte-identical, they just tell the rtk filter
	// which command produced the output it is looking at.
	toolCommands map[string]string
	// toolNames maps a tool call id to the tool that ran it, so rtk can tell a
	// shell command's output from a file read or a grep result.
	toolNames map[string]string
	// current is set while scanning items at or after the current turn's first
	// item, so a tool result the model just asked for is never capped.
	current bool
	// latestUser marks the newest user item when earlier turns exist: caveman
	// must not rewrite the message being answered.
	latestUser bool
	// noAssistant records a payload with no assistant item. Upstream dedup treats
	// that whole conversation as the current turn, so nothing is rewritten.
	noAssistant bool
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

	s.collectToolCommands(items)

	// Tool results from the current turn on are what the model just asked for,
	// so lite must not cap them; see currentTurnStart.
	currentFrom, hasAssistant := currentTurnStart(items)
	s.noAssistant = !hasAssistant
	lastUser, earlierTurn := lastUserTurn(items)
	idx := 0
	items.ForEach(func(_, item gjson.Result) bool {
		s.current = idx >= currentFrom
		s.latestUser = earlierTurn && idx == lastUser
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
	s.addPromptValue(root.Get("system"), kindSystem)
	s.addPromptValue(root.Get("systemInstruction"), kindSystem)
	s.addPromptValue(root.Get("system_instruction"), kindSystem)
}

// collectToolCommands pairs every tool call id in the payload with the shell
// command it ran. Upstream picks an rtk filter by the command that produced the
// output, which a chat payload only carries as a side record; reading it here is
// the difference between running a command filter and guessing from content.
func (s *scanner) collectToolCommands(items gjson.Result) {
	items.ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			id := item.Get("call_id").String()
			s.recordTool(id, item.Get("name").String())
			s.recordCommand(id, commandFromArgs(item.Get("arguments").String()))
		case "local_shell_call":
			id := item.Get("call_id").String()
			s.recordTool(id, "bash")
			s.recordCommand(id, item.Get("action.command").String())
		}
		item.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
			id := call.Get("id").String()
			s.recordTool(id, call.Get("function.name").String())
			s.recordCommand(id, commandFromArgs(call.Get("function.arguments").String()))
			return true
		})
		item.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_use" {
				id := block.Get("id").String()
				s.recordTool(id, block.Get("name").String())
				s.recordCommand(id, commandFromArgs(block.Get("input").Raw))
			}
			return true
		})
		return true
	})
}

// recordTool stores the tool that owns a call id: rtk admits only shell tool
// output, so a read or a grep result must not be treated as command logs.
func (s *scanner) recordTool(id, name string) {
	if id == "" || name == "" {
		return
	}
	if s.toolNames == nil {
		s.toolNames = make(map[string]string, 8)
	}
	s.toolNames[id] = name
}

// recordCommand stores id -> command from a call record. Only a non-empty
// command is worth keeping.
func (s *scanner) recordCommand(id, command string) {
	if id == "" || command == "" {
		return
	}
	if s.toolCommands == nil {
		s.toolCommands = make(map[string]string, 8)
	}
	s.toolCommands[id] = command
}

// commandFromArgs reads the `command` field out of a call's arguments, which
// arrive as JSON text or as an already-decoded object.
func commandFromArgs(raw string) string {
	if cmd := gjson.Get(raw, "command"); cmd.Type == gjson.String {
		return cmd.String()
	}
	return ""
}

// currentTurnStart is the index of the first item this request is about.
// Upstream lite exempts the current turn's tool results from its cap: cutting
// what the model just asked for makes it re-read the same file every turn.
// A Responses turn carries the call and its output as top-level items with no
// assistant message, so its trailing outputs count as this turn's too.
func currentTurnStart(items gjson.Result) (int, bool) {
	arr := items.Array()
	from := len(arr)
	found := false
	for i, item := range arr {
		if item.Get("role").String() == "assistant" {
			from, found = i+1, true
		}
	}
	trailing := len(arr)
	for trailing > 0 && isToolOutputItem(arr[trailing-1]) {
		trailing--
	}
	if trailing < from {
		return trailing, found
	}
	return from, found
}

// lastUserTurn is the index of the newest user item and whether an earlier turn
// precedes it. Caveman leaves that message as written: clients append memory
// blocks and quoted history to it, which would otherwise let the rules delete
// words the user typed. A one-shot body keeps compressing.
func lastUserTurn(items gjson.Result) (int, bool) {
	arr := items.Array()
	last := -1
	for i, item := range arr {
		if item.Get("role").String() == "user" {
			last = i
		}
	}
	if last < 0 {
		return -1, false
	}
	for _, item := range arr[:last] {
		if role := item.Get("role").String(); role == "user" || role == "assistant" {
			return last, true
		}
	}
	return last, false
}

// isToolOutputItem reports whether a Responses item is a tool result.
func isToolOutputItem(item gjson.Result) bool {
	switch item.Get("type").String() {
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output", "apply_patch_call_output":
		return true
	}
	return false
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
	// Responses API tool output items carry their call id beside the text.
	switch item.Get("type").String() {
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output", "apply_patch_call_output":
		callID := item.Get("call_id").String()
		s.addTool(item.Get("output"), callID, kindToolOutput)
		s.addParts(item.Get("content"), kindToolBlocks, callID)
		return
	}
	switch item.Get("role").String() {
	case "system":
		if !s.includeSystem {
			return
		}
		content := item.Get("content")
		s.add(content, kindSystem)
		s.addParts(content, kindSystem, "")
	case "user":
		content := item.Get("content")
		kind := kindUser
		if s.latestUser {
			kind = kindUserLatest
		}
		s.add(content, kind)
		s.addParts(content, kind, "")
	case "tool":
		callID := item.Get("tool_call_id").String()
		content := item.Get("content")
		s.addTool(content, callID, kindToolOutput)
		s.addParts(content, kindToolBlocks, callID)
	}
}

// addParts records the text blocks of a content array. callID names the tool
// call the blocks answer, so a tool_result keeps its originating command.
func (s *scanner) addParts(content gjson.Result, kind segmentKind, callID string) {
	if !content.IsArray() {
		return
	}
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "text", "input_text", "output_text":
			if kind == kindToolOutput || kind == kindToolBlocks {
				s.addTool(part.Get("text"), callID, kind)
			} else {
				s.add(part.Get("text"), kind)
			}
		case "tool_result":
			resultID := part.Get("tool_use_id").String()
			inner := part.Get("content")
			if inner.IsArray() {
				inner.ForEach(func(_, sub gjson.Result) bool {
					if sub.Get("type").String() == "text" {
						s.addTool(sub.Get("text"), resultID, kindToolOutput)
					}
					return true
				})
				return true
			}
			s.addTool(inner, resultID, kindToolOutput)
		}
		return true
	})
}

// addTool records a tool-output string together with the shell command that
// produced it, so a command-aware engine can pick its filter. The command is
// look-up only: nothing in the payload is rewritten.
func (s *scanner) addTool(res gjson.Result, callID string, kind segmentKind) {
	before := len(s.segs)
	s.add(res, kind)
	if callID == "" || len(s.segs) == before {
		return
	}
	seg := &s.segs[before]
	if s.current && kind == kindToolOutput {
		seg.kind = kindToolCurrent
	}
	if cmd := s.toolCommands[callID]; cmd != "" {
		seg.command = cmd
	}
	seg.toolName = s.toolNames[callID]
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
	s.segs = append(s.segs, segment{start: start, end: end, kind: kind, value: res, current: s.current})
}
