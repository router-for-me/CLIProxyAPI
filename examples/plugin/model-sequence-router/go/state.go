package main

import (
	"math/rand/v2"
	"sync"
	"time"
)

type cursorKey struct {
	Generation uint64
	Alias      string
	SessionID  string
}

type cursorEntry struct {
	Next      int
	ExpiresAt time.Time
	// LastTurn and LastIndex record the conversation state that produced the
	// stored selection, so a request repeating that state receives the same
	// sequence position instead of consuming the next one.
	LastTurn  *turnIdentity
	LastIndex int
}

// selectionOutcome discriminates how a sequence position was obtained.
type selectionOutcome int

const (
	// selectionExhausted means the probe found no available position.
	selectionExhausted selectionOutcome = iota
	// selectionAdvanced means a new conversation state reserved the next position.
	selectionAdvanced
	// selectionReplayed means a repeated conversation state reused its position.
	selectionReplayed
	// selectionStateless means a request without identity took the first available position.
	selectionStateless
	// selectionOutcome_COUNT bounds the closed outcome set.
	selectionOutcome_COUNT
)

// selectionOutcomeNames names every outcome for diagnostics. A value outside the
// closed set has no name and fails the lookup rather than reporting a wrong one.
var selectionOutcomeNames = [selectionOutcome_COUNT]string{
	selectionExhausted: "exhausted",
	selectionAdvanced:  "advanced",
	selectionReplayed:  "replayed",
	selectionStateless: "stateless",
}

// skippedPosition records one sequence position passed over for an unavailable provider.
type skippedPosition struct {
	Index    int
	Provider string
}

// selectionRequest carries one selection's domain inputs.
type selectionRequest struct {
	Key         cursorKey
	Sequence    []compiledTarget
	Available   map[string]struct{}
	Turn        *turnIdentity
	TTL         time.Duration
	RandomStart bool
	ProbeLimit  int
}

// selectionResult reports the chosen position, how it was obtained, and every position passed over.
type selectionResult struct {
	Target  compiledTarget
	Index   int
	Outcome selectionOutcome
	Skipped []skippedPosition
}

type cursorStore struct {
	mu      sync.Mutex
	entries map[cursorKey]cursorEntry
	clock   func() time.Time
	random  func(int) int
}

func newCursorStore(clock func() time.Time) *cursorStore {
	if clock == nil {
		clock = time.Now
	}
	return &cursorStore{entries: make(map[cursorKey]cursorEntry), clock: clock, random: rand.IntN}
}

// String names the outcome for diagnostics and rejects values outside the closed set.
func (o selectionOutcome) String() string {
	return selectionOutcomeNames[o]
}

// loadCursorEntry returns the cursor stored for one conversation or a fresh
// starting position and whether the returned entry was already stored.
// selectTarget holds the store mutex across this call.
func (s *cursorStore) loadCursorEntry(req selectionRequest, now time.Time) (cursorEntry, bool) {
	entry, exists := s.entries[req.Key]
	if exists && entry.ExpiresAt.After(now) && entry.Next >= 0 && entry.Next < len(req.Sequence) {
		return entry, true
	}
	fresh := cursorEntry{}
	if req.RandomStart {
		fresh.Next = s.random(len(req.Sequence))
	}
	return fresh, false
}

