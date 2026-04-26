package executor

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	codexWindowStateTTL             = 12 * time.Hour
	codexWindowStateCleanupInterval = 64
)

type codexWindowStateEntry struct {
	generation uint64
	lastSeen   time.Time
}

type codexWindowStateStore struct {
	mu       sync.Mutex
	sessions map[string]codexWindowStateEntry
	ops      uint64
}

var globalCodexWindowStateStore = &codexWindowStateStore{
	sessions: make(map[string]codexWindowStateEntry),
}

func codexCurrentWindowID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	generation := globalCodexWindowStateStore.currentGeneration(sessionID)
	return sessionID + ":" + strconv.FormatUint(generation, 10)
}

func codexAdvanceWindowGeneration(sessionID string) {
	globalCodexWindowStateStore.advance(sessionID)
}

func (s *codexWindowStateStore) currentGeneration(sessionID string) uint64 {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || s == nil {
		return 0
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupLocked(now)
	entry := s.sessions[sessionID]
	entry.lastSeen = now
	s.sessions[sessionID] = entry
	return entry.generation
}

func (s *codexWindowStateStore) advance(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || s == nil {
		return
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupLocked(now)
	entry := s.sessions[sessionID]
	entry.generation++
	entry.lastSeen = now
	s.sessions[sessionID] = entry
}

func (s *codexWindowStateStore) cleanupLocked(now time.Time) {
	if s == nil {
		return
	}
	s.ops++
	if s.ops%codexWindowStateCleanupInterval != 0 {
		return
	}
	for sessionID, entry := range s.sessions {
		if now.Sub(entry.lastSeen) > codexWindowStateTTL {
			delete(s.sessions, sessionID)
		}
	}
}
