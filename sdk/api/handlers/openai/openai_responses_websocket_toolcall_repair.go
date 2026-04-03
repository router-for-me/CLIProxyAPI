package openai

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	websocketToolOutputCacheMaxPerSession = 256
	websocketToolOutputCacheTTL           = 30 * time.Minute
)

var defaultWebsocketToolOutputCache = newWebsocketToolOutputCache(0, websocketToolOutputCacheMaxPerSession)
var defaultWebsocketToolCallCache = newWebsocketToolOutputCache(0, websocketToolOutputCacheMaxPerSession)
var defaultWebsocketToolSessionRefs = newWebsocketToolSessionRefCounter()

type websocketToolOutputCache struct {
	mu            sync.Mutex
	ttl           time.Duration
	maxPerSession int
	sessions      map[string]*websocketToolOutputSession
}

type websocketToolOutputSession struct {
	lastSeen time.Time
	outputs  map[string]json.RawMessage
	order    []string
}

func newWebsocketToolOutputCache(ttl time.Duration, maxPerSession int) *websocketToolOutputCache {
	if ttl < 0 {
		ttl = websocketToolOutputCacheTTL
	}
	if maxPerSession <= 0 {
		maxPerSession = websocketToolOutputCacheMaxPerSession
	}
	return &websocketToolOutputCache{
		ttl:           ttl,
		maxPerSession: maxPerSession,
		sessions:      make(map[string]*websocketToolOutputSession),
	}
}

func (c *websocketToolOutputCache) record(sessionKey string, callID string, item json.RawMessage) {
	sessionKey = strings.TrimSpace(sessionKey)
	callID = strings.TrimSpace(callID)
	if sessionKey == "" || callID == "" || c == nil {
		return
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupLocked(now)

	session, ok := c.sessions[sessionKey]
	if !ok || session == nil {
		session = &websocketToolOutputSession{
			lastSeen: now,
			outputs:  make(map[string]json.RawMessage),
		}
		c.sessions[sessionKey] = session
	}
	session.lastSeen = now

	if _, exists := session.outputs[callID]; !exists {
		session.order = append(session.order, callID)
	}
	session.outputs[callID] = append(json.RawMessage(nil), item...)

	for len(session.order) > c.maxPerSession {
		evict := session.order[0]
		session.order = session.order[1:]
		delete(session.outputs, evict)
	}
}

func (c *websocketToolOutputCache) get(sessionKey string, callID string) (json.RawMessage, bool) {
	sessionKey = strings.TrimSpace(sessionKey)
	callID = strings.TrimSpace(callID)
	if sessionKey == "" || callID == "" || c == nil {
		return nil, false
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupLocked(now)

	session, ok := c.sessions[sessionKey]
	if !ok || session == nil {
		return nil, false
	}
	session.lastSeen = now
	item, ok := session.outputs[callID]
	if !ok || len(item) == 0 {
		return nil, false
	}
	return append(json.RawMessage(nil), item...), true
}

func (c *websocketToolOutputCache) cleanupLocked(now time.Time) {
	if c == nil || c.ttl <= 0 {
		return
	}

	for key, session := range c.sessions {
		if session == nil {
			delete(c.sessions, key)
			continue
		}
		if now.Sub(session.lastSeen) > c.ttl {
			delete(c.sessions, key)
		}
	}
}

func (c *websocketToolOutputCache) deleteSession(sessionKey string) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.sessions, sessionKey)
}

func websocketDownstreamSessionKey(req *http.Request) string {
	if req == nil {
		return ""
	}
	if requestID := strings.TrimSpace(req.Header.Get("X-Client-Request-Id")); requestID != "" {
		return requestID
	}
	if raw := strings.TrimSpace(req.Header.Get("X-Codex-Turn-Metadata")); raw != "" {
		if sessionID := strings.TrimSpace(gjson.Get(raw, "session_id").String()); sessionID != "" {
			return sessionID
		}
	}
	if sessionID := strings.TrimSpace(req.Header.Get("Session_id")); sessionID != "" {
		return sessionID
	}
	return ""
}

type websocketToolSessionRefCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func newWebsocketToolSessionRefCounter() *websocketToolSessionRefCounter {
	return &websocketToolSessionRefCounter{counts: make(map[string]int)}
}

func (c *websocketToolSessionRefCounter) acquire(sessionKey string) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.counts[sessionKey]++
}

func (c *websocketToolSessionRefCounter) release(sessionKey string) bool {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	count := c.counts[sessionKey]
	if count <= 1 {
		delete(c.counts, sessionKey)
		return true
	}
	c.counts[sessionKey] = count - 1
	return false
}

func retainResponsesWebsocketToolCaches(sessionKey string) {
	if defaultWebsocketToolSessionRefs == nil {
		return
	}
	defaultWebsocketToolSessionRefs.acquire(sessionKey)
}

func releaseResponsesWebsocketToolCaches(sessionKey string) {
	if defaultWebsocketToolSessionRefs == nil {
		return
	}
	if !defaultWebsocketToolSessionRefs.release(sessionKey) {
		return
	}

	if defaultWebsocketToolOutputCache != nil {
		defaultWebsocketToolOutputCache.deleteSession(sessionKey)
	}
	if defaultWebsocketToolCallCache != nil {
		defaultWebsocketToolCallCache.deleteSession(sessionKey)
	}
}

