package requestlogs

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// maxSSEEvents caps how many parsed events the detail endpoint returns.
const maxSSEEvents = 5000

// SSEEvent is one server-sent event from a streamed response.
type SSEEvent struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

// ToolCall is a reconstructed tool/function call.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Assembled is a streamed response reconstructed into text, reasoning and tool calls.
type Assembled struct {
	Text      string     `json:"text"`
	Reasoning string     `json:"reasoning"`
	ToolCalls []ToolCall `json:"tool_calls"`
}

// sseCollector parses SSE lines into events and feeds them to an assembler.
type sseCollector struct {
	events    []SSEEvent
	bytes     int
	event     string
	data      []string
	hasFields bool
	asm       assembler
}

func (c *sseCollector) line(l string) {
	if l == "" {
		c.flush()
		return
	}
	if strings.HasPrefix(l, ":") {
		return
	}
	field, value, _ := strings.Cut(l, ":")
	value = strings.TrimPrefix(value, " ")
	switch field {
	case "event":
		c.event = value
		c.hasFields = true
	case "data":
		c.data = append(c.data, value)
		c.hasFields = true
	}
}

func (c *sseCollector) flush() {
	if !c.hasFields {
		return
	}
	ev := SSEEvent{Event: c.event, Data: strings.Join(c.data, "\n")}
	c.event, c.data, c.hasFields = "", nil, false
	c.asm.feed(ev)
	if len(c.events) < maxSSEEvents && c.bytes+len(ev.Data) <= BodyLimit {
		c.events = append(c.events, ev)
		c.bytes += len(ev.Data)
	}
}

// assembler reconstructs OpenAI chat/responses, Claude messages and Gemini streams.
type assembler struct {
	format    string
	text      strings.Builder
	reasoning strings.Builder
	tools     []*ToolCall
	byKey     map[string]*ToolCall
}

func (a *assembler) tool(key string) *ToolCall {
	if a.byKey == nil {
		a.byKey = map[string]*ToolCall{}
	}
	if t, ok := a.byKey[key]; ok {
		return t
	}
	t := &ToolCall{}
	a.byKey[key] = t
	a.tools = append(a.tools, t)
	return t
}

func appendCapped(b *strings.Builder, s string) {
	if b.Len()+len(s) > BodyLimit {
		s = s[:max(0, BodyLimit-b.Len())]
	}
	b.WriteString(s)
}

