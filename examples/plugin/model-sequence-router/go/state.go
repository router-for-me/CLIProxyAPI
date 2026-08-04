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

func (s *cursorStore) selectTarget(key cursorKey, sequence []compiledTarget, available map[string]struct{}, ttl time.Duration, advance bool, randomStarts ...bool) (compiledTarget, int, bool) {
	if s == nil || len(sequence) == 0 {
		return compiledTarget{}, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	randomStart := len(randomStarts) > 0 && randomStarts[0]
	now := s.clock()
	entry, exists := s.entries[key]
	if !exists || !entry.ExpiresAt.After(now) || entry.Next < 0 || entry.Next >= len(sequence) {
		entry = cursorEntry{}
		exists = false
		if randomStart {
			entry.Next = s.random(len(sequence))
		}
	}
	start := entry.Next
	for offset := range len(sequence) {
		index := (start + offset) % len(sequence)
		target := sequence[index]
		if _, ok := available[target.Provider]; !ok {
			continue
		}
		if advance {
			entry.Next = (index + 1) % len(sequence)
		}
		entry.ExpiresAt = now.Add(ttl)
		s.entries[key] = entry
		return target, index, true
	}
	return compiledTarget{}, 0, false
}

func selectStateless(sequence []compiledTarget, available map[string]struct{}) (compiledTarget, int, bool) {
	for index, target := range sequence {
		if _, ok := available[target.Provider]; ok {
			return target, index, true
		}
	}
	return compiledTarget{}, 0, false
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

type cleanupLoop struct {
	mu     sync.Mutex
	cancel chan struct{}
	done   chan struct{}
}

func (l *cleanupLoop) restart(store *cursorStore, ttl time.Duration) {
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
				store.cleanupExpired()
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
