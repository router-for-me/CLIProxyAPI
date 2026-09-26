package main

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

type requestState struct {
	bridge      bool
	bridgeKnown bool
	catalog     *toolCatalog
	bytes       int
	config      resolvedPluginConfig
}

var errRequestStateCapacity = errors.New("request state capacity exceeded")

var (
	requestStateMu    sync.RWMutex
	requestStates     = make(map[string]requestState)
	requestStateOrder []string
)

func rememberRequestState(requestID string, state requestState, budget pluginStateBudget) error {
	if strings.TrimSpace(requestID) == "" {
		return nil
	}
	if state.catalog != nil {
		state.catalog = compactResponseCatalog(state.catalog)
		if state.bytes == 0 {
			state.bytes = requestStateSize(state.catalog)
		}
	}
	requestStateMu.Lock()
	defer requestStateMu.Unlock()
	previous, exists := requestStates[requestID]
	if exists && !state.bridgeKnown {
		if state.catalog == nil {
			state.catalog = previous.catalog
			state.bytes = previous.bytes
		}
		if state.config.toolBudget.MaxToolBytes == 0 {
			state.config = previous.config
		}
		state.bridge = previous.bridge
		state.bridgeKnown = previous.bridgeKnown
	}
	// A known non-bridge request has no response mapping to preserve. Do not
	// create an unrelated state entry that could outlive a missing completion.
	// Existing IDs still fall through so a protocol change replaces stale state.
	if !exists && state.bridgeKnown && (!state.bridge || state.catalog == nil) {
		return nil
	}
	if !exists {
		requestStateOrder = append(requestStateOrder, requestID)
	}
	if errCapacity := enforceStateCapacityLocked(requestID, previous, exists, state, budget); errCapacity != nil {
		if !exists {
			requestStateOrder = requestStateOrder[:len(requestStateOrder)-1]
		}
		return errCapacity
	}
	requestStates[requestID] = state
	return nil
}

func enforceStateCapacityLocked(requestID string, previous requestState, existed bool, next requestState, budget pluginStateBudget) error {
	activeBefore := 0
	bytesBefore := 0
	for _, state := range requestStates {
		if state.catalog != nil {
			activeBefore++
			bytesBefore += state.bytes
		}
	}
	activeAfter := activeBefore
	bytesAfter := bytesBefore
	if existed && previous.catalog != nil {
		activeAfter--
		bytesAfter -= previous.bytes
	}
	if next.catalog != nil {
		activeAfter++
		bytesAfter += next.bytes
	}
	if activeAfter > budget.MaxStates || bytesAfter > budget.MaxBytes {
		return errRequestStateCapacity
	}
	return nil
}

func loadRequestState(requestID string) (requestState, bool) {
	requestStateMu.RLock()
	defer requestStateMu.RUnlock()
	state, ok := requestStates[requestID]
	return state, ok
}

func releaseRequestState(requestID string) {
	if strings.TrimSpace(requestID) == "" {
		return
	}
	requestStateMu.Lock()
	defer requestStateMu.Unlock()
	if _, exists := requestStates[requestID]; !exists {
		return
	}
	delete(requestStates, requestID)
	for index, candidate := range requestStateOrder {
		if candidate == requestID {
			requestStateOrder = append(requestStateOrder[:index], requestStateOrder[index+1:]...)
			break
		}
	}
}

func clearRequestStates() {
	requestStateMu.Lock()
	defer requestStateMu.Unlock()
	requestStates = make(map[string]requestState)
	requestStateOrder = nil
}

func requestStateUsage() (entries, bytes int) {
	requestStateMu.RLock()
	defer requestStateMu.RUnlock()
	for _, state := range requestStates {
		if state.catalog != nil {
			entries++
			bytes += state.bytes
		}
	}
	return entries, bytes
}

func compactResponseCatalog(catalog *toolCatalog) *toolCatalog {
	if catalog == nil {
		return nil
	}
	catalog.finalize()
	compact := newToolCatalog()
	compact.identities = append([]toolIdentity(nil), catalog.identities...)
	compact.clientSearch = catalog.clientSearch
	compact.serverSearch = catalog.serverSearch
	compact.searchBridge = catalog.searchBridge
	compact.searchAlias = catalog.searchAlias
	compact.deferred = make(map[toolIdentity]bool, len(catalog.deferred))
	for identity, deferred := range catalog.deferred {
		compact.deferred[identity] = deferred
	}
	compact.aliasByID = make(map[toolIdentity]string, len(catalog.aliasByID))
	for identity, alias := range catalog.aliasByID {
		compact.aliasByID[identity] = alias
	}
	compact.idByAlias = make(map[string]toolIdentity, len(catalog.idByAlias))
	for alias, identity := range catalog.idByAlias {
		compact.idByAlias[alias] = identity
	}
	compact.exact = make(map[string]toolIdentity, len(catalog.exact))
	for name, identity := range catalog.exact {
		compact.exact[name] = identity
	}
	compact.normalized = make(map[string]toolIdentity, len(catalog.normalized))
	for name, identity := range catalog.normalized {
		compact.normalized[name] = identity
	}
	compact.local = make(map[string]toolIdentity, len(catalog.local))
	for name, identity := range catalog.local {
		compact.local[name] = identity
	}
	compact.namespaces = make(map[string]toolIdentity, len(catalog.namespaces))
	for name, identity := range catalog.namespaces {
		compact.namespaces[name] = identity
	}
	compact.namespaceCounts = make(map[string]int, len(catalog.namespaceCounts))
	for name, count := range catalog.namespaceCounts {
		compact.namespaceCounts[name] = count
	}
	for name := range catalog.topLevel {
		compact.topLevel[name] = struct{}{}
	}
	return compact
}

func requestStateSize(catalog *toolCatalog) int {
	if catalog == nil {
		return 0
	}
	summary := struct {
		Identities []toolIdentity    `json:"identities"`
		Aliases    map[string]string `json:"aliases"`
		Search     string            `json:"search"`
	}{
		Identities: catalog.identities,
		Aliases:    make(map[string]string, len(catalog.idByAlias)),
		Search:     catalog.searchAlias,
	}
	for alias := range catalog.idByAlias {
		summary.Aliases[alias] = ""
	}
	encoded, errMarshal := json.Marshal(summary)
	if errMarshal != nil {
		return 1 << 30
	}
	return len(encoded)
}
