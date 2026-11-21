package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"

	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coresession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const codexHTTPMessagesLimit = 256

var errCodexHTTPMessagesParentMissing = errors.New("Codex rejected the generated previous_response_id")

func codexHTTPMessagesParentMissing(payload []byte) bool {
	status := codexWebsocketErrorStatus(payload)
	if status != http.StatusBadRequest && status != http.StatusNotFound && status != 0 {
		return false
	}
	for _, path := range []string{"error.code", "body.error.code"} {
		if ojson.GetBytesNoCopy(payload, path).String() == "previous_response_not_found" {
			return true
		}
	}
	return false
}

func codexHTTPMessagesUpgradeUnsupported(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	status := resp.StatusCode
	return status >= 200 && status < 300 || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented
}

// The lease fields are protected by the WebSocket store mutex; the continuation
// fields are used only by the lease owner and committed before the stream closes.
type codexHTTPMessagesSession struct {
	closed       bool     // protected by the connection mutex
	handshake    [32]byte // protected by the connection mutex
	busy         bool
	continuation codexHTTPMessagesContinuation
}

type codexHTTPMessagesContinuation struct {
	conn *websocket.Conn
	id   string

	shape  [32]byte
	count  int
	prefix [32]byte
	output [][32]byte
	calls  int
}

type codexHTTPMessagesTurn struct {
	input []ojson.Result
	shape [32]byte
}

func (e *CodexWebsocketsExecutor) serveHTTPMessages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error, bool) {
	if e.cfg == nil || !e.cfg.Codex.ServeHTTPMessagesOverWebsockets || !codexWebsocketsEnabled(auth) ||
		auth.ID == "" || opts.SourceFormat != sdktranslator.FormatClaude || cliproxyexecutor.ResponseFormatOrSource(opts) != sdktranslator.FormatClaude ||
		opts.Alt != "" || opts.ExecutionLifecycle != nil || e.cfg.Home.Enabled {
		return nil, nil, false
	}
	scope, _ := opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
	payload := opts.OriginalRequest
	if len(payload) == 0 {
		payload = req.Payload
	}
	// Do not use prompt_cache_key, a content-derived LCP ID, or an arbitrary
	// user ID as a connection identity; those can be shared across chats.
	sid := coresession.ClaudeMetadataSessionID(payload)
	for _, header := range []string{helps.ClaudeCodeSessionHeader, "X-Session-Id", "Session-Id", "Session_id"} {
		if explicit := coresession.NormalizeExplicitID(helps.HeaderValueCaseInsensitive(opts.Headers, header)); explicit != "" {
			sid = explicit
			break
		}
	}
	if scope == "" || sid == "" {
		return nil, nil, false
	}
	agent := coresession.ClaudeAgentID(opts.Headers, payload)
	key := "http-messages:" + codexHTTPMessagesSessionKey(scope, sid, agent, auth.ID, req.Model)
	sess, release := e.leaseHTTPMessagesSession(key, auth.ID)
	if sess == nil {
		return nil, nil, false
	}
	res, err := e.executeStream(ctx, auth, req, opts, sess, release)
	if cliproxyexecutor.IsUpstreamWebsocketReplayRequired(err) || errors.Is(err, errCodexHTTPMessagesParentMissing) {
		return nil, nil, false // HTTP sends the original full history after a definite rejection or no write.
	}
	return res, err, true
}

func codexHTTPMessagesSessionKey(values ...string) string {
	h := sha256.New()
	for _, v := range values {
		_, _ = h.Write(strconv.AppendInt(nil, int64(len(v)), 10))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(v))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (e *CodexWebsocketsExecutor) leaseHTTPMessagesSession(key, authID string) (*codexWebsocketSession, func()) {
	store := e.sessionStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.sessions == nil {
		store.sessions = make(map[string]*codexWebsocketSession)
	}
	sess := store.sessions[key]
	if sess == nil {
		count := 0
		for _, other := range store.sessions {
			if other != nil && other.httpMessages != nil {
				count++
			}
		}
		if count >= codexHTTPMessagesLimit {
			return nil, nil
		}
		sess = &codexWebsocketSession{sessionID: key, authID: authID, upstreamDisconnectCh: make(chan error, 1), httpMessages: &codexHTTPMessagesSession{}}
		store.sessions[key] = sess
	}
	state := sess.httpMessages
	if state == nil || state.busy {
		return nil, nil
	}
	state.busy = true
	return sess, func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.sessions[key] != sess {
			return
		}
		sess.connMu.Lock()
		sess.turnContext = nil
		open := sess.conn != nil
		sess.connMu.Unlock()
		if !open {
			delete(store.sessions, key)
			return
		}
		state.busy = false
	}
}

// A disconnected idle session no longer needs a pool entry. If a leased turn
// still owns it, its release is responsible for removing the dead entry.
func (e *CodexWebsocketsExecutor) removeDisconnectedHTTPMessagesSession(sess *codexWebsocketSession) {
	store := e.sessionStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.sessions[sess.sessionID] != sess || sess.httpMessages.busy {
		return
	}
	sess.connMu.Lock()
	defer sess.connMu.Unlock()
	if sess.conn == nil {
		delete(store.sessions, sess.sessionID)
	}
}

