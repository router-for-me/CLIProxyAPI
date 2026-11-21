package helps

import (
	"strings"
	"sync/atomic"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Keep Responses item boundaries and opaque history server-side across lossy conversions.
type copilotResponsesEntry struct {
	output    string
	wireKey   uuid.UUID // Independently verify call IDs/arguments on the translated Claude request.
	ambiguous atomic.Bool
}

type CopilotResponsesCapture struct {
	claude    bool
	cache     *CopilotTurnLinks
	prefix    uuid.UUID
	done      map[int]string
	size      int
	itemCount int
}

func responsesSeed(authID string, opts cliproxyexecutor.Options, model, instructions, previous string) uuid.UUID {
	seed := copilotReplaySeed(authID, opts, model)
	if seed == uuid.Nil || previous != "" {
		return uuid.Nil
	}
	return uuid.NewSHA1(seed, []byte(instructions))
}

func (c *CopilotTurnLinks) NewResponsesCapture(authID string, opts cliproxyexecutor.Options, wire ojson.Result) *CopilotResponsesCapture {
	prefix := responsesSeed(authID, opts, wire.Get("model").String(), wire.Get("instructions").Raw, wire.Get("previous_response_id").String())
	if prefix == uuid.Nil || !wire.Get("input").IsArray() {
		return nil
	}
	for _, item := range wire.Get("input").Array() {
		prefix = uuid.NewSHA1(prefix, []byte(item.Raw))
	}
	return &CopilotResponsesCapture{cache: c, prefix: prefix}
}

// Match text and the entire ordered call batch on the Responses wire. Claude
// additionally echoes its actual projected content; retain plain call items
// there too, since tool_use cannot represent their item IDs or statuses.
func responsesAssistantKey(prefix uuid.UUID, items []ojson.Result, keepCallItems bool) (uuid.UUID, bool) {
	var content strings.Builder
	var calls []ojson.Result
	var private bool
	for _, item := range items {
		switch item.Get("type").String() {
		case "reasoning":
			private = true
		case "message":
			if item.Get("role").String() != "assistant" {
				return uuid.Nil, false
			}
			private = private || item.Get("phase").Exists()
			for _, part := range item.Get("content").Array() {
				if part.Get("type").String() != "output_text" {
					return uuid.Nil, false
				}
				content.WriteString(part.Get("text").String())
			}
		case "function_call":
			calls = append(calls, item)
		default:
			return uuid.Nil, false
		}
	}
	if content.Len() == 0 && len(calls) == 0 {
		return uuid.Nil, false
	}
	key := uuid.NewSHA1(prefix, []byte(content.String()))
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		id := call.Get("call_id").String()
		if seen[id] {
			return uuid.Nil, false
		}
		seen[id] = true
		key = copilotCallDigest(key, id, call.Get("name").String(), call.Get("arguments").String())
		if key == uuid.Nil {
			return uuid.Nil, false
		}
	}
	return key, private || keepCallItems && len(calls) > 0
}

func (c *CopilotResponsesCapture) Observe(event, visibleClaudeBlocks ojson.Result) {
	var output ojson.Result
	if index := event.Get("output_index"); index.Type == ojson.Number && index.Int() >= 0 && index.Int() < 128 {
		c.itemCount = max(c.itemCount, int(index.Int())+1)
	} else if index.Exists() {
		c.itemCount = 129
	}
	switch event.Get("type").String() {
	case "response.output_item.done":
		index, item := event.Get("output_index"), event.Get("item")
		if c.size > copilotReplayLimit {
			return
		}
		i := int(index.Int())
		c.size += len(item.Raw)
		if c.done == nil {
			c.done = make(map[int]string)
		}
		if index.Type != ojson.Number || i < 0 || i >= 128 || !item.IsObject() || c.done[i] != "" || c.size > copilotReplayLimit {
			c.done = nil
			c.size = copilotReplayLimit + 1
			return
		}
		c.done[i] = strings.Clone(item.Raw)
		return
	case "response.completed", "response.incomplete":
		output = event.Get("response.output")
	case "":
		output = event.Get("output")
	default:
		return
	}
	if output.Get("#").Int() == 0 && len(c.done) > 0 && len(c.done) == c.itemCount {
		items := make([][]byte, len(c.done))
		for i := range items {
			if c.done[i] == "" {
				return
			}
			items[i] = []byte(c.done[i])
		}
		output = ojson.ParseBytes(common.JoinRawArray(items))
	}
	if !output.IsArray() || len(output.Raw) > copilotReplayLimit {
		return
	}
	items := output.Array()
	key, keepOutput := responsesAssistantKey(c.prefix, items, c.claude)
	entry := &copilotResponsesEntry{}
	if c.claude {
		// Bind the original output to what this client actually received, including
		// the order of summary/text/tool blocks. A partial stream is not an anchor.
		if !visibleClaudeBlocks.IsArray() || len(visibleClaudeBlocks.Raw) > copilotReplayLimit {
			return
		}
		// Two different upstream IDs can sanitize to the same visible ID. Index
		// by the visible reply, not the original call: both siblings must collide.
		entry.wireKey = key
		key = copilotJSONDigest(c.prefix, visibleClaudeBlocks.Raw)
		if entry.wireKey == uuid.Nil || event.Get("type").String() == "response.incomplete" || event.Get("status").String() == "incomplete" || event.Get("response.status").String() == "incomplete" {
			keepOutput = false
		}
	}
	if key == uuid.Nil {
		return
	}
	if keepOutput {
		entry.output = strings.Clone(output.Raw)
	}
	old := c.cache.responses.GetOrAdd(key, func() *copilotResponsesEntry { return entry })
	if old.output != entry.output || old.wireKey != entry.wireKey {
		old.ambiguous.Store(true)
	}
}

