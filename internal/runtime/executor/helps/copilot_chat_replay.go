package helps

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Copilot Chat has replay-only fields with no equivalent in Messages or
// Responses. Never export opaque state as visible reasoning or as a foreign
// signature. Keep it in bounded process memory and bind it to the actual
// assistant call batch AND the prefix at which the upstream issued it.
const copilotReplayLimit = 128 << 10

var chatReasoningKeys = [...]string{"reasoning_text", "reasoning_content", "reasoning_opaque"}

type copilotReplayEntry struct {
	reasoning          [3]string
	signatures         []string
	arguments          []string
	ambiguous          atomic.Bool
	argumentsAmbiguous atomic.Bool
}

func copilotReplaySeed(authID string, opts cliproxyexecutor.Options, model string) uuid.UUID {
	scope, stable := copilotSessionScope(authID, opts)
	if !stable || authID == "" || model == "" {
		return uuid.Nil
	}
	return uuid.NewSHA1(scope, []byte(model))
}

// Digest JSON arguments semantically: a Claude client can reserialize objects
// and change whitespace/key order. Preserve numbers without float rounding.
func copilotCallDigest(prefix uuid.UUID, id, name, args string) uuid.UUID {
	if id == "" || name == "" {
		return uuid.Nil
	}
	for _, part := range []string{id, name} {
		prefix = uuid.NewSHA1(prefix, []byte(part))
	}
	return copilotJSONDigest(prefix, args)
}

func copilotJSONDigest(prefix uuid.UUID, raw string) uuid.UUID {
	if !ojson.Valid(raw) {
		return uuid.Nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return uuid.Nil
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return uuid.Nil
	}
	return uuid.NewSHA1(prefix, canonical)
}

func (entry *copilotReplayEntry) restore(doc *ojson.Doc, i int, fromClaude bool) {
	p := "messages." + strconv.Itoa(i) + "."
	// A cross-protocol translator may have copied our visible reasoning_text
	// into reasoning_content. Restore the original representation, but do not
	// replace independently supplied, different reasoning text.
	if text := entry.reasoning[0]; text != "" && doc.Get(p+"reasoning_content").String() == text {
		_ = doc.Delete(p + "reasoning_content")
	}
	for j, k := range chatReasoningKeys {
		if entry.reasoning[j] != "" && !doc.Get(p+k).Exists() {
			_ = doc.Set(p+k, entry.reasoning[j])
		}
	}
	// The digest already verified these arguments semantically. Restore the
	// model's spelling, which can be lost in a Claude tool_use.input object.
	if fromClaude && !entry.argumentsAmbiguous.Load() {
		for j, args := range entry.arguments {
			_ = doc.Set(p+"tool_calls."+strconv.Itoa(j)+".function.arguments", args)
		}
	}
	for j, sig := range entry.signatures {
		dest := p + "tool_calls." + strconv.Itoa(j) + ".extra_content.google.thought_signature"
		if sig != "" && !doc.Get(dest).Exists() {
			_ = doc.Set(dest, sig)
		}
	}
}

func (c *CopilotTurnLinks) chatPrefix(seed uuid.UUID, messages ojson.Result, restore *ojson.Doc, issuedIDs map[string]string, fromClaude bool) (uuid.UUID, bool) {
	var restored bool
	if seed == uuid.Nil {
		return uuid.Nil, false
	}
	prefix := seed
	for i, msg := range messages.Array() {
		role := msg.Get("role").String()
		content := msg.Get("content")
		value := content.Raw
		if content.Type == ojson.String {
			value = content.String()
		} else if content.Get("#").Int() == 1 && content.Get("0.type").String() == "text" {
			value = content.Get("0.text").String()
		} else if content.Type == ojson.Null {
			value = ""
		}
		calls := msg.Get("tool_calls").Array()
		prefix = uuid.NewSHA1(prefix, []byte(role))
		prefix = uuid.NewSHA1(prefix, []byte(value))
		if role == "assistant" && (len(calls) > 0 || value != "") {
			key, wireKey := prefix, prefix
			for _, call := range calls {
				id, name, args := call.Get("id").String(), call.Get("function.name").String(), call.Get("function.arguments").String()
				original := issuedIDs[id]
				if original == "" {
					original = id
				}
				before := key
				key = copilotCallDigest(key, original, name, args)
				if id == original && before == wireKey {
					wireKey = key
				} else {
					wireKey = copilotCallDigest(wireKey, id, name, args)
				}
				if key == uuid.Nil || wireKey == uuid.Nil {
					return uuid.Nil, restored
				}
			}
			if restore != nil {
				if rec, ok := c.replay.Get(key); ok && !rec.ambiguous.Load() && len(calls) == len(rec.signatures) {
					rec.restore(restore, i, fromClaude)
					restored = true
				}
			}
			prefix = wireKey
		}
		if role == "tool" {
			prefix = uuid.NewSHA1(prefix, []byte(msg.Get("tool_call_id").String()))
		}
	}
	return prefix, restored
}

func (c *CopilotTurnLinks) RestoreChatReplay(doc *ojson.Doc, authID string, opts cliproxyexecutor.Options, input CopilotTurnInput) bool {
	_, restored := c.chatPrefix(copilotReplaySeed(authID, opts, doc.Get("model").String()), doc.Get("messages"), doc, input.chatIssuedIDs, opts.SourceFormat == translator.FormatClaude)
	return restored
}