// Digest the effective headers, not user-provided aliases or raw credential material.
func codexHTTPMessagesHandshake(headers http.Header) [32]byte {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		_, _ = io.WriteString(h, key)
		_, _ = h.Write([]byte{0})
		for _, value := range headers[key] {
			_, _ = io.WriteString(h, value)
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte{0})
	}
	var digest [32]byte
	h.Sum(digest[:0])
	return digest
}

func (s *codexWebsocketSession) matchesHTTPMessagesHandshake(conn *websocket.Conn, digest [32]byte) bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conn == conn && !s.httpMessages.closed && s.httpMessages.handshake == digest
}

func prepareCodexHTTPMessagesTurn(doc *ojson.Doc, cfg *config.Config) (*codexHTTPMessagesTurn, error) {
	// Only called for requests without user payload rules.
	if doc.Get("previous_response_id").Exists() {
		return nil, nil
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return nil, nil
	}
	// Hash the envelope without materializing the complete outgoing request.
	// Restore input on the same Doc before matching or finalizing it.
	if err := doc.Delete("input"); err != nil {
		return nil, err
	}
	hash := sha256.New()
	_, _ = fmt.Fprintln(hash, cfg.DisableImageGeneration)
	if _, err := doc.WriteTo(hash); err != nil {
		return nil, err
	}
	if err := doc.SetRaw("input", input.Raw); err != nil {
		return nil, err
	}
	var shape [32]byte
	hash.Sum(shape[:0])
	return &codexHTTPMessagesTurn{input: input.Array(), shape: shape}, nil
}

func hashCodexHTTPMessagesInput(input []ojson.Result) [32]byte {
	h := sha256.New()
	for _, item := range input {
		_, _ = io.WriteString(h, item.Raw)
		_, _ = h.Write([]byte{0})
	}
	var sum [32]byte
	h.Sum(sum[:0])
	return sum
}

// Match only output types with an unambiguous replay in streaming Messages.
func codexHTTPMessagesOutput(item ojson.Result) (digest [32]byte, call bool, ok bool) {
	switch item.Get("type").String() {
	case "function_call":
		callID := item.Get("call_id").String()
		name := item.Get("name").String()
		args := item.Get("arguments").String()
		if callID == "" || name == "" || args == "" || strings.ContainsRune(callID, 0) || strings.ContainsRune(name, 0) {
			return digest, false, false
		}
		return sha256.Sum256([]byte("call\x00" + callID + "\x00" + name + "\x00" + args)), true, true
	case "reasoning":
		sig := item.Get("encrypted_content").String()
		if sig != "" {
			return sha256.Sum256([]byte("reasoning\x00" + sig)), false, true
		}
	}
	return digest, false, false
}

func (t *codexHTTPMessagesTurn) apply(doc *ojson.Doc, previous codexHTTPMessagesContinuation, conn *websocket.Conn) bool {
	if t == nil || conn == nil || previous.conn != conn || previous.shape != t.shape || previous.calls == 0 || len(t.input) != previous.count+len(previous.output)+previous.calls ||
		hashCodexHTTPMessagesInput(t.input[:previous.count]) != previous.prefix {
		return false
	}
	n := previous.count
	outputs := make(map[string]bool, previous.calls)
	for i, want := range previous.output {
		item := t.input[n+i]
		got, call, ok := codexHTTPMessagesOutput(item)
		if !ok || got != want {
			return false
		}
		if call {
			outputs[item.Get("call_id").String()] = true
		}
	}
	n += len(previous.output)
	var suffix strings.Builder
	suffix.WriteByte('[')
	for i, item := range t.input[n:] {
		if item.Get("type").String() != "function_call_output" {
			return false
		}
		id := item.Get("call_id").String()
		if !outputs[id] {
			return false
		}
		delete(outputs, id)
		if i > 0 {
			suffix.WriteByte(',')
		}
		suffix.WriteString(item.Raw)
	}
	if len(outputs) != 0 {
		return false
	}
	suffix.WriteByte(']')
	_ = doc.SetRaw("input", suffix.String())
	_ = doc.Set("previous_response_id", previous.id)
	return true
}

func (t *codexHTTPMessagesTurn) completed(conn *websocket.Conn, payload []byte) codexHTTPMessagesContinuation {
	if t == nil {
		return codexHTTPMessagesContinuation{}
	}
	root := ojson.ParseBytes(payload)
	id := root.Get("response.id").String()
	if id == "" {
		return codexHTTPMessagesContinuation{}
	}
	items := root.Get("response.output").Array()
	output := make([][32]byte, 0, len(items))
	calls := 0
	for _, item := range items {
		digest, call, ok := codexHTTPMessagesOutput(item)
		if !ok {
			return codexHTTPMessagesContinuation{}
		}
		output = append(output, digest)
		if call {
			calls++
		}
	}
	if calls == 0 {
		return codexHTTPMessagesContinuation{}
	}
	return codexHTTPMessagesContinuation{conn: conn, id: strings.Clone(id), shape: t.shape, count: len(t.input), prefix: hashCodexHTTPMessagesInput(t.input), output: output, calls: calls}
}
