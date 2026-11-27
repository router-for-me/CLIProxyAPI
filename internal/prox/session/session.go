// Package session derives conversation identity from the headers and
// metadata CPA passes to scheduler plugins.
//
// The precedence mirrors CPA's session.ExtractSessionInfo with no request
// body (the scheduler never receives one). X-Client-Request-Id is excluded:
// several clients send a fresh value per request, and a request ID must never
// pin a conversation.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/MoeForge/moego/json"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind classifies a session's place in a conversation tree.
type Kind string

const (
	Main     Kind = "main"
	Subagent Kind = "subagent"
	Fork     Kind = "fork"
)

// Ref identifies one session. ID and ParentID are CPA-format identities and
// must not leave the process; only Key and ParentKey are stored or shown.
type Ref struct {
	ID        string
	ParentID  string
	Client    string
	Kind      Kind
	Key       string
	ParentKey string
}

const executionSessionKey = "execution_session_id"
const callerScopeKey = "caller_scope"

// FromRequest extracts the session from scheduler pick headers and metadata.
func FromRequest(h http.Header, meta map[string]any) (Ref, bool) {
	scope, _ := meta[callerScopeKey].(string)
	id, parent, client, fork := extract(h, meta)
	return build(strings.TrimSpace(scope), id, parent, client, fork)
}

// FromUsage rebuilds a Ref from a usage record. The caller scope is derived
// from the client API key exactly as CPA does; the key is not retained.
func FromUsage(sessionID, parentSessionID, apiKey string) (Ref, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || strings.HasPrefix(sessionID, "clientreq:") {
		return Ref{}, false
	}
	client, _, _ := strings.Cut(sessionID, ":")
	return build(CallerScope(apiKey), sessionID, strings.TrimSpace(parentSessionID), client, false)
}

// CallerScope reproduces CPA's per-API-key session namespace.
func CallerScope(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("cli-proxy-api:caller-scope:v1\x00" + apiKey))
	return hex.EncodeToString(sum[:])
}

// Key hashes a session identity within a caller scope.
func Key(scope, id string) string {
	sum := sha256.Sum256([]byte("quota-router:session:v1\x00" + scope + "\x00" + id))
	return hex.EncodeToString(sum[:16])
}

// Short is the redacted display form of a session key.
func Short(key string) string {
	if len(key) > 10 {
		return key[:10]
	}
	return key
}

func build(scope, id, parent, client string, fork bool) (Ref, bool) {
	if id == "" {
		return Ref{}, false
	}
	id = bound(id)
	if parent != "" {
		parent = bound(parent)
	}
	if parent == id {
		parent = ""
	}
	r := Ref{ID: id, ParentID: parent, Client: client, Kind: Main, Key: Key(scope, id)}
	if parent != "" {
		r.ParentKey = Key(scope, parent)
		r.Kind = Subagent
		if fork {
			r.Kind = Fork
		}
	}
	return r, true
}

func extract(h http.Header, meta map[string]any) (id, parent, client string, fork bool) {
	if sid := header(h, "X-Claude-Code-Session-Id"); sid != "" {
		agent := header(h, "X-Claude-Code-Agent-Id")
		parentAgent := header(h, "X-Claude-Code-Parent-Agent-Id")
		if agent != "" && agent != "main" {
			parent = "claude:" + sid
			if parentAgent != "" && parentAgent != "main" && parentAgent != agent {
				parent = "claude:" + sid + ":agent:" + parentAgent
			}
			return "claude:" + sid + ":agent:" + agent, parent, "claude", false
		}
		return "claude:" + sid, "", "claude", false
	}
	if id, parent, fork, ok := codex(h); ok {
		return id, parent, "codex", fork
	}
	for _, g := range generic {
		sid := ""
		for _, name := range g.names {
			if sid = header(h, name); sid != "" {
				break
			}
		}
		if sid == "" {
			continue
		}
		p := ""
		for _, name := range g.parents {
			if p = header(h, name); p != "" {
				break
			}
		}
		id = g.prefix + sid
		if p != "" && p != sid {
			parent = g.prefix + p
		}
		return id, parent, g.client, false
	}
	if raw, ok := meta[executionSessionKey].(string); ok {
		if eid := normalize(raw); eid != "" {
			return "execution:" + eid, "", "websocket", false
		}
	}
	return "", "", "", false
}

