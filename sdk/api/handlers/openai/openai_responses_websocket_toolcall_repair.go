package openai

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

const (
	websocketToolOutputCacheMaxPerSession = 256
	websocketToolOutputCacheTTL           = 30 * time.Minute
)

var defaultWebsocketToolOutputCache = newWebsocketToolOutputCache(0, websocketToolOutputCacheMaxPerSession)
var defaultWebsocketToolCallCache = newWebsocketToolOutputCache(0, websocketToolOutputCacheMaxPerSession)
var defaultWebsocketToolSessionRefs = newWebsocketToolSessionRefCounter()
var defaultWebsocketToolCacheTransactionMu sync.RWMutex

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

type responsesWebsocketToolCacheTurn struct {
	sessionKey  string
	outputs     map[string]json.RawMessage
	outputOrder []string
	calls       map[string]json.RawMessage
	callOrder   []string
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
	callID = strings.Clone(strings.TrimSpace(callID))
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
		session.order[0] = ""
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
	if sessionID := strings.TrimSpace(req.Header.Get("Session-Id")); sessionID != "" {
		return sessionID
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
	defaultWebsocketToolCacheTransactionMu.Lock()
	defer defaultWebsocketToolCacheTransactionMu.Unlock()
	if defaultWebsocketToolSessionRefs == nil {
		return
	}
	defaultWebsocketToolSessionRefs.acquire(sessionKey)
}

func releaseResponsesWebsocketToolCaches(sessionKey string) {
	defaultWebsocketToolCacheTransactionMu.Lock()
	defer defaultWebsocketToolCacheTransactionMu.Unlock()
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

func newResponsesWebsocketToolCacheTurn(sessionKey string) *responsesWebsocketToolCacheTurn {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil
	}
	return &responsesWebsocketToolCacheTurn{
		sessionKey: sessionKey,
		outputs:    make(map[string]json.RawMessage),
		calls:      make(map[string]json.RawMessage),
	}
}

func (t *responsesWebsocketToolCacheTurn) recordResponse(payload []byte) {
	if t == nil || len(payload) == 0 {
		return
	}
	switch strings.TrimSpace(util.GetGJSONBytesNoCopy(payload, "type").String()) {
	case "response.completed":
		output := util.GetGJSONBytesNoCopy(payload, "response.output")
		if !output.Exists() || !output.IsArray() {
			return
		}
		output.ForEach(func(_, item gjson.Result) bool {
			if isCompleteResponsesWebsocketToolCall(item) {
				t.recordItem(payload, item)
			}
			return true
		})
	case "response.output_item.added", "response.output_item.done":
		item := util.GetGJSONBytesNoCopy(payload, "item")
		if isCompleteResponsesWebsocketToolCall(item) {
			t.recordItem(payload, item)
		}
	}
}

func (t *responsesWebsocketToolCacheTurn) recordItem(payload []byte, item gjson.Result) {
	if t == nil || !item.Exists() {
		return
	}
	rawItem, ok := responsesWebsocketRawMessageForResult(payload, item)
	if !ok {
		return
	}
	t.recordRawItem(item.Get("type").String(), item.Get("call_id").String(), rawItem)
}

func (t *responsesWebsocketToolCacheTurn) recordInputItem(item responsesWebsocketInputItem) {
	if t == nil {
		return
	}
	t.recordRawItem(item.itemType, item.callID, item.raw)
}

func (t *responsesWebsocketToolCacheTurn) recordRawItem(itemType string, callID string, rawItem []byte) {
	if t == nil || (!isResponsesToolCallOutputType(itemType) && !isResponsesToolCallType(itemType)) {
		return
	}
	callID = strings.Clone(strings.TrimSpace(callID))
	if callID == "" || len(bytes.TrimSpace(rawItem)) == 0 {
		return
	}
	raw := append(json.RawMessage(nil), rawItem...)
	if isResponsesToolCallOutputType(itemType) {
		if _, exists := t.outputs[callID]; !exists {
			t.outputOrder = append(t.outputOrder, callID)
		}
		t.outputs[callID] = raw
		return
	}
	if _, exists := t.calls[callID]; !exists {
		t.callOrder = append(t.callOrder, callID)
	}
	t.calls[callID] = raw
}

func (t *responsesWebsocketToolCacheTurn) commit() {
	if t == nil || t.sessionKey == "" {
		return
	}
	defaultWebsocketToolCacheTransactionMu.Lock()
	defer defaultWebsocketToolCacheTransactionMu.Unlock()
	if defaultWebsocketToolOutputCache != nil {
		for _, callID := range t.outputOrder {
			defaultWebsocketToolOutputCache.record(t.sessionKey, callID, t.outputs[callID])
		}
	}
	if defaultWebsocketToolCallCache != nil {
		for _, callID := range t.callOrder {
			defaultWebsocketToolCallCache.record(t.sessionKey, callID, t.calls[callID])
		}
	}
}

func repairResponsesWebsocketToolCalls(sessionKey string, payload []byte) []byte {
	return repairResponsesWebsocketToolCallsWithCaches(defaultWebsocketToolOutputCache, defaultWebsocketToolCallCache, sessionKey, payload)
}

func repairResponsesWebsocketToolCallsWithoutRecording(sessionKey string, payload []byte) []byte {
	defaultWebsocketToolCacheTransactionMu.RLock()
	defer defaultWebsocketToolCacheTransactionMu.RUnlock()
	return repairResponsesWebsocketToolCallsWithCachesMode(defaultWebsocketToolOutputCache, defaultWebsocketToolCallCache, sessionKey, payload, false, nil)
}

func prepareResponsesWebsocketFallbackTurn(sessionKey string, payload []byte) ([]byte, *responsesWebsocketToolCacheTurn) {
	turn := newResponsesWebsocketToolCacheTurn(sessionKey)
	defaultWebsocketToolCacheTransactionMu.RLock()
	defer defaultWebsocketToolCacheTransactionMu.RUnlock()
	payload = repairResponsesWebsocketToolCallsWithCachesMode(
		defaultWebsocketToolOutputCache,
		defaultWebsocketToolCallCache,
		sessionKey,
		payload,
		false,
		turn,
	)
	return payload, turn
}

func repairResponsesWebsocketToolCallsWithCache(cache *websocketToolOutputCache, sessionKey string, payload []byte) []byte {
	return repairResponsesWebsocketToolCallsWithCaches(cache, nil, sessionKey, payload)
}

func repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache *websocketToolOutputCache, sessionKey string, payload []byte) []byte {
	return repairResponsesWebsocketToolCallsWithCachesMode(outputCache, callCache, sessionKey, payload, true, nil)
}

