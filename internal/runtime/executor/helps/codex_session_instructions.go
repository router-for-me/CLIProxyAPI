package helps

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	codexSessionInstructionMarker      = "cpa_session_instructions"
	codexSessionInstructionPath        = "client_metadata." + codexSessionInstructionMarker
	codexSessionInstructionTTL         = 24 * time.Hour
	codexSessionInstructionMaxBytes    = 32 << 10
	codexSessionInstructionMaxSessions = 512
)

type codexSessionInstructionError struct {
	status  int
	message string
}

func (e *codexSessionInstructionError) Error() string       { return e.message }
func (e *codexSessionInstructionError) StatusCode() int     { return e.status }
func (*codexSessionInstructionError) IsRequestScoped() bool { return true }

var (
	errCodexSessionInstructionMissing = &codexSessionInstructionError{http.StatusConflict, "session instructions are missing or expired; use start with full input and a new session ID"}
	errCodexSessionInstructionStore   = &codexSessionInstructionError{http.StatusServiceUnavailable, "session instructions storage is unavailable"}
	errCodexSessionInstructionStart   = &codexSessionInstructionError{http.StatusBadRequest, "session instructions start requires non-empty instructions to establish a baseline"}
)

type codexSessionInstructionEntry struct {
	text    string
	expires time.Time
}

type codexSessionInstructionStore struct {
	current func() (kvstore.KVClient, bool, error)
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]codexSessionInstructionEntry
}

var codexSessionInstructions = codexSessionInstructionStore{current: kvstore.CurrentKVClient, now: time.Now}