func repairResponsesWebsocketToolCalls(sessionKey string, payload []byte) []byte {
	return repairResponsesWebsocketToolCallsWithCaches(defaultWebsocketToolOutputCache, defaultWebsocketToolCallCache, sessionKey, payload)
}

func repairResponsesWebsocketToolCallsWithCache(cache *websocketToolOutputCache, sessionKey string, payload []byte) []byte {
	return repairResponsesWebsocketToolCallsWithCaches(cache, nil, sessionKey, payload)
}

func repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache *websocketToolOutputCache, sessionKey string, payload []byte) []byte {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || outputCache == nil || len(payload) == 0 {
		return payload
	}

	input := gjson.GetBytes(payload, "input")
	if !input.Exists() || !input.IsArray() {
		return payload
	}

	allowOrphanOutputs := strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()) != ""
	updatedRaw, errRepair := repairResponsesToolCallsArray(outputCache, callCache, sessionKey, input.Raw, allowOrphanOutputs)
	if errRepair != nil || updatedRaw == "" || updatedRaw == input.Raw {
		return payload
	}

	updated, errSet := sjson.SetRawBytes(payload, "input", []byte(updatedRaw))
	if errSet != nil {
		return payload
	}
	return updated
}

func repairResponsesToolCallsArray(outputCache, callCache *websocketToolOutputCache, sessionKey string, rawArray string, allowOrphanOutputs bool) (string, error) {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		return "[]", nil
	}

	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &items); errUnmarshal != nil {
		return "", errUnmarshal
	}

	// First pass: record tool outputs and remember which call_ids have outputs in this payload.
	outputPresent := make(map[string]struct{}, len(items))
	callPresent := make(map[string]struct{}, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		switch itemType {
		case "function_call_output":
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if callID == "" {
				continue
			}
			outputPresent[callID] = struct{}{}
			outputCache.record(sessionKey, callID, item)
		case "function_call":
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if callID == "" {
				continue
			}
			callPresent[callID] = struct{}{}
			if callCache != nil {
				callCache.record(sessionKey, callID, item)
			}
		}
	}

	filtered := make([]json.RawMessage, 0, len(items))
	insertedCalls := make(map[string]struct{}, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if itemType == "function_call_output" {
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if callID == "" {
				// Upstream rejects tool outputs without a call_id; drop it.
				continue
			}

			if allowOrphanOutputs {
				filtered = append(filtered, item)
				continue
			}

			if _, ok := callPresent[callID]; ok {
				filtered = append(filtered, item)
				continue
			}

			if callCache != nil {
				if cached, ok := callCache.get(sessionKey, callID); ok {
					if _, already := insertedCalls[callID]; !already {
						filtered = append(filtered, cached)
						insertedCalls[callID] = struct{}{}
						callPresent[callID] = struct{}{}
					}
					filtered = append(filtered, item)
					continue
				}
			}

			// Drop orphaned function_call_output items; upstream rejects transcripts with missing calls.
			continue
		}
		if itemType != "function_call" {
			filtered = append(filtered, item)
			continue
		}

		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		if callID == "" {
			// Upstream rejects tool calls without a call_id; drop it.
			continue
		}

		if _, ok := outputPresent[callID]; ok {
			filtered = append(filtered, item)
			continue
		}

		if cached, ok := outputCache.get(sessionKey, callID); ok {
			filtered = append(filtered, item)
			filtered = append(filtered, cached)
			outputPresent[callID] = struct{}{}
			continue
		}

		// Drop orphaned function_call items; upstream rejects transcripts with missing outputs.
	}

	out, errMarshal := json.Marshal(filtered)
	if errMarshal != nil {
		return "", errMarshal
	}
	return string(out), nil
}

func recordResponsesWebsocketToolCallsFromPayload(sessionKey string, payload []byte) {
	recordResponsesWebsocketToolCallsFromPayloadWithCache(defaultWebsocketToolCallCache, sessionKey, payload)
}

func recordResponsesWebsocketToolCallsFromPayloadWithCache(cache *websocketToolOutputCache, sessionKey string, payload []byte) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || cache == nil || len(payload) == 0 {
		return
	}

	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	switch eventType {
	case "response.completed":
		output := gjson.GetBytes(payload, "response.output")
		if !output.Exists() || !output.IsArray() {
			return
		}
		for _, item := range output.Array() {
			if strings.TrimSpace(item.Get("type").String()) != "function_call" {
				continue
			}
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				continue
			}
			cache.record(sessionKey, callID, json.RawMessage(item.Raw))
		}
	case "response.output_item.added", "response.output_item.done":
		item := gjson.GetBytes(payload, "item")
		if !item.Exists() || !item.IsObject() {
			return
		}
		if strings.TrimSpace(item.Get("type").String()) != "function_call" {
			return
		}
		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID == "" {
			return
		}
		cache.record(sessionKey, callID, json.RawMessage(item.Raw))
	}
}