func repairResponsesWebsocketToolCallsWithCachesMode(
	outputCache, callCache *websocketToolOutputCache,
	sessionKey string,
	payload []byte,
	record bool,
	turn *responsesWebsocketToolCacheTurn,
) []byte {
	if len(payload) == 0 {
		return payload
	}

	input, previousResponseID, ok := parseResponsesWebsocketRepairRequest(payload)
	if !ok {
		return payload
	}
	items, rawItems, ok := parseResponsesWebsocketInputItemsNoCopy(payload, input)
	if !ok {
		return payload
	}

	sessionKey = strings.TrimSpace(sessionKey)
	repairEnabled := sessionKey != "" && outputCache != nil
	updatedItems, errRepair := repairResponsesToolCallItems(
		outputCache,
		callCache,
		sessionKey,
		items,
		repairEnabled && responsesWebsocketMetadataString(previousResponseID) != "",
		record && repairEnabled,
		turn,
		repairEnabled,
	)
	if errRepair != nil || responsesWebsocketInputItemsEqualRaw(updatedItems, rawItems) {
		return payload
	}

	updatedRaw, errMarshal := marshalResponsesWebsocketInputItems(updatedItems)
	if errMarshal != nil {
		return payload
	}
	updated, ok := replaceResponsesWebsocketRawResult(payload, input, []byte(updatedRaw))
	if !ok {
		return payload
	}
	return updated
}