// selectTarget reserves a sequence position for one conversation. A route
// repeating the previous turn identity replays that turn's position and moves no
// cursor. A new turn advances. An exhausted probe preserves its starting position.
func (s *cursorStore) selectTarget(req selectionRequest) selectionResult {
	result := selectionResult{Outcome: selectionExhausted}
	if s == nil || len(req.Sequence) == 0 {
		return result
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	entry, stored := s.loadCursorEntry(req, now)
	repeated := entry.LastTurn.equals(req.Turn) && entry.LastIndex >= 0 && entry.LastIndex < len(req.Sequence)
	if repeated && providerAvailable(req.Sequence[entry.LastIndex], req.Available) {
		entry.ExpiresAt = now.Add(req.TTL)
		s.entries[req.Key] = entry
		result.Target = req.Sequence[entry.LastIndex]
		result.Index = entry.LastIndex
		result.Outcome = selectionReplayed
	} else {
		// A repeated turn whose remembered provider disappeared re-probes its own
		// position; every other request probes forward from the reserved position.
		start := entry.Next
		if repeated {
			start = entry.LastIndex
		}
		reserved := false
		for offset := 0; offset < req.ProbeLimit && !reserved; offset++ {
			index := (start + offset) % len(req.Sequence)
			target := req.Sequence[index]
			if providerAvailable(target, req.Available) {
				entry.Next = (index + 1) % len(req.Sequence)
				entry.ExpiresAt = now.Add(req.TTL)
				entry.LastTurn = req.Turn
				entry.LastIndex = index
				s.entries[req.Key] = entry
				result.Target = target
				result.Index = index
				result.Outcome = selectionAdvanced
				reserved = true
			} else {
				result.Skipped = append(result.Skipped, skippedPosition{Index: index, Provider: target.Provider})
			}
		}
		// A drawn starting position exists nowhere else, so an exhausted probe
		// records it and a retry re-enters on the position it examined.
		if !reserved && !stored && req.RandomStart {
			entry.ExpiresAt = now.Add(req.TTL)
			s.entries[req.Key] = entry
		}
	}
	return result
}

// selectStateless returns the first available position for a request with no identity.
func selectStateless(sequence []compiledTarget, available map[string]struct{}) selectionResult {
	result := selectionResult{Outcome: selectionExhausted}
	found := false
	for index := 0; index < len(sequence) && !found; index++ {
		target := sequence[index]
		if providerAvailable(target, available) {
			result.Target = target
			result.Index = index
			result.Outcome = selectionStateless
			found = true
		} else {
			result.Skipped = append(result.Skipped, skippedPosition{Index: index, Provider: target.Provider})
		}
	}
	return result
}

// providerAvailable reports whether a target's provider is currently routable.
func providerAvailable(target compiledTarget, available map[string]struct{}) bool {
	_, ok := available[target.Provider]
	return ok
}

func (s *cursorStore) cleanupExpired() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	for key, entry := range s.entries {
		if !entry.ExpiresAt.After(now) {
			delete(s.entries, key)
		}
	}
}

// size reports how many conversation cursors the store currently holds.
func (s *cursorStore) size() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *cursorStore) reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.entries = make(map[cursorKey]cursorEntry)
	s.mu.Unlock()
}

func cleanupInterval(ttl time.Duration) time.Duration {
	interval := ttl / 2
	if interval > 5*time.Minute {
		interval = 5 * time.Minute
	}
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	return interval
}

// expiringStore is generation-scoped state that a periodic sweep releases once
// its entries pass their expiry.
type expiringStore interface {
	cleanupExpired()
}

// cleanupExpiredStores releases expired entries from every generation-scoped store.
func cleanupExpiredStores(stores ...expiringStore) {
	for _, store := range stores {
		store.cleanupExpired()
	}
}

type cleanupLoop struct {
	mu     sync.Mutex
	cancel chan struct{}
	done   chan struct{}
}

// restart sweeps every supplied store on one interval derived from the session TTL.
func (l *cleanupLoop) restart(ttl time.Duration, stores ...expiringStore) {
	l.stop()
	l.mu.Lock()
	cancel := make(chan struct{})
	done := make(chan struct{})
	l.cancel = cancel
	l.done = done
	l.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(cleanupInterval(ttl))
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanupExpiredStores(stores...)
			case <-cancel:
				return
			}
		}
	}()
}

func (l *cleanupLoop) stop() {
	l.mu.Lock()
	cancel, done := l.cancel, l.done
	l.cancel, l.done = nil, nil
	l.mu.Unlock()
	if cancel == nil {
		return
	}
	close(cancel)
	<-done
}