// load never falls back after an enabled KV fails. A start with a candidate uses
// SET NX, and the loser reads the winner without changing its expiration.
func (s *codexSessionInstructionStore) load(ctx context.Context, key, candidate string) (string, error) {
	client, enabled, errClient := s.current()
	if errClient != nil || (enabled && client == nil) {
		return "", errCodexSessionInstructionStore
	}
	if enabled {
		if candidate != "" {
			won, errSet := client.KVSetNX(ctx, key, []byte(candidate), codexSessionInstructionTTL)
			if errSet != nil {
				return "", errCodexSessionInstructionStore
			}
			if won {
				return candidate, nil
			}
		}
		value, found, errGet := client.KVGet(ctx, key)
		if errGet != nil {
			return "", errCodexSessionInstructionStore
		}
		if !found {
			return "", errCodexSessionInstructionMissing
		}
		if len(value) == 0 || len(value) > codexSessionInstructionMaxBytes || !utf8.Valid(value) {
			return "", errCodexSessionInstructionStore
		}
		return string(value), nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if entry, ok := s.entries[key]; ok {
		if now.Before(entry.expires) {
			return entry.text, nil
		}
		delete(s.entries, key)
	}
	if candidate == "" {
		return "", errCodexSessionInstructionMissing
	}
	if len(s.entries) >= codexSessionInstructionMaxSessions {
		for id, entry := range s.entries {
			if !now.Before(entry.expires) {
				delete(s.entries, id)
			}
		}
		if len(s.entries) >= codexSessionInstructionMaxSessions {
			return "", errCodexSessionInstructionStore
		}
	}
	if s.entries == nil {
		s.entries = make(map[string]codexSessionInstructionEntry)
	}
	s.entries[key] = codexSessionInstructionEntry{strings.Clone(candidate), now.Add(codexSessionInstructionTTL)}
	return candidate, nil
}

func codexInstructionKey(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	caller, _ := opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
	if caller == "" {
		caller, _ = req.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
	}
	caller = strings.TrimSpace(caller)
	if caller == "" || len(caller) > 256 {
		return ""
	}
	var sessionID string
	for name, values := range opts.Headers {
		if !strings.EqualFold(name, "Session-Id") && !strings.EqualFold(name, "Session_id") && !strings.EqualFold(name, "X-Session-Id") {
			continue
		}
		for _, value := range values {
			id := session.NormalizeExplicitID(value)
			if id == "" || (sessionID != "" && id != sessionID) {
				return ""
			}
			sessionID = id
		}
	}
	if sessionID == "" {
		return ""
	}
	return "cpa:codex:session-instructions:v1:" + kvstore.HashKeyPart(caller) + ":" + kvstore.HashKeyPart(sessionID)
}

func CodexSessionInstructionErrorStatus(err error) int {
	var sessionErr *codexSessionInstructionError
	if errors.As(err, &sessionErr) {
		return sessionErr.status
	}
	return 0
}

func HasCodexSessionInstructionMarker(doc *ojson.Doc, from sdktranslator.Format) bool {
	return doc != nil && from == sdktranslator.FormatOpenAIResponse && doc.Get(codexSessionInstructionPath).Exists()
}

func codexSessionMarker(root ojson.Result) (marker ojson.Result, ambiguous bool) {
	metadataCount, markerCount := 0, 0
	root.ForEach(func(key, value ojson.Result) bool {
		if key.String() == "client_metadata" {
			metadataCount++
			if value.IsObject() {
				value.ForEach(func(k, v ojson.Result) bool {
					if k.String() == codexSessionInstructionMarker {
						marker, markerCount = v, markerCount+1
					}
					return true
				})
			}
		}
		return true
	})
	return marker, markerCount > 1 || (markerCount > 0 && metadataCount > 1)
}

// StripCodexSessionInstructionMarker is also used for control and summary frames,
// which may never read or create a session baseline.
func StripCodexSessionInstructionMarker(doc *ojson.Doc, from sdktranslator.Format) {
	if HasCodexSessionInstructionMarker(doc, from) {
		_ = doc.Delete(codexSessionInstructionPath)
	}
}

func ValidateCodexSessionInstructionMarker(body []byte, from sdktranslator.Format) error {
	if from == sdktranslator.FormatOpenAIResponse && ojson.GetBytesNoCopy(body, "client_metadata").Exists() {
		if marker, _ := codexSessionMarker(ojson.GetBytesNoCopy(body, "@this")); marker.Exists() {
			return &codexSessionInstructionError{http.StatusBadRequest, "outbound payload contains the reserved session instructions marker"}
		}
	}
	return nil
}

func codexInstructionFieldsUnique(root ojson.Result, fields ...string) bool {
	var seen uint32
	unique := true
	root.ForEach(func(key, _ ojson.Result) bool {
		for i, field := range fields {
			if key.String() == field {
				bit := uint32(1) << i
				if seen&bit != 0 {
					unique = false
					return false
				}
				seen |= bit
				break
			}
		}
		return true
	})
	return unique
}

// PrepareCodexSessionInstructions runs after built-in payload transformations and
// before payload rules. A nil chain means the consumer has no upstream WS proof.
func PrepareCodexSessionInstructions(ctx context.Context, doc *ojson.Doc, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, chain *CodexInstructionChain, conn *websocket.Conn, summary bool) (CodexInstructionTurn, error) {
	var turn CodexInstructionTurn
	if opts.SourceFormat != sdktranslator.FormatOpenAIResponse {
		return turn, nil
	}
	StripCodexSessionInstructionMarker(doc, opts.SourceFormat)
	if summary {
		return turn, nil
	}
	raw := ojson.GetBytesNoCopy(req.Payload, "@this")
	var marker ojson.Result
	if raw.Get("client_metadata").Exists() {
		var ambiguous bool
		marker, ambiguous = codexSessionMarker(raw)
		if ambiguous {
			return turn, codexSessionInstructionPayload()
		}
	}
	mode := marker.String()
	if marker.Exists() && (marker.Type != ojson.String || (mode != "start" && mode != "reuse")) {
		return turn, &codexSessionInstructionError{http.StatusBadRequest, "cpa_session_instructions must be start or reuse"}
	}
	armed, last := chain.parent(conn)
	if mode == "" && !armed {
		return turn, nil
	}
	kind := strings.TrimSpace(raw.Get("type").String())
	if (kind != "" && kind != "response.create" && kind != "response.append") ||
		!codexInstructionFieldsUnique(raw, "type", "previous_response_id", "instructions", "input") {
		return turn, codexSessionInstructionPayload()
	}
	parent := strings.TrimSpace(raw.Get("previous_response_id").String())
	incremental := parent != "" || kind == "response.append"
	if !incremental && len(opts.OriginalRequest) > 0 {
		original := ojson.GetBytesNoCopy(opts.OriginalRequest, "@this")
		if strings.TrimSpace(original.Get("previous_response_id").String()) != "" || strings.TrimSpace(original.Get("type").String()) == "response.append" {
			return turn, codexSessionInstructionsReplay()
		}
	}
	turn.key = codexInstructionKey(req, opts)
	turn.checkParent = true
	if incremental {
		// An implicit append can only target the last proven response on this socket.
		if parent == "" {
			parent = last.id
		}
		if parent == "" || parent != last.id || turn.key != last.key || (last.managed && last.digest == "") || (mode != "" && !last.managed) {
			return turn, codexSessionInstructionsReplay()
		}
		turn.parent, turn.conn = parent, conn
		if doc.Get("previous_response_id").String() != parent {
			_ = doc.Set("previous_response_id", parent)
		}
		if last.managed {
			mode = "reuse"
		}
	}
	if mode == "" {
		return turn, nil
	}
	if turn.key == "" {
		return turn, &codexSessionInstructionError{http.StatusBadRequest, "session instructions require an authenticated caller and an explicit session ID header"}
	}
	turn.managed = true
	value := raw.Get("instructions")
	if inherited := cliproxyexecutor.InheritedWebsocketInstructions(ctx); inherited != "" && value.Raw == inherited {
		value = ojson.Result{}
	}
	if value.Exists() && value.Type != ojson.Null && (value.Type != ojson.String || len(value.String()) > codexSessionInstructionMaxBytes) {
		return turn, &codexSessionInstructionError{http.StatusBadRequest, "instructions must be a string of at most 32 KiB or null in session instructions mode"}
	}
	current := ""
	if value.Type == ojson.String {
		current = value.String()
	}
	if !incremental {
		input := doc.Get("input")
		if input.Exists() && !input.IsArray() {
			return turn, &codexSessionInstructionError{http.StatusBadRequest, "session instructions require Responses string or array input"}
		}
	}
	candidate := ""
	if mode == "start" && !incremental {
		candidate = current
	}
	baseline, errLoad := codexSessionInstructions.load(ctx, turn.key, candidate)
	if errLoad != nil {
		if mode == "start" && candidate == "" && errors.Is(errLoad, errCodexSessionInstructionMissing) {
			return turn, errCodexSessionInstructionStart
		}
		return turn, errLoad
	}
	turn.digest = kvstore.HashKeyPart(baseline)
	if incremental && last.digest != turn.digest {
		return turn, codexSessionInstructionsReplay()
	}
	if current == baseline {
		current = ""
	}
	if errSet := doc.Set("instructions", current); errSet != nil {
		return turn, codexSessionInstructionPayload()
	}
	if !incremental && !codexSessionInstructionPrefix(doc.Get("input.0"), turn.digest) {
		prefix := ojson.NewDocString(`[{"type":"message","role":"developer","content":[{"type":"input_text","text":""}]}]`)
		_ = prefix.Set("0.content.0.text", baseline)
		input := doc.Get("input")
		merged := prefix.String()
		if input.Get("#").Int() > 0 {
			merged = merged[:len(merged)-1] + "," + input.Raw[1:]
		}
		if errSet := doc.SetRaw("input", merged); errSet != nil {
			return turn, codexSessionInstructionPayload()
		}
	}
	return turn, nil
}

func codexSessionInstructionPayload() error {
	return &codexSessionInstructionError{http.StatusBadRequest, "invalid session instructions payload"}
}

func codexSessionInstructionsReplay() error {
	return &cliproxyexecutor.UpstreamWebsocketTurnError{FullContextRequired: true}
}

func codexSessionInstructionPrefix(item ojson.Result, digest string) bool {
	if !codexInstructionFieldsUnique(item, "type", "role", "content") {
		return false
	}
	typ := item.Get("type")
	if (typ.Exists() && typ.String() != "message") || item.Get("role").String() != "developer" {
		return false
	}
	text := item.Get("content")
	if text.IsArray() {
		if text.Get("#").Int() != 1 || text.Get("0.type").String() != "input_text" ||
			!codexInstructionFieldsUnique(text.Get("0"), "type", "text") {
			return false
		}
		text = text.Get("0.text")
	}
	return text.Type == ojson.String && kvstore.HashKeyPart(text.String()) == digest
}

// CodexInstructionTurn carries only proof metadata, never baseline text.
type CodexInstructionTurn struct {
	key, digest, parent  string
	conn                 *websocket.Conn
	managed, checkParent bool
}

// CheckFinal observes actual configured business fields without rewriting them.
func (t *CodexInstructionTurn) CheckFinal(body []byte) error {
	if !t.checkParent {
		return nil
	}
	root := ojson.GetBytesNoCopy(body, "@this")
	if !codexInstructionFieldsUnique(root, "previous_response_id", "input", "type", "instructions") ||
		root.Get("previous_response_id").String() != t.parent {
		return codexSessionInstructionsReplay()
	}
	if t.managed && t.parent == "" && !codexSessionInstructionPrefix(root.Get("input.0"), t.digest) {
		t.digest = ""
	}
	return nil
}

func (t CodexInstructionTurn) ConnectionBound() bool { return t.conn != nil }

func (t CodexInstructionTurn) OnConnection(conn *websocket.Conn) error {
	if t.conn != nil && t.conn != conn {
		return codexSessionInstructionsReplay()
	}
	return nil
}

// LogBody keeps restored baseline text out of upstream request diagnostics.
func (t CodexInstructionTurn) LogBody(body []byte) []byte {
	if t.managed {
		return nil
	}
	return body
}

func (t CodexInstructionTurn) Managed() bool { return t.managed }

func (t CodexInstructionTurn) AllowsControl(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	return codexInstructionFieldsUnique(ojson.GetBytesNoCopy(body, "@this"), "type", "previous_response_id") &&
		(!t.managed || (t.key != "" && t.key == codexInstructionKey(req, opts)))
}

// CodexInstructionChain retains only the most recent reliably correlated success.
// Once a managed frame is sent, unmarked deltas cannot bypass verification by
// changing scope, targeting an old ID, or switching physical connections.
type CodexInstructionChain struct {
	mu     sync.Mutex
	armed  bool
	serial uint64
	last   codexInstructionParent
}

type codexInstructionParent struct {
	conn            *websocket.Conn
	id, key, digest string
	managed         bool
}

func (c *CodexInstructionChain) parent(conn *websocket.Conn) (bool, codexInstructionParent) {
	if c == nil {
		return false, codexInstructionParent{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn == nil || c.last.conn != conn {
		return c.armed, codexInstructionParent{}
	}
	return c.armed, c.last
}

func (c *CodexInstructionChain) Armed() bool {
	armed, _ := c.parent(nil)
	return armed
}

// Sent forgets the previous proof as another turn takes its place. The serial
// prevents a late completion from winning over a more recently submitted turn.
func (c *CodexInstructionChain) Sent(conn *websocket.Conn, turn CodexInstructionTurn) CodexInstructionReceipt {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.armed && !turn.managed {
		return CodexInstructionReceipt{}
	}
	c.armed = true
	c.serial++
	c.last = codexInstructionParent{}
	return CodexInstructionReceipt{chain: c, serial: c.serial, sent: codexInstructionParent{conn: conn, key: turn.key, digest: turn.digest, managed: turn.managed}, parent: turn.parent}
}

type CodexInstructionReceipt struct {
	chain   *CodexInstructionChain
	serial  uint64
	sent    codexInstructionParent
	parent  string
	invalid bool
}

func (r *CodexInstructionReceipt) Observe(payload []byte) {
	if r.chain == nil || r.invalid {
		return
	}
	kind := CodexResponseEventType(payload)
	switch kind {
	case "response.created":
		id := ojson.GetBytesNoCopy(payload, "response.id")
		if r.sent.id != "" || id.Type != ojson.String || id.String() == "" || len(id.String()) > 256 || !r.matchesParent(payload) {
			r.invalid = true
			return
		}
		r.sent.id = strings.Clone(id.String())
	case "response.completed", "response.done":
		id := ojson.GetBytesNoCopy(payload, "response.id")
		if r.sent.id == "" || id.Type != ojson.String || r.sent.id != id.String() || !r.matchesParent(payload) {
			return
		}
		status := ojson.GetBytesNoCopy(payload, "response.status").String()
		if status != "" && status != "completed" {
			return
		}
		r.chain.mu.Lock()
		if r.chain.serial == r.serial {
			r.chain.last = r.sent
		}
		r.chain.mu.Unlock()
	}
}

func (r *CodexInstructionReceipt) matchesParent(payload []byte) bool {
	echo := ojson.GetBytesNoCopy(payload, "response.previous_response_id")
	return !echo.Exists() || echo.String() == r.parent
}
