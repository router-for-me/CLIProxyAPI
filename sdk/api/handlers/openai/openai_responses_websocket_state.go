package openai

import (
	"bytes"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const (
	responsesWebsocketTranscriptStateTTL = 30 * time.Minute
)

var defaultResponsesWebsocketTranscriptStateCache = newResponsesWebsocketTranscriptStateCache(0)

type responsesWebsocketTranscriptState struct {
	lastRequest                    []byte
	lastResponseOutput             []byte
	lastResponseID                 string
	lastResponsePendingToolCallIDs []string
	passthroughModelName           string
}

func (s responsesWebsocketTranscriptState) clone() responsesWebsocketTranscriptState {
	out := responsesWebsocketTranscriptState{
		lastRequest:                    bytes.Clone(s.lastRequest),
		lastResponseOutput:             bytes.Clone(s.lastResponseOutput),
		lastResponseID:                 strings.TrimSpace(s.lastResponseID),
		lastResponsePendingToolCallIDs: append([]string(nil), s.lastResponsePendingToolCallIDs...),
		passthroughModelName:           strings.TrimSpace(s.passthroughModelName),
	}
	if len(out.lastResponseOutput) == 0 {
		out.lastResponseOutput = []byte("[]")
	}
	return out
}

func (s responsesWebsocketTranscriptState) valid() bool {
	return len(s.lastRequest) != 0
}

type responsesWebsocketTranscriptStateCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	sessions map[string]responsesWebsocketTranscriptStateEntry
}

type responsesWebsocketTranscriptStateEntry struct {
	lastSeen time.Time
	state    responsesWebsocketTranscriptState
}

func newResponsesWebsocketTranscriptStateCache(ttl time.Duration) *responsesWebsocketTranscriptStateCache {
	if ttl <= 0 {
		ttl = responsesWebsocketTranscriptStateTTL
	}
	return &responsesWebsocketTranscriptStateCache{
		ttl:      ttl,
		sessions: make(map[string]responsesWebsocketTranscriptStateEntry),
	}
}

func (c *responsesWebsocketTranscriptStateCache) record(sessionKey string, state responsesWebsocketTranscriptState) {
	sessionKey = strings.TrimSpace(sessionKey)
	if c == nil || sessionKey == "" || !state.valid() {
		return
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupLocked(now)
	c.sessions[sessionKey] = responsesWebsocketTranscriptStateEntry{
		lastSeen: now,
		state:    state.clone(),
	}
}

func (c *responsesWebsocketTranscriptStateCache) get(sessionKey string) (responsesWebsocketTranscriptState, bool) {
	sessionKey = strings.TrimSpace(sessionKey)
	if c == nil || sessionKey == "" {
		return responsesWebsocketTranscriptState{}, false
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupLocked(now)
	entry, ok := c.sessions[sessionKey]
	if !ok || !entry.state.valid() {
		return responsesWebsocketTranscriptState{}, false
	}
	entry.lastSeen = now
	c.sessions[sessionKey] = entry
	return entry.state.clone(), true
}

func (c *responsesWebsocketTranscriptStateCache) cleanupLocked(now time.Time) {
	if c == nil || c.ttl <= 0 {
		return
	}
	for key, entry := range c.sessions {
		if now.Sub(entry.lastSeen) > c.ttl {
			delete(c.sessions, key)
		}
	}
}

func recordResponsesWebsocketTranscriptState(sessionKey string, state responsesWebsocketTranscriptState) {
	if defaultResponsesWebsocketTranscriptStateCache == nil {
		return
	}
	defaultResponsesWebsocketTranscriptStateCache.record(sessionKey, state)
}

func restoreResponsesWebsocketTranscriptState(sessionKey string) (responsesWebsocketTranscriptState, bool) {
	if defaultResponsesWebsocketTranscriptStateCache == nil {
		return responsesWebsocketTranscriptState{}, false
	}
	return defaultResponsesWebsocketTranscriptStateCache.get(sessionKey)
}

func responsesWebsocketRequestRequiresUpstreamContext(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == wsRequestTypeAppend {
		return true
	}
	return strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()) != ""
}

type responsesWebsocketUpstreamSessionActiveChecker interface {
	UpstreamSessionActive(sessionID string) bool
}

func (h *OpenAIResponsesAPIHandler) responsesWebsocketUpstreamSessionActive(provider string, sessionID string) (bool, bool) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || h.AuthManager == nil || provider == "" || sessionID == "" {
		return false, false
	}
	exec, ok := h.AuthManager.Executor(provider)
	if !ok || exec == nil {
		return false, false
	}
	checker, ok := exec.(responsesWebsocketUpstreamSessionActiveChecker)
	if !ok || checker == nil {
		return false, false
	}
	return checker.UpstreamSessionActive(sessionID), true
}