// For Messages, bind both the translated wire prefix and the inbound transcript.
// Return a capture at the resulting prefix for a subsequent Responses reply.
func (c *CopilotTurnLinks) RestoreResponsesReplay(doc *ojson.Doc, authID string, opts cliproxyexecutor.Options, claude ojson.Result) (*CopilotResponsesCapture, bool) {
	prefix := responsesSeed(authID, opts, doc.Get("model").String(), doc.Get("instructions").Raw, doc.Get("previous_response_id").String())
	if prefix == uuid.Nil || !doc.Get("input").IsArray() || claude.Exists() && doc.Get("conversation").Exists() {
		return nil, false
	}
	sourcePrefix, anchors := copilotClaudeResponseAnchors(claude)
	isClaude := claude.Exists()
	items := doc.Get("input").Array()
	var rebuilt [][]byte
	for i := 0; i < len(items); {
		end := i + 1
		item := items[i]
		assistant := item.Get("role").String() == "assistant" || item.Get("type").String() == "function_call"
		if isClaude && item.Get("type").String() == "reasoning" {
			assistant = true
		}
		if assistant {
			for end < len(items) && (items[end].Get("type").String() == "function_call" || isClaude && (items[end].Get("role").String() == "assistant" || items[end].Get("type").String() == "reasoning")) {
				end++
			}
		}
		group := items[i:end]
		if assistant {
			base := prefix
			var a copilotClaudeResponseAnchor
			if isClaude {
				if len(anchors) == 0 {
					return nil, false
				}
				a, anchors = anchors[0], anchors[1:]
				base = uuid.NewSHA1(prefix, a.prefix[:])
			}
			key, _ := responsesAssistantKey(base, group, isClaude)
			wireKey := uuid.Nil
			if isClaude && key != uuid.Nil {
				wireKey = key
				if len(a.content.Raw) > copilotReplayLimit || !copilotClaudeResponseBatchMatches(group, items[end:], a.next) {
					key = uuid.Nil
				} else {
					key = copilotJSONDigest(base, a.content.Raw)
				}
			}
			if key != uuid.Nil {
				if entry, ok := c.responses.Get(key); ok && !entry.ambiguous.Load() && entry.output != "" && entry.wireKey == wireKey {
					if rebuilt == nil {
						rebuilt = make([][]byte, 0, len(items))
						for _, prior := range items[:i] {
							rebuilt = append(rebuilt, []byte(prior.Raw))
						}
					}
					group = ojson.Parse(entry.output).Array()
				}
			}
		}
		for _, v := range group {
			prefix = uuid.NewSHA1(prefix, []byte(v.Raw))
			if rebuilt != nil {
				rebuilt = append(rebuilt, []byte(v.Raw))
			}
		}
		i = end
	}
	// An omitted or merged assistant turn makes the mapping ambiguous.
	if len(anchors) != 0 {
		return nil, false
	}
	if rebuilt != nil && doc.SetRawBytes("input", common.JoinRawArray(rebuilt)) != nil {
		return nil, false
	}
	if isClaude {
		return &CopilotResponsesCapture{cache: c, claude: true, prefix: uuid.NewSHA1(prefix, sourcePrefix[:])}, rebuilt != nil
	}
	return nil, rebuilt != nil
}
