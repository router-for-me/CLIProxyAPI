package main

import (
	"sync"
	"time"
)

// continuationRequestKey names one in-flight request inside one configuration
// generation.
type continuationRequestKey struct {
	Generation uint64
	RequestID  string
}

// continuationResponseKey names one provider response inside one configuration
// generation.
type continuationResponseKey struct {
	Generation uint64
	ResponseID string
}

// expiringEntry is one generation-scoped record that answers when it lapses.
type expiringEntry interface {
	expiry() time.Time
}

type continuationEntry struct {
	Identity  conversationIdentity
	ExpiresAt time.Time
}

// expiry reports when this conversation binding lapses.
func (e continuationEntry) expiry() time.Time {
	return e.ExpiresAt
}

// continuationChainStore remembers which conversation each provider response
// belongs to. A continuation request names a prior response instead of carrying
// the transcript it continues, so the chain supplies the conversation that
// fragment belongs to. One request is held from the moment its conversation is
// known until a stream chunk names the response it produced.
type continuationChainStore struct {
	mu        sync.Mutex
	requests  map[continuationRequestKey]continuationEntry
	responses map[continuationResponseKey]continuationEntry
	clock     func() time.Time
}

func newContinuationChainStore(clock func() time.Time) *continuationChainStore {
	if clock == nil {
		clock = time.Now
	}
	return &continuationChainStore{
		requests:  make(map[continuationRequestKey]continuationEntry),
		responses: make(map[continuationResponseKey]continuationEntry),
		clock:     clock,
	}
}

// holdRequest records the conversation one in-flight request belongs to.
func (s *continuationChainStore) holdRequest(key continuationRequestKey, identity conversationIdentity, ttl time.Duration) {
	if s == nil || identity == "" {
		return
	}
	s.mu.Lock()
	s.requests[key] = continuationEntry{Identity: identity, ExpiresAt: s.clock().Add(ttl)}
	s.mu.Unlock()
}

// heldRequest returns the conversation of one in-flight request.
func (s *continuationChainStore) heldRequest(key continuationRequestKey) conversationIdentity {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return liveIdentity(s.requests[key], s.clock())
}

// bindResponse names the conversation one provider response continues.
func (s *continuationChainStore) bindResponse(key continuationResponseKey, identity conversationIdentity, ttl time.Duration) {
	if s == nil || identity == "" {
		return
	}
	s.mu.Lock()
	s.responses[key] = continuationEntry{Identity: identity, ExpiresAt: s.clock().Add(ttl)}
	s.mu.Unlock()
}

// lookupResponse returns the conversation a named provider response belongs to.
// An expired binding reads as absent and the periodic sweep releases it.
func (s *continuationChainStore) lookupResponse(key continuationResponseKey) conversationIdentity {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return liveIdentity(s.responses[key], s.clock())
}

// cleanupExpired releases held requests and response bindings whose expiry passed.
func (s *continuationChainStore) cleanupExpired() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	expireEntries(s.requests, now)
	expireEntries(s.responses, now)
}

// size reports how many held requests and response bindings the store holds.
func (s *continuationChainStore) size() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests) + len(s.responses)
}

func (s *continuationChainStore) reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.requests = make(map[continuationRequestKey]continuationEntry)
	s.responses = make(map[continuationResponseKey]continuationEntry)
	s.mu.Unlock()
}

// liveIdentity returns the identity of an entry that has not expired. A missing
// entry reads as expired, so an absent key and a stale key answer alike.
func liveIdentity(entry continuationEntry, now time.Time) conversationIdentity {
	if !entryLive(entry, now) {
		return ""
	}
	return entry.Identity
}

// entryLive reports whether one stored record still stands at the given moment.
// A zero-valued record reads as lapsed, so an absent key and a stale key answer
// alike wherever a store reads one.
func entryLive(entry expiringEntry, now time.Time) bool {
	return entry.expiry().After(now)
}

// expireEntries removes every entry whose expiry has passed.
func expireEntries[K comparable, V expiringEntry](entries map[K]V, now time.Time) {
	for key, entry := range entries {
		if !entryLive(entry, now) {
			delete(entries, key)
		}
	}
}