// CopilotChatCapture belongs to one response. Tool index is only meaningful
// within its choice; signatures can arrive after the ID in a separate delta.
type CopilotChatCapture struct {
	cache   *CopilotTurnLinks
	prefix  uuid.UUID
	choices map[int]*copilotReplayChoice
	bytes   int
}

type copilotReplayChoice struct {
	content   strings.Builder
	reasoning [3]strings.Builder
	calls     map[int]*copilotReplayCall
}

type copilotReplayCall struct{ id, name, args, sig strings.Builder }

func (c *CopilotTurnLinks) NewChatCapture(authID string, opts cliproxyexecutor.Options, wire ojson.Result) *CopilotChatCapture {
	prefix, _ := c.chatPrefix(copilotReplaySeed(authID, opts, wire.Get("model").String()), wire.Get("messages"), nil, nil, false)
	if prefix == uuid.Nil {
		return nil
	}
	return &CopilotChatCapture{cache: c, prefix: prefix, choices: make(map[int]*copilotReplayChoice)}
}

func (c *CopilotChatCapture) Observe(root ojson.Result) {
	if c == nil || c.choices == nil {
		return
	}
	for _, choice := range root.Get("choices").Array() {
		idx := int(choice.Get("index").Int())
		if idx < 0 || idx >= 16 {
			continue
		}
		state := c.choices[idx]
		if state == nil {
			state = &copilotReplayChoice{calls: make(map[int]*copilotReplayCall)}
			c.choices[idx] = state
		}
		m := choice.Get("delta")
		flat := !m.Exists()
		if flat {
			m = choice.Get("message")
		}
		appendValue := func(dst *strings.Builder, v ojson.Result) {
			if v.Type == ojson.String {
				s := v.String()
				c.bytes += len(s)
				if c.bytes <= copilotReplayLimit {
					dst.WriteString(s)
				}
			}
		}
		content := m.Get("content")
		if content.Type != ojson.String && content.Type != ojson.Null {
			c.choices = nil
			return
		} // Unsupported content must not match an empty assistant message.
		appendValue(&state.content, content)
		for j, k := range chatReasoningKeys {
			appendValue(&state.reasoning[j], m.Get(k))
		}
		for i, call := range m.Get("tool_calls").Array() {
			if !flat {
				if call.Get("index").Type != ojson.Number {
					c.choices = nil
					return
				}
				i = int(call.Get("index").Int())
			}
			if i < 0 || i >= 128 {
				c.choices = nil
				return
			}
			p := state.calls[i]
			if p == nil {
				p = &copilotReplayCall{}
				state.calls[i] = p
			}
			appendValue(&p.id, call.Get("id"))
			appendValue(&p.name, call.Get("function.name"))
			appendValue(&p.args, call.Get("function.arguments"))
			appendValue(&p.sig, call.Get("extra_content.google.thought_signature"))
		}
		if c.bytes > copilotReplayLimit {
			c.choices = nil
			return
		}
		if flat || choice.Get("finish_reason").String() != "" {
			c.commit(state)
			delete(c.choices, idx)
		}
	}
}

func (c *CopilotChatCapture) commit(s *copilotReplayChoice) {
	if len(s.calls) == 0 && s.content.Len() == 0 {
		return
	}
	entry := &copilotReplayEntry{signatures: make([]string, len(s.calls)), arguments: make([]string, len(s.calls))}
	for i := range s.reasoning {
		entry.reasoning[i] = s.reasoning[i].String()
	}
	key := uuid.NewSHA1(c.prefix, []byte("assistant"))
	key = uuid.NewSHA1(key, []byte(s.content.String()))
	seen := make(map[string]bool, len(s.calls))
	for i := range len(s.calls) {
		p := s.calls[i]
		if p == nil || seen[p.id.String()] {
			return
		}
		seen[p.id.String()] = true
		key = copilotCallDigest(key, p.id.String(), p.name.String(), p.args.String())
		if key == uuid.Nil {
			return
		}
		entry.signatures[i] = p.sig.String()
		entry.arguments[i] = p.args.String()
	}
	old := c.cache.replay.GetOrAdd(key, func() *copilotReplayEntry { return entry })
	if old.reasoning != entry.reasoning || !slices.Equal(old.signatures, entry.signatures) {
		old.ambiguous.Store(true)
	}
	if !slices.Equal(old.arguments, entry.arguments) {
		old.argumentsAmbiguous.Store(true)
	}
}

// Expose only the human-readable alias to cross-protocol translators. Keep
// the unmodified upstream event for replay capture, usage and native clients.
func CopilotVisibleChatReasoning(data []byte, stream bool) []byte {
	if !bytes.Contains(data, []byte(`"reasoning_text"`)) {
		return data
	}
	node := "message"
	if stream {
		node = "delta"
	}
	var doc *ojson.Doc
	for i, ch := range ojson.GetBytesNoCopy(data, "choices").Array() {
		v := ch.Get(node + ".reasoning_text")
		if v.Type != ojson.String || v.String() == "" {
			continue
		}
		if doc == nil {
			doc = ojson.NewDoc(data)
		}
		_ = doc.Set("choices."+strconv.Itoa(i)+"."+node+".reasoning_content", v.String())
	}
	if doc == nil {
		return data
	}
	return doc.Bytes()
}
