package helps

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	log "github.com/sirupsen/logrus"
)

const (
	claudeThreadBetaLimit  = 1024
	claudeThreadBetaTTL    = cliproxysession.ClaudeThreadStateTTL
	claudeThreadEventLimit = 52_428_800
)

type claudeThreadBetaKey struct {
	scope     string
	messageID string
}

type claudeThreadBetaProfile struct {
	betas     string
	policy    string
	bodyBetas string
}

// ClaudeThreadBetaPolicy separates explicit payload rules from caller declarations.
type ClaudeThreadBetaPolicy struct {
	Fingerprint       string
	BodyBetas         string
	PayloadConfigured bool
}

// ClaudeThreadBetaConflictError requires replay when a fixed Thread profile
// cannot satisfy the current explicit policy or declared protocol capabilities.
type ClaudeThreadBetaConflictError struct{}

func (ClaudeThreadBetaConflictError) Error() string {
	return "The Thread beta profile conflicts with the current header policy or cannot be reconstructed. Replay the full conversation with thread create."
}

func (ClaudeThreadBetaConflictError) StatusCode() int { return http.StatusConflict }

func (ClaudeThreadBetaConflictError) IsRequestScoped() bool { return true }

// ClaudeThreadBetaStore keeps successful Thread wire profiles bounded and shared
// across credential-scoped executor copies. Empty profiles are valid entries.
type ClaudeThreadBetaStore struct {
	mu      sync.Mutex
	entries map[claudeThreadBetaKey]claudeThreadBetaProfile
	order   []claudeThreadBetaKey
}

// ClaudeThreadBetaRequest binds a request to the profile sent to its upstream.
type ClaudeThreadBetaRequest struct {
	store     *ClaudeThreadBetaStore
	scope     string
	betas     string
	policy    string
	bodyBetas string
	onCommit  func(string)
}

// OnCommit registers routing state that must be visible before completion.
func (r *ClaudeThreadBetaRequest) OnCommit(commit func(string)) {
	if r != nil {
		r.onCommit = commit
	}
}

// Bind restores only an actual successful profile, recovering it from the
// configured KV store after memory eviction or restart. Unknown profiles are
// never guessed from the current turn's potentially different capabilities.
func (s *ClaudeThreadBetaStore) Bind(scope, previousMessageID string, header http.Header, policy ClaudeThreadBetaPolicy) (*ClaudeThreadBetaRequest, bool, error) {
	betas := strings.Join(HeaderValuesCaseInsensitive(header, "Anthropic-Beta"), ",")
	bodyBetas := policy.BodyBetas
	if previousMessageID != "" {
		key := claudeThreadBetaKey{scope: scope, messageID: previousMessageID}
		s.mu.Lock()
		previous, ok := s.entries[key]
		s.mu.Unlock()
		if !ok {
			client, kvMode, errClient := kvstore.CurrentKVClient()
			if errClient != nil {
				log.WithError(errClient).Warn("Claude Thread beta store unavailable")
				return nil, false, ClaudeThreadBetaConflictError{}
			}
			if !kvMode || client == nil {
				return nil, false, nil
			}
			raw, found, errGet := client.KVGet(context.Background(), claudeThreadBetaKVKey(key))
			if errGet != nil {
				log.WithError(errGet).Warn("Claude Thread beta profile load failed")
				return nil, false, ClaudeThreadBetaConflictError{}
			}
			if !found {
				return nil, false, nil
			}
			storedPolicy, rest, validPolicy := strings.Cut(string(raw), "\n")
			storedBodyBetas, storedBetas, validBetas := strings.Cut(rest, "\n")
			if !validPolicy || !validBetas || strings.ContainsAny(storedBetas+storedBodyBetas, "\r\n") {
				return nil, false, ClaudeThreadBetaConflictError{}
			}
			previous = claudeThreadBetaProfile{betas: storedBetas, policy: storedPolicy, bodyBetas: storedBodyBetas}
			s.save(key, previous)
		}
		payloadConflict := false
		if policy.PayloadConfigured {
			current := claudeThreadBetaNames(betas)
			frozen := claudeThreadBetaNames(previous.betas)
			for beta := range claudeThreadBetaNames(previous.bodyBetas) {
				payloadConflict = payloadConflict || !current[beta]
			}
			for beta := range claudeThreadBetaNames(policy.BodyBetas) {
				payloadConflict = payloadConflict || !frozen[beta]
			}
		}
		if (previous.policy != policy.Fingerprint && previous.betas != betas) || payloadConflict {
			return nil, true, ClaudeThreadBetaConflictError{}
		}
		betas = previous.betas
		bodyBetas = previous.bodyBetas
		for name := range header {
			if strings.EqualFold(name, "Anthropic-Beta") {
				delete(header, name)
			}
		}
		if betas != "" {
			header.Set("Anthropic-Beta", betas)
		}
	}
	return &ClaudeThreadBetaRequest{store: s, scope: scope, betas: betas, policy: policy.Fingerprint, bodyBetas: bodyBetas}, true, nil
}

