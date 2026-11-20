package main

import (
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// reasoningLane names the provider and canonical base model that produced reasoning.
type reasoningLane struct {
	Provider string
	Model    string
}

// String renders one lane for a diagnostic record. An unresolved lane renders empty.
func (l reasoningLane) String() string {
	if l.Provider == "" {
		return ""
	}
	return l.Provider + "/" + l.Model
}

// reasoningOwnerKey names one reasoning item inside one configuration generation.
type reasoningOwnerKey struct {
	Generation uint64
	ItemID     string
}

// reasoningOwnerEntry holds one item's producing lane and its expiry.
type reasoningOwnerEntry struct {
	Lane      reasoningLane
	ExpiresAt time.Time
}

// expiry reports when this ownership record lapses.
func (e reasoningOwnerEntry) expiry() time.Time {
	return e.ExpiresAt
}

// reasoningOwnerStore remembers which lane produced each reasoning item. A
// provider rejects an item another provider framed, so a turn rotating onto a
// second lane withholds the reasoning the first lane produced and returns that
// reasoning to its own lane on every later turn.
type reasoningOwnerStore struct {
	mu      sync.Mutex
	entries map[reasoningOwnerKey]reasoningOwnerEntry
	clock   func() time.Time
}

// newReasoningOwnerStore builds an empty ownership store reading the given clock.
func newReasoningOwnerStore(clock func() time.Time) *reasoningOwnerStore {
	if clock == nil {
		clock = time.Now
	}
	return &reasoningOwnerStore{
		entries: make(map[reasoningOwnerKey]reasoningOwnerEntry),
		clock:   clock,
	}
}

// record binds one reasoning item to the lane that produced it.
func (s *reasoningOwnerStore) record(key reasoningOwnerKey, lane reasoningLane, ttl time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.entries[key] = reasoningOwnerEntry{Lane: lane, ExpiresAt: s.clock().Add(ttl)}
	s.mu.Unlock()
}

// renewLane returns the lane that produced one reasoning item and extends that
// record by ttl, so ownership lapses only after its conversation idles for ttl.
// An expired record reads as absent and stays lapsed, so an unrecorded item and
// a stale item answer alike.
func (s *reasoningOwnerStore) renewLane(key reasoningOwnerKey, ttl time.Duration) (reasoningLane, bool) {
	if s == nil {
		return reasoningLane{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	entry, recorded := s.entries[key]
	if !recorded || !entryLive(entry, now) {
		return reasoningLane{}, false
	}
	entry.ExpiresAt = now.Add(ttl)
	s.entries[key] = entry
	return entry.Lane, true
}

// cleanupExpired releases every ownership record whose expiry passed.
func (s *reasoningOwnerStore) cleanupExpired() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expireEntries(s.entries, s.clock())
}

// size reports how many ownership records the store holds.
func (s *reasoningOwnerStore) size() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// reset releases every ownership record.
func (s *reasoningOwnerStore) reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.entries = make(map[reasoningOwnerKey]reasoningOwnerEntry)
	s.mu.Unlock()
}

// recordReasoningOwners binds every finished reasoning item one response carries
// to the lane the plugin dispatched for that request. A request naming no single
// lane records nothing, because its reasoning has no provable producer.
func (r *runtimeState) recordReasoningOwners(cfg *compiledConfig, req pluginapi.StreamChunkInterceptRequest, payloads []map[string]any) {
	lane, laneKnown := cfg.laneFor(req.RequestedModel, req.Model)
	if !laneKnown {
		return
	}
	for _, payload := range payloads {
		for _, value := range finishedOutputItems(payload) {
			item, isObject := value.(map[string]any)
			if !isObject || stringJSONValue(item["type"]) != "reasoning" {
				continue
			}
			itemID := stringJSONValue(item["id"])
			if itemID == "" {
				continue
			}
			r.owners.record(reasoningOwnerKey{Generation: cfg.Generation, ItemID: itemID}, lane, cfg.SessionTTL)
		}
	}
}
