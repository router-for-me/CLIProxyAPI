package executor

import (
	"encoding/json"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// ResponsesSnapshot captures the state of a completed Responses API request,
// stored for subsequent requests that carry previous_response_id in chat_fallback mode.
type ResponsesSnapshot struct {
	Model        string          `json:"model"`
	Instructions string          `json:"instructions,omitempty"`
	Input        json.RawMessage `json:"input"`
	Output       json.RawMessage `json:"output"`
	CreatedAt    int64           `json:"created_at"`
	StoredAt     time.Time       `json:"stored_at"`
}

// ResponsesStateStore persists response snapshots keyed by response_id
// for the chat_fallback path. Only chat_fallback mode writes and reads;
// native mode never touches the store.
type ResponsesStateStore interface {
	// Put stores a snapshot for the given responseID.
	Put(responseID string, snapshot ResponsesSnapshot)

	// Get retrieves a snapshot by responseID. Returns false if not found or expired.
	Get(responseID string) (ResponsesSnapshot, bool)

	// Delete removes a snapshot by responseID.
	Delete(responseID string)
}

// memoryResponsesStateStore is an in-memory implementation with TTL-based expiry and LRU eviction.
type memoryResponsesStateStore struct {
	mu      sync.Mutex
	entries map[string]*memoryEntry
	ttl     time.Duration
	maxSize int
}

type memoryEntry struct {
	snapshot  ResponsesSnapshot
	createdAt time.Time
}

// NewMemoryResponsesStateStore creates an in-memory state store.
// Entries older than ttl are automatically expired; when the store exceeds
// maxEntries, the oldest entries are evicted.
func NewMemoryResponsesStateStore(ttl time.Duration, maxEntries int) ResponsesStateStore {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = 1024
	}
	s := &memoryResponsesStateStore{
		entries: make(map[string]*memoryEntry),
		ttl:     ttl,
		maxSize: maxEntries,
	}
	go s.cleanupLoop()
	return s
}

func (s *memoryResponsesStateStore) Put(responseID string, snapshot ResponsesSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot.StoredAt = time.Now()
	s.entries[responseID] = &memoryEntry{
		snapshot:  snapshot,
		createdAt: time.Now(),
	}

	// Evict oldest entries if over capacity.
	if len(s.entries) > s.maxSize {
		s.evictOldestLocked()
	}
}

func (s *memoryResponsesStateStore) Get(responseID string) (ResponsesSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[responseID]
	if !ok {
		return ResponsesSnapshot{}, false
	}

	// Check TTL expiry.
	if time.Since(entry.createdAt) > s.ttl {
		delete(s.entries, responseID)
		return ResponsesSnapshot{}, false
	}

	return entry.snapshot, true
}

func (s *memoryResponsesStateStore) Delete(responseID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, responseID)
}

func (s *memoryResponsesStateStore) evictOldestLocked() {
	// Simple eviction: remove entries until we're under capacity.
	toRemove := len(s.entries) - s.maxSize + 1
	if toRemove < 1 {
		toRemove = 1
	}

	// Find the oldest entries by createdAt.
	type idAge struct {
		id        string
		createdAt time.Time
	}
	entries := make([]idAge, 0, len(s.entries))
	for id, entry := range s.entries {
		entries = append(entries, idAge{id: id, createdAt: entry.createdAt})
	}
	// Sort by createdAt ascending (oldest first).
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].createdAt.Before(entries[i].createdAt) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
	removed := 0
	for _, e := range entries {
		if removed >= toRemove {
			break
		}
		delete(s.entries, e.id)
		removed++
	}
}

func (s *memoryResponsesStateStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for id, entry := range s.entries {
			if now.Sub(entry.createdAt) > s.ttl {
				delete(s.entries, id)
			}
		}
		s.mu.Unlock()
	}
}

// globalResponsesStateStore is the process-wide singleton state store.
var globalResponsesStateStore ResponsesStateStore

func init() {
	globalResponsesStateStore = NewMemoryResponsesStateStore(30*time.Minute, 1024)
}

// ensure logging for state store operations
func init() {
	log.Debugf("responses state store: initialized with ttl=30m max=1024")
}
