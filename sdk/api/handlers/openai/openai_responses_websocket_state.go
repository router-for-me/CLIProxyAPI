package openai

import (
	"bytes"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const (
	responsesWebsocketTranscriptStateTTL         = 30 * time.Minute
	responsesWebsocketTranscriptStateMaxSessions = 512
	responsesWebsocketTranscriptStateMaxBytes    = 32 << 20
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

func (s responsesWebsocketTranscriptState) equal(other responsesWebsocketTranscriptState) bool {
	return bytes.Equal(s.lastRequest, other.lastRequest) &&
		bytes.Equal(s.lastResponseOutput, other.lastResponseOutput) &&
		strings.TrimSpace(s.lastResponseID) == strings.TrimSpace(other.lastResponseID) &&
		stringSlicesEqual(s.lastResponsePendingToolCallIDs, other.lastResponsePendingToolCallIDs) &&
		strings.TrimSpace(s.passthroughModelName) == strings.TrimSpace(other.passthroughModelName)
}

func stringSlicesEqual(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}

type responsesWebsocketTranscriptStateCache struct {
	mu          sync.Mutex
	ttl         time.Duration
	maxSessions int
	maxBytes    int
	totalBytes  int
	sessions    map[string]responsesWebsocketTranscriptStateEntry
}

type responsesWebsocketTranscriptStateEntry struct {
	lastSeen time.Time
	size     int
	state    responsesWebsocketTranscriptState
}

func newResponsesWebsocketTranscriptStateCache(ttl time.Duration) *responsesWebsocketTranscriptStateCache {
	if ttl <= 0 {
		ttl = responsesWebsocketTranscriptStateTTL
	}
	return &responsesWebsocketTranscriptStateCache{
		ttl:         ttl,
		maxSessions: responsesWebsocketTranscriptStateMaxSessions,
		maxBytes:    responsesWebsocketTranscriptStateMaxBytes,
		sessions:    make(map[string]responsesWebsocketTranscriptStateEntry),
	}
}

func (c *responsesWebsocketTranscriptStateCache) record(sessionKey string, state responsesWebsocketTranscriptState) {
	sessionKey = strings.TrimSpace(sessionKey)
	if c == nil || sessionKey == "" || !state.valid() {
		return
	}

	now := time.Now()
	cloned := state.clone()
	size := responsesWebsocketTranscriptStateEntrySize(sessionKey, cloned)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupLocked(now)
	if previous, ok := c.sessions[sessionKey]; ok {
		c.totalBytes -= previous.size
		delete(c.sessions, sessionKey)
	}
	if c.maxBytes > 0 && size > c.maxBytes {
		return
	}
	c.sessions[sessionKey] = responsesWebsocketTranscriptStateEntry{
		lastSeen: now,
		size:     size,
		state:    cloned,
	}
	c.totalBytes += size
	c.enforceLimitsLocked()
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
			c.totalBytes -= entry.size
			delete(c.sessions, key)
		}
	}
}

func (c *responsesWebsocketTranscriptStateCache) enforceLimitsLocked() {
	if c == nil {
		return
	}
	for {
		if (c.maxSessions <= 0 || len(c.sessions) <= c.maxSessions) && (c.maxBytes <= 0 || c.totalBytes <= c.maxBytes) {
			return
		}
		var oldestKey string
		var oldestSeen time.Time
		for key, entry := range c.sessions {
			if oldestKey == "" || entry.lastSeen.Before(oldestSeen) {
				oldestKey = key
				oldestSeen = entry.lastSeen
			}
		}
		if oldestKey == "" {
			return
		}
		entry := c.sessions[oldestKey]
		c.totalBytes -= entry.size
		delete(c.sessions, oldestKey)
	}
}

func responsesWebsocketTranscriptStateEntrySize(sessionKey string, state responsesWebsocketTranscriptState) int {
	size := len(sessionKey)
	size += len(state.lastRequest)
	size += len(state.lastResponseOutput)
	size += len(state.lastResponseID)
	size += len(state.passthroughModelName)
	for _, id := range state.lastResponsePendingToolCallIDs {
		size += len(id)
	}
	return size
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
	if strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()) != "" {
		return true
	}
	requestType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if requestType == wsRequestTypeAppend {
		return true
	}
	if requestType != wsRequestTypeCreate {
		return false
	}
	input := gjson.GetBytes(payload, "input")
	if shouldReplaceWebsocketTranscript(payload, input) || inputContainsFullTranscript(input) {
		return false
	}
	return true
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