var generic = []struct {
	names   []string
	parents []string
	prefix  string
	client  string
}{
	{[]string{"X-Http-Session-Id"}, []string{"X-Parent-Session-ID", "X-Parent-Session-Id", "X-Parent-ID", "X-Parent-Id"}, "agy:", "agy"},
	{[]string{"X-Session-ID"}, []string{"X-Parent-Session-ID", "X-Parent-Session-Id", "X-Parent-ID", "X-Parent-Id"}, "header:", "generic"},
	{[]string{"X-Session-Affinity"}, []string{"X-Parent-Session-Affinity", "X-Parent-Session-ID", "X-Parent-ID", "X-Parent-Id"}, "affinity:", "opencode"},
	{[]string{"X-Slot-Session-Id"}, []string{"X-Parent-Slot-Session-Id", "X-Parent-Session-ID", "X-Parent-Session-Id", "X-Parent-ID", "X-Parent-Id"}, "slot:", "pi"},
	{[]string{"X-Task-ID", "X-Task-Id", "X-Task_ID"}, []string{"X-Parent-Task-ID", "X-Parent-Task-Id", "X-Parent-Session-ID", "X-Parent-Session-Id", "X-Parent-ID", "X-Parent-Id"}, "task:", "task"},
	{[]string{"X-Conversation-Id"}, []string{"X-Parent-Conversation-Id", "X-Parent-ID"}, "conv:", "conv"},
	{[]string{"X-Thread-Id"}, []string{"X-Parent-Thread-Id", "X-Parent-ID"}, "thread:", "openai-thread"},
}

func codex(h http.Header) (id, parent string, fork, ok bool) {
	sid := header(h, "Session-Id")
	if sid == "" {
		sid = header(h, "Session_id")
	}
	tid := header(h, "Thread-Id")
	if tid == "" {
		tid = header(h, "Thread_id")
	}
	var turn map[string]any
	for k, v := range h {
		if strings.EqualFold(k, "X-Codex-Turn-Metadata") && len(v) > 0 {
			_ = json.Unmarshal([]byte(strings.TrimSpace(v[0])), &turn)
			break
		}
	}
	str := func(key string) string {
		s, _ := turn[key].(string)
		return normalize(s)
	}
	if sid == "" {
		sid = str("session_id")
	}
	if tid == "" {
		tid = str("thread_id")
	}
	if sid == "" && tid == "" {
		return "", "", false, false
	}
	parentThread := header(h, "X-Codex-Parent-Thread-Id")
	if parentThread == "" {
		parentThread = str("parent_thread_id")
	}
	forkedFrom := str("forked_from_thread_id")
	if forkedFrom == "" {
		forkedFrom = str("forked_from_id")
	}
	agentName := ""
	if raw, _ := turn["agent_name"].(string); raw != "" {
		raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(raw, "/root/"), "/"))
		if raw = normalize(raw); raw != "" && raw != "root" && raw != "main" {
			agentName = raw
		}
	}
	sub := header(h, "X-Openai-Subagent")
	subagent := sub != "" && !strings.EqualFold(sub, "false") && sub != "0"
	if kind, _ := turn["subagent_kind"].(string); kind == "thread_spawn" {
		subagent = true
	}
	if forkedFrom != "" {
		forkID := tid
		if forkID == "" {
			forkID = sid
		}
		if forkID == forkedFrom && sid != "" && sid != forkedFrom {
			forkID = sid
		}
		return "codex:" + forkID, "codex:" + forkedFrom, true, true
	}
	if subagent || (tid != "" && sid != "" && tid != sid) || (parentThread != "" && parentThread != tid && parentThread != sid) {
		child := tid
		if child == "" {
			child = sid
		}
		parentID := parentThread
		if parentID == "" {
			parentID = sid
		}
		if agentName != "" && sid != "" {
			id = "codex:" + sid + ":agent:" + agentName
			if parentID != "" {
				parent = "codex:" + parentID
			}
			return id, parent, false, true
		}
		if parentID != "" && parentID != child {
			parent = "codex:" + parentID
		}
		return "codex:" + child, parent, false, true
	}
	id = sid
	if id == "" {
		id = tid
	}
	if parentThread != "" && parentThread != id {
		parent = "codex:" + parentThread
	}
	return "codex:" + id, parent, false, true
}

func header(h http.Header, name string) string {
	if h == nil {
		return ""
	}
	if v := normalize(h.Get(name)); v != "" {
		return v
	}
	for k, vs := range h {
		if !strings.EqualFold(k, name) {
			continue
		}
		for _, v := range vs {
			if n := normalize(v); n != "" {
				return n
			}
		}
	}
	return ""
}

// normalize matches CPA's NormalizeExplicitID: printable, trimmed, ≤ 256 bytes.
func normalize(raw string) string {
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ""
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 256 {
		return ""
	}
	return raw
}

// bound matches CPA's BoundSessionIdentity so keys agree with usage records.
func bound(id string) string {
	if len(id) <= 256 {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	hashHex := hex.EncodeToString(sum[:])
	prefix := id[:min(len(id), 255-1-len(hashHex))]
	for !utf8.ValidString(prefix) && len(prefix) > 0 {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + "#" + hashHex
}
