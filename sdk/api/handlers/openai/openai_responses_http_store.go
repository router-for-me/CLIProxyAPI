package openai

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"
)

const (
	responsesHTTPContinuationMaxEntries    = 1024
	responsesHTTPContinuationMaxBytes      = 64 << 20
	responsesHTTPContinuationMaxEntryBytes = 8 << 20
	responsesHTTPContinuationTTL           = 30 * time.Minute
)

// responsesHTTPContinuation contains immutable, owned JSON array snapshots.
// Callers must not modify Input or Output returned by load.
type responsesHTTPContinuation struct {
	Input  []byte
	Output []byte
	AuthID string
	Model  string
}

type responsesHTTPContinuationKey struct {
	scope      string
	responseID string
}

type responsesHTTPContinuationEntry struct {
	state   responsesHTTPContinuation
	expires time.Time
	order   uint64
	bytes   int
}

// responsesHTTPContinuationStore is a bounded, process-local cache. Its zero
// value is ready for use; clock and limits may be set before use by tests.
type responsesHTTPContinuationStore struct {
	mu            sync.Mutex
	entries       map[responsesHTTPContinuationKey]responsesHTTPContinuationEntry
	bytes         int
	order         uint64
	now           func() time.Time
	ttl           time.Duration
	maxEntries    int
	maxBytes      int
	maxEntryBytes int
}

func (s *responsesHTTPContinuationStore) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *responsesHTTPContinuationStore) sweep(now time.Time) {
	for key, entry := range s.entries {
		if !now.Before(entry.expires) {
			s.bytes -= entry.bytes
			delete(s.entries, key)
		}
	}
}

func (s *responsesHTTPContinuationStore) load(scope, responseID string) (responsesHTTPContinuation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(s.clock())
	entry, ok := s.entries[responsesHTTPContinuationKey{scope: scope, responseID: responseID}]
	return entry.state, ok
}

func (s *responsesHTTPContinuationStore) save(scope, responseID string, state responsesHTTPContinuation) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	s.sweep(now)

	maxEntries, maxBytes, maxEntryBytes, ttl := s.maxEntries, s.maxBytes, s.maxEntryBytes, s.ttl
	if maxEntries <= 0 {
		maxEntries = responsesHTTPContinuationMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = responsesHTTPContinuationMaxBytes
	}
	if maxEntryBytes <= 0 {
		maxEntryBytes = responsesHTTPContinuationMaxEntryBytes
	}
	if ttl <= 0 {
		ttl = responsesHTTPContinuationTTL
	}
	size := len(scope) + len(responseID) + len(state.Input) + len(state.Output) + len(state.AuthID) + len(state.Model)
	if responseID == "" || state.AuthID == "" || size > maxEntryBytes || size > maxBytes || !responsesHTTPContinuationArray(state.Input) || !responsesHTTPContinuationArray(state.Output) {
		return false
	}
	key := responsesHTTPContinuationKey{scope: scope, responseID: responseID}
	previous, exists := s.entries[key]
	if exists && (previous.state.AuthID != state.AuthID || previous.state.Model != state.Model || !bytes.Equal(previous.state.Input, state.Input)) {
		return false
	}
	if exists && bytes.Equal(previous.state.Output, state.Output) {
		return true
	}
	if s.entries == nil {
		s.entries = make(map[responsesHTTPContinuationKey]responsesHTTPContinuationEntry)
	}
	if exists {
		s.bytes -= previous.bytes
		delete(s.entries, key)
	}
	for len(s.entries) >= maxEntries || s.bytes > maxBytes-size {
		var oldestKey responsesHTTPContinuationKey
		var oldestOrder uint64
		found := false
		for candidate, entry := range s.entries {
			if !found || entry.order < oldestOrder {
				oldestKey, oldestOrder, found = candidate, entry.order, true
			}
		}
		s.bytes -= s.entries[oldestKey].bytes
		delete(s.entries, oldestKey)
	}
	entry := responsesHTTPContinuationEntry{
		state: responsesHTTPContinuation{
			Input:  bytes.Clone(state.Input),
			Output: bytes.Clone(state.Output),
			AuthID: state.AuthID,
			Model:  state.Model,
		},
		expires: now.Add(ttl),
		bytes:   size,
	}
	if exists {
		entry.expires, entry.order = previous.expires, previous.order
	} else {
		s.order++
		entry.order = s.order
	}
	s.entries[key] = entry
	s.bytes += size
	return true
}

func responsesHTTPContinuationArray(value []byte) bool {
	value = bytes.TrimSpace(value)
	return len(value) > 0 && value[0] == '[' && json.Valid(value)
}