func parseResponsesWebsocketRepairRequest(payload []byte) (gjson.Result, json.RawMessage, bool) {
	if !json.Valid(payload) {
		return gjson.Result{}, nil, false
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	if !root.IsObject() {
		return gjson.Result{}, nil, false
	}

	var input gjson.Result
	var previousResponseID json.RawMessage
	inputFound := false
	valid := true
	root.ForEach(func(key, value gjson.Result) bool {
		switch {
		case strings.EqualFold(key.String(), "input"):
			if !value.IsArray() && strings.TrimSpace(value.Raw) != "null" {
				valid = false
				return false
			}
			input = value
			inputFound = true
		case strings.EqualFold(key.String(), "previous_response_id"):
			var ok bool
			previousResponseID, ok = responsesWebsocketRawMessageForResult(payload, value)
			if !ok {
				valid = false
				return false
			}
		}
		return true
	})
	if !valid || !inputFound || !input.IsArray() {
		return gjson.Result{}, nil, false
	}
	return input, previousResponseID, true
}

func replaceResponsesWebsocketRawResult(payload []byte, result gjson.Result, replacement []byte) ([]byte, bool) {
	if result.Index < 0 || result.Index > len(payload) || len(result.Raw) > len(payload)-result.Index {
		return nil, false
	}
	updated := make([]byte, 0, len(payload)-len(result.Raw)+len(replacement))
	updated = append(updated, payload[:result.Index]...)
	updated = append(updated, replacement...)
	updated = append(updated, payload[result.Index+len(result.Raw):]...)
	return updated, true
}

func parseResponsesWebsocketInputItemsNoCopy(payload []byte, input gjson.Result) ([]responsesWebsocketInputItem, []json.RawMessage, bool) {
	var items []responsesWebsocketInputItem
	var rawItems []json.RawMessage
	valid := true
	input.ForEach(func(_, itemResult gjson.Result) bool {
		rawItem, ok := responsesWebsocketRawMessageForResult(payload, itemResult)
		if !ok {
			valid = false
			return false
		}
		item, errItem := parseResponsesWebsocketInputItem(rawItem)
		if errItem != nil {
			valid = false
			return false
		}
		items = append(items, item)
		rawItems = append(rawItems, rawItem)
		return true
	})
	if !valid {
		return nil, nil, false
	}
	return items, rawItems, true
}

func responsesWebsocketRawMessageForResult(payload []byte, result gjson.Result) (json.RawMessage, bool) {
	if result.Index < 0 || result.Index > len(payload) || len(result.Raw) > len(payload)-result.Index {
		return nil, false
	}
	return payload[result.Index : result.Index+len(result.Raw)], true
}

func repairResponsesToolCallItems(
	outputCache, callCache *websocketToolOutputCache,
	sessionKey string,
	items []responsesWebsocketInputItem,
	allowOrphanOutputs bool,
	record bool,
	turn *responsesWebsocketToolCacheTurn,
	repairEnabled bool,
) ([]responsesWebsocketInputItem, error) {
	if !repairEnabled {
		return dedupeResponsesWebsocketInputItems(items), nil
	}

	// First pass: record tool outputs and remember which call_ids have outputs in this payload.
	outputPresent := make(map[string]struct{}, len(items))
	callPresent := make(map[string]struct{}, len(items))
	for _, item := range items {
		if turn != nil {
			turn.recordInputItem(item)
		}
		switch {
		case isResponsesToolCallOutputType(item.itemType):
			if item.callID == "" {
				continue
			}
			outputPresent[item.callID] = struct{}{}
			if record {
				outputCache.record(sessionKey, item.callID, item.raw)
			}
		case isResponsesToolCallType(item.itemType):
			if item.callID == "" {
				continue
			}
			callPresent[item.callID] = struct{}{}
			if record && callCache != nil {
				callCache.record(sessionKey, item.callID, item.raw)
			}
		}
	}

	filtered := make([]responsesWebsocketInputItem, 0, len(items))
	insertedCalls := make(map[string]struct{}, len(items))
	for _, item := range items {
		if isResponsesToolCallOutputType(item.itemType) {
			if item.callID == "" {
				// Upstream rejects tool outputs without a call_id; drop it.
				continue
			}

			if _, ok := callPresent[item.callID]; ok {
				filtered = append(filtered, item)
				continue
			}

			if allowOrphanOutputs {
				filtered = append(filtered, item)
				continue
			}

			if callCache != nil {
				if cached, ok := callCache.get(sessionKey, item.callID); ok {
					if _, already := insertedCalls[item.callID]; !already {
						cachedItem, errCached := parseResponsesWebsocketInputItem(cached)
						if errCached != nil {
							return nil, errCached
						}
						filtered = append(filtered, cachedItem)
						insertedCalls[item.callID] = struct{}{}
						callPresent[item.callID] = struct{}{}
					}
					filtered = append(filtered, item)
					continue
				}
			}

			// Drop orphaned function_call_output items; upstream rejects transcripts with missing calls.
			continue
		}
		if !isResponsesToolCallType(item.itemType) {
			filtered = append(filtered, item)
			continue
		}

		if item.callID == "" {
			// Upstream rejects tool calls without a call_id; drop it.
			continue
		}

		if _, ok := outputPresent[item.callID]; ok {
			filtered = append(filtered, item)
			continue
		}

		if allowOrphanOutputs {
			filtered = append(filtered, item)
			continue
		}

		if cached, ok := outputCache.get(sessionKey, item.callID); ok {
			cachedItem, errCached := parseResponsesWebsocketInputItem(cached)
			if errCached != nil {
				return nil, errCached
			}
			filtered = append(filtered, item, cachedItem)
			outputPresent[item.callID] = struct{}{}
			continue
		}

		// Drop orphaned function_call items; upstream rejects transcripts with missing outputs.
	}

	return dedupeResponsesWebsocketInputItems(filtered), nil
}

func responsesWebsocketInputItemsEqualRaw(items []responsesWebsocketInputItem, rawItems []json.RawMessage) bool {
	if len(items) != len(rawItems) {
		return false
	}
	for index := range items {
		if !bytes.Equal(items[index].raw, rawItems[index]) {
			return false
		}
	}
	return true
}

func recordResponsesWebsocketToolCallsFromPayload(sessionKey string, payload []byte) {
	recordResponsesWebsocketToolCallsFromPayloadWithCache(defaultWebsocketToolCallCache, sessionKey, payload)
}

func recordResponsesWebsocketToolCallsFromPayloadWithCache(cache *websocketToolOutputCache, sessionKey string, payload []byte) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || cache == nil || len(payload) == 0 {
		return
	}

	eventType := strings.TrimSpace(util.GetGJSONBytesNoCopy(payload, "type").String())
	switch eventType {
	case "response.completed":
		output := util.GetGJSONBytesNoCopy(payload, "response.output")
		if !output.Exists() || !output.IsArray() {
			return
		}
		output.ForEach(func(_, item gjson.Result) bool {
			if !isCompleteResponsesWebsocketToolCall(item) {
				return true
			}
			rawItem, ok := responsesWebsocketRawMessageForResult(payload, item)
			if !ok {
				return false
			}
			callID := strings.TrimSpace(item.Get("call_id").String())
			cache.record(sessionKey, callID, rawItem)
			return true
		})
	case "response.output_item.added", "response.output_item.done":
		item := util.GetGJSONBytesNoCopy(payload, "item")
		if !isCompleteResponsesWebsocketToolCall(item) {
			return
		}
		rawItem, ok := responsesWebsocketRawMessageForResult(payload, item)
		if !ok {
			return
		}
		callID := strings.TrimSpace(item.Get("call_id").String())
		cache.record(sessionKey, callID, rawItem)
	}
}

func isResponsesToolCallType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "function_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

func isResponsesToolCallOutputType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "function_call_output", "custom_tool_call_output":
		return true
	default:
		return false
	}
}
