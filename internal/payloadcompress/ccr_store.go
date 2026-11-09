package payloadcompress

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// In-process tier of the CCR block store. Upstream keeps blocks in a bounded
// Map with a 24h TTL; the optional SQLite tier is not ported. Keys are content
// hashes and every block is owned by the anonymous principal because the
// engine has no principal identity here.
const (
	ccrMaxEntries    = 5000
	ccrMaxBlockBytes = 2 * 1024 * 1024
	ccrMaxStoreBytes = 64 * 1024 * 1024
	ccrBlockTTL      = 24 * time.Hour
)

type ccrStoreEntry struct {
	text       string
	bytes      int
	lastAccess time.Time
	expiresAt  time.Time
}

var (
	ccrStoreMu    sync.Mutex
	ccrStore      = make(map[string]*ccrStoreEntry)
	ccrStoreBytes int
)

// ccrBlockID is the deterministic 24-hex SHA-256 prefix embedded in a marker.
func ccrBlockID(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:24]
}

// ccrTryStore keeps text for later retrieval and reports its content id. It is
// bounded by entry count, block size and total bytes; expired and least-recently
// used entries are evicted first.
func ccrTryStore(text string) (string, bool) {
	id := ccrBlockID(text)
	n := len(text)
	if n > ccrMaxBlockBytes {
		return id, false
	}
	now := time.Now()
	ccrStoreMu.Lock()
	defer ccrStoreMu.Unlock()
	ccrPurgeExpiredLocked(now)
	if entry, ok := ccrStore[id]; ok {
		entry.lastAccess = now
		entry.expiresAt = now.Add(ccrBlockTTL)
		return id, true
	}
	for len(ccrStore) >= ccrMaxEntries || ccrStoreBytes+n > ccrMaxStoreBytes {
		if !ccrEvictOldestLocked() {
			return id, false
		}
	}
	ccrStore[id] = &ccrStoreEntry{
		text:       text,
		bytes:      n,
		lastAccess: now,
		expiresAt:  now.Add(ccrBlockTTL),
	}
	ccrStoreBytes += n
	return id, true
}

// RetrieveCCRBlock returns the verbatim block stored under id. Hosts that serve
// the `omniroute_ccr_retrieve` tool must call this; without that wiring a
// marker's content is unreachable.
func RetrieveCCRBlock(id string) (string, bool) {
	now := time.Now()
	ccrStoreMu.Lock()
	defer ccrStoreMu.Unlock()
	ccrPurgeExpiredLocked(now)
	entry, ok := ccrStore[id]
	if !ok {
		return "", false
	}
	entry.lastAccess = now
	entry.expiresAt = now.Add(ccrBlockTTL)
	return entry.text, true
}

func ccrPurgeExpiredLocked(now time.Time) {
	for id, entry := range ccrStore {
		if now.After(entry.expiresAt) {
			ccrStoreBytes -= entry.bytes
			delete(ccrStore, id)
		}
	}
}

func ccrEvictOldestLocked() bool {
	var oldestID string
	var oldest time.Time
	first := true
	for id, entry := range ccrStore {
		if first || entry.lastAccess.Before(oldest) {
			oldestID, oldest, first = id, entry.lastAccess, false
		}
	}
	if first {
		return false
	}
	entry := ccrStore[oldestID]
	ccrStoreBytes -= entry.bytes
	delete(ccrStore, oldestID)
	return true
}

// ccrResetStore clears the store. Tests only.
func ccrResetStore() {
	ccrStoreMu.Lock()
	defer ccrStoreMu.Unlock()
	ccrStore = make(map[string]*ccrStoreEntry)
	ccrStoreBytes = 0
}
