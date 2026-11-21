package helps

import (
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// A Chat assistant can echo the text and calls of a Claude message, but cannot
// represent signed/redacted blocks or their position among those calls.
// Keep the original blocks server-side; an unsigned sibling is a candidate
// too and must invalidate an otherwise indistinguishable signed response.
type copilotMessagesEntry struct {
	content   string
	ambiguous atomic.Bool
}

type CopilotMessagesCapture struct {
	links  *CopilotTurnLinks
	prefix uuid.UUID
}

type messagesProjection struct {
	text      string
	reasoning string
	calls     []ojson.Result
	signed    bool
}

func projectMessagesBlocks(content ojson.Result, upstream bool) (p messagesProjection, ok bool) {
	if !content.IsArray() || len(content.Raw) > copilotReplayLimit {
		return p, false
	}
	var text, reasoning strings.Builder
	for _, block := range content.Array() {
		switch block.Get("type").String() {
		case "thinking":
			if !upstream || block.Get("signature").String() == "" {
				return p, false
			}
			p.signed = true
			reasoning.WriteString(block.Get("thinking").String())
		case "redacted_thinking":
			if !upstream || block.Get("data").String() == "" {
				return p, false
			}
			p.signed = true
		case "text":
			if len(block.Map()) != 2 || block.Get("text").Type != ojson.String {
				return p, false
			}
			text.WriteString(block.Get("text").String())
		case "tool_use":
			if len(block.Map()) != 4 || !block.Get("input").IsObject() {
				return p, false
			}
			p.calls = append(p.calls, block)
		default:
			return p, false
		}
	}
	p.text, p.reasoning = text.String(), reasoning.String()
	return p, len(p.calls) > 0
}

func (p *messagesProjection) key(prefix uuid.UUID) uuid.UUID {
	key := uuid.NewSHA1(prefix, []byte(p.text))
	seen := make(map[string]bool, len(p.calls))
	for _, call := range p.calls {
		id := call.Get("id").String()
		if seen[id] {
			return uuid.Nil
		}
		seen[id] = true
		key = copilotCallDigest(key, id, call.Get("name").String(), call.Get("input").Raw)
		if key == uuid.Nil {
			return key
		}
	}
	return key
}

func (c *CopilotMessagesCapture) Observe(content ojson.Result) {
	// The Messages response may annotate a direct tool use with caller.
	// It is response metadata, not part of the CLI's historical tool_use.
	if !content.IsArray() || len(content.Raw) > copilotReplayLimit {
		return
	}
	var history *ojson.Doc
	for i, block := range content.Array() {
		if block.Get("type").String() == "tool_use" && block.Get("caller.type").String() == "direct" {
			if history == nil {
				history = ojson.NewDocString(content.Raw)
			}
			_ = history.Delete(strconv.Itoa(i) + ".caller")
		}
	}
	if history != nil {
		content = ojson.ParseBytes(history.Bytes())
	}
	p, ok := projectMessagesBlocks(content, true)
	if !ok {
		return
	}
	key := p.key(c.prefix)
	if key == uuid.Nil {
		return
	}
	entry := &copilotMessagesEntry{}
	if p.signed {
		entry.content = strings.Clone(content.Raw)
	}
	old := c.links.messages.GetOrAdd(key, func() *copilotMessagesEntry { return entry })
	if old.content != entry.content {
		old.ambiguous.Store(true)
	}
}

// Verify the source call batch as well as the translated one. A translator
// may sanitize an ID or turn malformed arguments into {}, neither of which
// identifies the original call. Never replace caller-supplied block metadata.
func (p *messagesProjection) matchesChat(msg, results ojson.Result, sourceTail []ojson.Result) bool {
	calls := msg.Get("tool_calls").Array()
	if len(calls) != len(p.calls) || results.Get("role").String() != "user" {
		return false
	}
	ids := make(map[string]bool, len(calls))
	for i, call := range calls {
		id, name := call.Get("id").String(), call.Get("function.name").String()
		block := p.calls[i]
		if id == "" || ids[id] || id != block.Get("id").String() || name != block.Get("name").String() {
			return false
		}
		if copilotCallDigest(uuid.Nil, id, name, call.Get("function.arguments").String()) != copilotCallDigest(uuid.Nil, id, name, block.Get("input").Raw) {
			return false
		}
		ids[id] = true
	}
	// Check the untranslated results too: two distinct caller IDs can
	// sanitize to the same Anthropic ID.
	for _, r := range sourceTail {
		if r.Get("role").String() != "tool" {
			break
		}
		id := r.Get("tool_call_id").String()
		if !ids[id] {
			return false
		}
		ids[id] = false
	}
	for _, pending := range ids {
		if pending {
			return false
		}
	}
	for _, block := range results.Get("content").Array() {
		if block.Get("type").String() == "tool_result" {
			id := block.Get("tool_use_id").String()
			if _, ok := ids[id]; !ok {
				return false
			}
			delete(ids, id)
		}
	}
	return len(ids) == 0
}

// Bind replay to BOTH transcripts: the client's complete prefix and the
// selected account's translated prefix, including system and earlier restored
// blocks. If translation merged assistant messages, there is no safe mapping.
func (c *CopilotTurnLinks) PrepareMessagesReplay(doc *ojson.Doc, source ojson.Result, authID string, opts cliproxyexecutor.Options) (*CopilotMessagesCapture, bool) {
	seed := copilotReplaySeed(authID, opts, doc.Get("model").String())
	if seed == uuid.Nil {
		return nil, false
	}
	mode := doc.Get("thinking.type").String()
	if mode != "enabled" && mode != "adaptive" {
		return nil, false
	}
	type anchor struct {
		prefix uuid.UUID
		msg    ojson.Result
		tail   []ojson.Result
	}
	var anchors []anchor
	sourcePrefix := seed
	sourceMessages := source.Get("messages").Array()
	for i, msg := range sourceMessages {
		if msg.Get("role").String() == "assistant" {
			anchors = append(anchors, anchor{sourcePrefix, msg, sourceMessages[i+1:]})
		}
		sourcePrefix = uuid.NewSHA1(sourcePrefix, []byte(msg.Raw))
	}
	wire := doc.Get("messages").Array()
	n := 0
	for _, msg := range wire {
		if msg.Get("role").String() == "assistant" {
			n++
		}
	}
	if n != len(anchors) {
		return nil, false
	}
	prefix := uuid.NewSHA1(seed, []byte(doc.Get("system").Raw))
	restored := false
	for i, msg := range wire {
		if msg.Get("role").String() == "assistant" {
			a := anchors[0]
			anchors = anchors[1:]
			p, ok := projectMessagesBlocks(msg.Get("content"), false)
			if ok && i+1 < len(wire) && p.matchesChat(a.msg, wire[i+1], a.tail) {
				key := p.key(uuid.NewSHA1(prefix, a.prefix[:]))
				if entry, found := c.messages.Get(key); found && !entry.ambiguous.Load() && entry.content != "" {
					// Chat clients may omit visible reasoning. If echoed, it must match.
					echo := a.msg.Get("reasoning_content")
					match := !echo.Exists() || echo.Type == ojson.Null
					if !match {
						cached, _ := projectMessagesBlocks(ojson.Parse(entry.content), true)
						match = echo.Type == ojson.String && echo.String() == cached.reasoning
					}
					path := "messages." + strconv.Itoa(i)
					if match && doc.SetRaw(path+".content", entry.content) == nil {
						msg = doc.Get(path)
						restored = true
					}
				}
			}
		}
		prefix = uuid.NewSHA1(prefix, []byte(msg.Raw))
	}
	return &CopilotMessagesCapture{links: c, prefix: uuid.NewSHA1(prefix, sourcePrefix[:])}, restored
}
