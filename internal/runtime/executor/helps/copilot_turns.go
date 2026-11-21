package helps

import (
	"slices"
	"sync/atomic"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// CopilotTurnInput distinguishes real user turns from user envelopes carrying
// only tool results. Its digest is from the inbound transcript, before protocol
// translation, cache breakpoints, or payload rules can change the history.
type CopilotTurnInput struct {
	Initiator       string
	history         uuid.UUID
	chatIssuedIDs   map[string]string // Rewritten Chat ID -> the ID actually issued by the model.
	chatWireIDs     []string
	chatRelays      []uuid.UUID // Exact messages generated from the last batch, before user payload rules.
	toolResultIDs   []string    // Call IDs referenced by the current tool results, before translation reorders them.
	systemReminders []string    // Source system messages that Chat may relay after those results.
}

func ParseCopilotTurnInput(source []byte, format translator.Format) CopilotTurnInput {
	root := ojson.ParseBytes(source)
	path := "messages"
	switch format {
	case translator.FormatGemini:
		path = "contents"
	case translator.FormatOpenAIResponse, translator.FormatInteractions:
		path = "input"
	}
	seq := root.Get(path)
	turn := CopilotTurnInput{Initiator: "user"}
	if seq.Type == ojson.String {
		turn.history = uuid.NewSHA1(uuid.Nil, []byte(seq.String()))
		return turn
	}
	seq.ForEach(func(_, msg ojson.Result) bool {
		role := msg.Get("role").String()
		content := msg.Get("content")
		if role == "system" || role == "developer" {
			if format == translator.FormatClaude && role == "system" {
				if reminder, ok := translatorcommon.ClaudeMessageSystemReminderText(content); ok {
					turn.systemReminders = append(turn.systemReminders, reminder)
				}
			}
			return true
		}
		switch format {
		case translator.FormatGemini:
			content = msg.Get("parts")
			if role == "" {
				role = "user"
			}
		case translator.FormatInteractions:
			if role == "" && msg.Get("type").String() != "function_result" {
				role = "user"
			}
		}
		turn.Initiator = "agent"
		if format == translator.FormatOpenAI && role == "tool" {
			turn.toolResultIDs = append(turn.toolResultIDs, msg.Get("tool_call_id").String())
			return true
		}
		if format == translator.FormatOpenAIResponse && isCopilotCallOutput(msg) {
			turn.toolResultIDs = append(turn.toolResultIDs, translatorcommon.ExtractResponsesCallID(msg))
			return true
		}
		if role != "user" {
			turn.toolResultIDs, turn.systemReminders = nil, nil
			return true
		}
		var result, user bool
		var onlyText ojson.Result
		parts := content.Array()
		for _, part := range parts {
			switch {
			case part.Get("type").String() == "tool_result":
				result = true
				if format == translator.FormatClaude {
					turn.toolResultIDs = append(turn.toolResultIDs, part.Get("tool_use_id").String())
				}
			case part.Get("functionResponse").Exists(), part.Get("function_response").Exists():
				result = true
			case part.Get("text").Exists():
				text := part.Get("text")
				user = user || text.String() != ""
				if len(parts) == 1 {
					onlyText = text
				}
			default:
				user = true
			}
		}
		if result && !user {
			return true
		}
		turn.toolResultIDs, turn.systemReminders = nil, nil
		turn.Initiator = "user"
		// Hash user inputs in order. A call ID alone is not unique, and a
		// session-wide "latest turn" would misattribute interleaved branches.
		value := content.Raw
		switch {
		case content.Type == ojson.String:
			value = content.String()
		case onlyText.Type == ojson.String:
			value = onlyText.String()
		case !content.Exists():
			value = msg.Raw
		}
		turn.history = uuid.NewSHA1(turn.history, []byte(value))
		return true
	})
	return turn
}

func isCopilotCallOutput(msg ojson.Result) bool {
	switch msg.Get("type").String() {
	case "function_call_output", "custom_tool_call_output":
		return true
	}
	return false
}

// Inspect only the trailing batch on the final wire. Never walk across a new
// user turn looking for a convenient tool ID. Compare both sides because a
// translator or payload rule may have dropped, inserted, or reordered results.
func (input CopilotTurnInput) wireCalls(format translator.Format, seq ojson.Result) ([]string, bool) {
	items := seq.Array()
	reminders, relays := input.systemReminders, input.chatRelays
	var ids []string
scan:
	for i := len(items) - 1; i >= 0; i-- {
		msg := items[i]
		content := msg.Get("content")
		switch format {
		case translator.FormatOpenAI:
			if msg.Get("role").String() == "user" && len(ids) == 0 {
				if n := len(reminders); n > 0 && ((content.Type == ojson.String && content.String() == reminders[n-1]) || (content.Get("#").Int() == 1 && content.Get("0.type").String() == "text" && content.Get("0.text").String() == reminders[n-1])) {
					reminders = reminders[:n-1]
					continue
				}
				if n := len(relays); n > 0 && uuid.NewSHA1(uuid.Nil, []byte(msg.Raw)) == relays[n-1] {
					relays = relays[:n-1]
					continue
				}
			}
			if msg.Get("role").String() != "tool" {
				break scan
			}
			ids = append(ids, msg.Get("tool_call_id").String())
		case translator.FormatClaude:
			if msg.Get("role").String() != "user" || !content.IsArray() {
				break scan
			}
			var batch []string
			for _, p := range content.Array() {
				if p.Get("type").String() != "tool_result" {
					break scan
				}
				batch = append(batch, p.Get("tool_use_id").String())
			}
			if len(batch) == 0 {
				break scan
			}
			ids = append(ids, batch...)
		case translator.FormatOpenAIResponse:
			if !isCopilotCallOutput(msg) {
				break scan
			}
			ids = append(ids, translatorcommon.ExtractResponsesCallID(msg))
		default:
			break scan
		}
	}
	slices.Sort(ids)
	if format == translator.FormatOpenAI && len(input.chatIssuedIDs) > 0 {
		if !slices.Equal(ids, input.chatWireIDs) {
			return nil, false
		}
		for i, id := range ids {
			if orig := input.chatIssuedIDs[id]; orig != "" {
				ids[i] = orig
			}
		}
		slices.Sort(ids)
	}
	for i, id := range ids {
		if id == "" || (i > 0 && ids[i-1] == id) {
			return nil, false
		}
	}
	if len(input.toolResultIDs) > 0 {
		want := slices.Clone(input.toolResultIDs)
		slices.Sort(want)
		if !slices.Equal(ids, want) {
			return nil, false
		}
	}
	return ids, true
}

// CopilotTurnLinks binds upstream-issued tool calls and response IDs to their
// originating turn. A session can have several pending tool calls or branches;
// its most recent request is not necessarily the parent of a continuation.
type CopilotTurnLinks struct {
	links     *cache.BoundedLRU[uuid.UUID, *copilotTurnLink]
	replay    *cache.BoundedLRU[uuid.UUID, *copilotReplayEntry]
	responses *cache.BoundedLRU[uuid.UUID, *copilotResponsesEntry]
	messages  *cache.BoundedLRU[uuid.UUID, *copilotMessagesEntry]
}

type copilotTurnLink struct {
	turn      CopilotTurn
	ambiguous atomic.Bool
}

func NewCopilotTurnLinks() *CopilotTurnLinks {
	return &CopilotTurnLinks{
		responses: cache.NewBoundedLRU[uuid.UUID, *copilotResponsesEntry](512, nil),
		messages:  cache.NewBoundedLRU[uuid.UUID, *copilotMessagesEntry](512, nil),
		links:     cache.NewBoundedLRU[uuid.UUID, *copilotTurnLink](4096, nil),
		replay:    cache.NewBoundedLRU[uuid.UUID, *copilotReplayEntry](512, nil),
	}
}

type CopilotTurn struct {
	scope, session, interaction, task, history uuid.UUID
}

// Both parent links and private replay data use the same account/caller/session namespace.
func copilotSessionScope(authID string, opts cliproxyexecutor.Options) (uuid.UUID, bool) {
	caller := metadataString(opts.Metadata, cliproxyexecutor.CallerScopeMetadataKey)
	sessionID := metadataString(opts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey)
	if sessionID == "" {
		sessionID = metadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
	}
	scope := uuid.NewSHA1(uuid.NameSpaceURL, []byte(authID+"\x00"+caller+"\x00"+sessionID))
	if sessionID != "" {
		return scope, true
	}
	if derived := DerivedSessionID(opts.Metadata); derived != "" {
		return uuid.NewSHA1(scope, []byte(derived)), true
	}
	return scope, false
}

func (c *CopilotTurnLinks) Resolve(authID string, input CopilotTurnInput, opts cliproxyexecutor.Options, format translator.Format, body ojson.Result) CopilotTurn {
	scope, stable := copilotSessionScope(authID, opts)
	if input.Initiator == "agent" {
		path := "messages"
		if format == translator.FormatOpenAIResponse {
			path = "input"
		}
		if ids, valid := input.wireCalls(format, body.Get(path)); valid {
			previous := ""
			if format == translator.FormatOpenAIResponse {
				previous = body.Get("previous_response_id").String()
			}
			if turn, ok := c.turnForCalls(scope, input.history, ids, previous); ok {
				return turn
			}
		}
	}
	session := scope
	if !stable {
		session = uuid.New()
	}
	return CopilotTurn{scope: scope, session: session, history: input.history, interaction: uuid.New(), task: uuid.New()}
}

// A known previous response can cover a cache miss, but cannot override a
// conflicting or ambiguous call. Without that anchor every ID must resolve.
func (c *CopilotTurnLinks) turnForCalls(scope, history uuid.UUID, ids []string, previous string) (CopilotTurn, bool) {
	var parent CopilotTurn
	explicit := false
	if previous != "" {
		if link, ok := c.links.Get(uuid.NewSHA1(scope, []byte("response:"+previous))); ok {
			if link.ambiguous.Load() {
				return CopilotTurn{}, false
			}
			parent = link.turn
			explicit = true
		}
	}
	for _, id := range ids {
		key := uuid.NewSHA1(scope, []byte("call:"+id))
		var link *copilotTurnLink
		if history != uuid.Nil {
			link, _ = c.links.Get(uuid.NewSHA1(key, history[:]))
		}
		if link == nil {
			link, _ = c.links.Get(key)
		}
		if link == nil && explicit {
			continue
		}
		if link == nil || link.ambiguous.Load() || (parent.interaction != uuid.Nil && parent != link.turn) {
			return CopilotTurn{}, false
		}
		parent = link.turn
	}
	return parent, parent.interaction != uuid.Nil
}

// Observe uses the same wire protocol as Resolve, before translating IDs back
// to the caller. Only opaque, scoped identifiers are retained, never payloads.
func (c *CopilotTurnLinks) Observe(turn CopilotTurn, format translator.Format, root ojson.Result) {
	remember := func(key uuid.UUID) {
		link := c.links.GetOrAdd(key, func() *copilotTurnLink { return &copilotTurnLink{turn: turn} })
		if link.turn != turn {
			link.ambiguous.Store(true)
		}
	}
	bind := func(kind, id string) {
		if id == "" {
			return
		}
		key := uuid.NewSHA1(turn.scope, []byte(kind+id))
		if kind == "call:" && turn.history != uuid.Nil {
			remember(uuid.NewSHA1(key, turn.history[:]))
		}
		remember(key)
	}
	switch format {
	case translator.FormatOpenAI:
		for _, choice := range root.Get("choices").Array() {
			message := choice.Get("delta")
			if !message.Exists() {
				message = choice.Get("message")
			}
			for _, call := range message.Get("tool_calls").Array() {
				bind("call:", call.Get("id").String())
			}
		}
	case translator.FormatClaude:
		if block := root.Get("content_block"); block.Get("type").String() == "tool_use" {
			bind("call:", block.Get("id").String())
		}
		for _, block := range root.Get("content").Array() {
			if block.Get("type").String() == "tool_use" {
				bind("call:", block.Get("id").String())
			}
		}
	case translator.FormatOpenAIResponse:
		switch root.Get("type").String() {
		case "response.output_item.added", "response.output_item.done":
			bind("call:", root.Get("item.call_id").String())
			return
		case "response.completed", "response.incomplete":
			root = root.Get("response")
		case "": // Non-streaming response object.
		default:
			return
		}
		bind("response:", root.Get("id").String())
		for _, item := range root.Get("output").Array() {
			bind("call:", item.Get("call_id").String())
		}
	}
}