func (a *assembler) feed(ev SSEEvent) {
	data := strings.TrimSpace(ev.Data)
	if data == "" || data == "[DONE]" || !ojson.Valid(data) {
		return
	}
	root := ojson.Parse(data)
	typ := root.Get("type").String()
	if typ == "" {
		typ = ev.Event
	}
	switch {
	case root.Get("choices").IsArray():
		a.format = "openai-chat"
		for _, choice := range root.Get("choices").Array() {
			delta := choice.Get("delta")
			appendCapped(&a.text, delta.Get("content").String())
			if r := delta.Get("reasoning_content"); r.Exists() {
				appendCapped(&a.reasoning, r.String())
			} else {
				appendCapped(&a.reasoning, delta.Get("reasoning").String())
			}
			for _, tc := range delta.Get("tool_calls").Array() {
				key := "chat:" + choice.Get("index").String() + ":" + tc.Get("index").String()
				if !tc.Get("index").Exists() {
					key = "chat:id:" + tc.Get("id").String()
				}
				t := a.tool(key)
				if id := tc.Get("id").String(); id != "" {
					t.ID = id
				}
				if name := tc.Get("function.name").String(); name != "" {
					t.Name = name
				}
				t.Arguments += tc.Get("function.arguments").String()
			}
		}
	case strings.HasPrefix(typ, "response."):
		a.format = "openai-responses"
		switch typ {
		case "response.output_text.delta":
			appendCapped(&a.text, root.Get("delta").String())
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			appendCapped(&a.reasoning, root.Get("delta").String())
		case "response.output_item.added", "response.output_item.done":
			item := root.Get("item")
			if item.Get("type").String() != "function_call" {
				return
			}
			t := a.tool("resp:" + item.Get("id").String())
			if id := item.Get("call_id").String(); id != "" {
				t.ID = id
			} else if t.ID == "" {
				t.ID = item.Get("id").String()
			}
			if name := item.Get("name").String(); name != "" {
				t.Name = name
			}
			if args := item.Get("arguments").String(); args != "" && (typ == "response.output_item.done" || t.Arguments == "") {
				t.Arguments = args
			}
		case "response.function_call_arguments.delta":
			t := a.tool("resp:" + root.Get("item_id").String())
			t.Arguments += root.Get("delta").String()
		case "response.function_call_arguments.done":
			t := a.tool("resp:" + root.Get("item_id").String())
			if args := root.Get("arguments").String(); args != "" {
				t.Arguments = args
			}
		}
	case typ == "message_start" || typ == "content_block_start" || typ == "content_block_delta" ||
		typ == "content_block_stop" || typ == "message_delta" || typ == "message_stop":
		a.format = "claude"
		key := "claude:" + root.Get("index").String()
		switch typ {
		case "content_block_start":
			cb := root.Get("content_block")
			switch cb.Get("type").String() {
			case "tool_use", "server_tool_use":
				t := a.tool(key)
				t.ID = cb.Get("id").String()
				t.Name = cb.Get("name").String()
				if in := cb.Get("input"); in.IsObject() && in.Raw != "{}" {
					t.Arguments = in.Raw
				}
			case "text":
				appendCapped(&a.text, cb.Get("text").String())
			case "thinking":
				appendCapped(&a.reasoning, cb.Get("thinking").String())
			}
		case "content_block_delta":
			delta := root.Get("delta")
			switch delta.Get("type").String() {
			case "text_delta":
				appendCapped(&a.text, delta.Get("text").String())
			case "thinking_delta":
				appendCapped(&a.reasoning, delta.Get("thinking").String())
			case "input_json_delta":
				a.tool(key).Arguments += delta.Get("partial_json").String()
			}
		}
	default:
		candidates := root.Get("candidates")
		if !candidates.IsArray() {
			candidates = root.Get("response.candidates")
		}
		if !candidates.IsArray() {
			return
		}
		a.format = "gemini"
		for _, cand := range candidates.Array() {
			for _, part := range cand.Get("content.parts").Array() {
				if fc := part.Get("functionCall"); fc.IsObject() {
					t := a.tool("gemini:" + strconv.Itoa(len(a.tools)))
					t.ID = fc.Get("id").String()
					t.Name = fc.Get("name").String()
					t.Arguments = fc.Get("args").Raw
					continue
				}
				if part.Get("thought").Bool() {
					appendCapped(&a.reasoning, part.Get("text").String())
				} else {
					appendCapped(&a.text, part.Get("text").String())
				}
			}
		}
	}
}

func (a *assembler) result() *Assembled {
	if a.format == "" {
		return nil
	}
	out := &Assembled{Text: a.text.String(), Reasoning: a.reasoning.String(), ToolCalls: []ToolCall{}}
	for _, t := range a.tools {
		out.ToolCalls = append(out.ToolCalls, *t)
	}
	return out
}

// tokenSet holds token counts reported in a response body (fallback when no usage record).
type tokenSet struct {
	in, out, reasoning, cached *int64
}

func (t *tokenSet) any() bool {
	return t.in != nil || t.out != nil || t.reasoning != nil || t.cached != nil
}

func setToken(dst **int64, u ojson.Result, paths ...string) {
	for _, path := range paths {
		v := u.Get(path)
		if v.Type != ojson.Number {
			continue
		}
		n := v.Int()
		if *dst == nil || n > 0 {
			*dst = &n
		}
		return
	}
}

// mergeUsage reads usage from OpenAI chat/responses, Claude and Gemini response shapes.
func (t *tokenSet) mergeUsage(data string) {
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" || !ojson.Valid(data) {
		return
	}
	root := ojson.Parse(data)
	for _, path := range []string{"usage", "response.usage", "message.usage", "usageMetadata", "response.usageMetadata"} {
		u := root.Get(path)
		if !u.IsObject() {
			continue
		}
		setToken(&t.in, u, "prompt_tokens", "input_tokens", "promptTokenCount")
		setToken(&t.out, u, "completion_tokens", "output_tokens", "candidatesTokenCount")
		setToken(&t.reasoning, u, "completion_tokens_details.reasoning_tokens", "output_tokens_details.reasoning_tokens", "thoughtsTokenCount")
		setToken(&t.cached, u, "prompt_tokens_details.cached_tokens", "input_tokens_details.cached_tokens", "cache_read_input_tokens", "cachedContentTokenCount")
	}
}