// Commit publishes only a successful response ID. The existing KV store keeps
// exact snapshots recoverable across cache eviction and ordinary server restarts.
func (r *ClaudeThreadBetaRequest) Commit(messageID string) {
	if r == nil || messageID == "" {
		return
	}
	key := claudeThreadBetaKey{scope: r.scope, messageID: messageID}
	if r.store.save(key, claudeThreadBetaProfile{betas: r.betas, policy: r.policy, bodyBetas: r.bodyBetas}) {
		// Policy is hex; the delimiters cannot occur in HTTP header values.
		kvstore.KVSetNXBestEffort(context.Background(), claudeThreadBetaKVKey(key), []byte(r.policy+"\n"+r.bodyBetas+"\n"+r.betas), claudeThreadBetaTTL)
	}
	if r.onCommit != nil {
		r.onCommit(messageID)
	}
}

func (s *ClaudeThreadBetaStore) save(key claudeThreadBetaKey, profile claudeThreadBetaProfile) bool {
	key.messageID = strings.Clone(key.messageID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[claudeThreadBetaKey]claudeThreadBetaProfile)
	}
	if _, exists := s.entries[key]; exists {
		return false
	}
	s.entries[key] = profile
	s.order = append(s.order, key)
	if len(s.order) > claudeThreadBetaLimit {
		delete(s.entries, s.order[0])
		s.order = s.order[1:]
	}
	return true
}

func claudeThreadBetaNames(betas string) map[string]bool {
	names := make(map[string]bool)
	for _, beta := range strings.Split(betas, ",") {
		if beta = strings.TrimSpace(beta); beta != "" {
			names[beta] = true
		}
	}
	return names
}

func claudeThreadBetaKVKey(key claudeThreadBetaKey) string {
	return "claude:thread-beta:v1:" + kvstore.HashKeyPart(key.scope) + ":" + kvstore.HashKeyPart(key.messageID)
}

// StreamReader withholds terminal events until message_stop confirms success.
// This is before translation, so even translators that finish on message_delta
// cannot expose completion before the next turn can load its Thread profile.
func (r *ClaudeThreadBetaRequest) StreamReader(body io.Reader, beforeComplete func(string)) io.Reader {
	if r == nil {
		return body
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, claudeThreadEventLimit)
	return &claudeThreadStreamReader{scanner: scanner, commit: func(messageID string) {
		if beforeComplete != nil {
			beforeComplete(messageID)
		}
		r.Commit(messageID)
	}}
}

type claudeThreadStreamReader struct {
	scanner   *bufio.Scanner
	commit    func(string)
	ready     bytes.Buffer
	pending   bytes.Buffer
	messageID string
	failed    bool
	finished  bool
}

func (r *claudeThreadStreamReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for r.ready.Len() == 0 {
		if r.finished {
			return 0, io.EOF
		}
		var event bytes.Buffer
		for r.scanner.Scan() {
			line := r.scanner.Bytes()
			event.Write(line)
			event.WriteByte('\n')
			if event.Len() > claudeThreadEventLimit {
				return 0, errors.New("Claude Thread event exceeds buffer limit")
			}
			if len(bytes.TrimSpace(line)) == 0 {
				break
			}
		}
		if event.Len() == 0 {
			if errScan := r.scanner.Err(); errScan != nil {
				return 0, errScan
			}
			// Never release a completion signal from a truncated Thread turn.
			return 0, io.ErrUnexpectedEOF
		}
		payload := claudeThreadEventPayload(event.Bytes())
		if bytes.HasPrefix(payload, []byte("{")) && !ojson.ValidBytes(payload) {
			return 0, errors.New("malformed Claude Thread stream event")
		}
		if r.pending.Len()+event.Len() > claudeThreadEventLimit {
			return 0, errors.New("Claude Thread completion exceeds buffer limit")
		}
		root := ojson.ParseBytes(payload)
		typ := root.Get("type").String()
		terminal := false
		if _, blocked := clienterror.ClaudePolicyRefusalMessage(payload); blocked {
			r.failed = true
		}
		switch typ {
		case "message_start":
			r.messageID = strings.Clone(root.Get("message.id").String())
		case "message_delta":
			terminal = root.Get("delta.stop_reason").String() != ""
		case "error":
			r.failed = true
			// Drop any speculative completion and let the executor report the error.
			r.pending.Reset()
		case "message_stop":
			r.finished = true
			if !r.failed && r.messageID != "" {
				r.commit(r.messageID)
			}
		case "message":
			if root.Get("stop_reason").String() != "" {
				r.finished = true
				if !r.failed {
					r.commit(root.Get("id").String())
				}
			}
		}
		if terminal || r.pending.Len() > 0 {
			r.pending.Write(event.Bytes())
			if r.finished {
				r.ready.Write(r.pending.Bytes())
				r.pending.Reset()
			}
		} else {
			r.ready.Write(event.Bytes())
		}
	}
	return r.ready.Read(p)
}

func claudeThreadEventPayload(event []byte) []byte {
	var lines [][]byte
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("data:")) {
			lines = append(lines, bytes.TrimSpace(line[len("data:"):]))
		}
	}
	if len(lines) == 0 {
		return bytes.TrimSpace(event)
	}
	return bytes.Join(lines, []byte("\n"))
}
