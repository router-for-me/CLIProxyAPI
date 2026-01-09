package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// SignatureEntry holds a cached thinking signature with timestamp
type SignatureEntry struct {
	Signature string
	Timestamp time.Time
}

const (
	// SignatureCacheTTL is how long signatures are valid
	SignatureCacheTTL = 1 * time.Hour

	// MaxEntriesPerSession limits memory usage per session
	MaxEntriesPerSession = 100

	// SignatureTextHashLen is the length of the hash key (16 hex chars = 64-bit key space)
	SignatureTextHashLen = 16

	// MinValidSignatureLen is the minimum length for a signature to be considered valid
	MinValidSignatureLen = 50

	// SessionCleanupInterval controls how often stale sessions are purged
	SessionCleanupInterval = 10 * time.Minute
)

// signatureCache stores signatures by sessionId -> textHash -> SignatureEntry
var signatureCache sync.Map

// sessionCleanupOnce ensures the background cleanup goroutine starts only once
var sessionCleanupOnce sync.Once

// sessionCache is the inner map type
type sessionCache struct {
	mu      sync.RWMutex
	entries map[string]SignatureEntry
}

// hashText creates a stable, Unicode-safe key from text content
func hashText(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])[:SignatureTextHashLen]
}

// getOrCreateSession gets or creates a session cache
func getOrCreateSession(sessionID string) *sessionCache {
	// Start background cleanup on first access
	sessionCleanupOnce.Do(startSessionCleanup)

	if val, ok := signatureCache.Load(sessionID); ok {
		return val.(*sessionCache)
	}
	sc := &sessionCache{entries: make(map[string]SignatureEntry)}
	actual, _ := signatureCache.LoadOrStore(sessionID, sc)
	return actual.(*sessionCache)
}

// startSessionCleanup launches a background goroutine that periodically
// removes sessions where all entries have expired.
func startSessionCleanup() {
	go func() {
		ticker := time.NewTicker(SessionCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredSessions()
		}
	}()
}

// purgeExpiredSessions removes sessions with no valid (non-expired) entries.
func purgeExpiredSessions() {
	now := time.Now()
	signatureCache.Range(func(key, value any) bool {
		sc := value.(*sessionCache)
		sc.mu.Lock()
		// Remove expired entries
		for k, entry := range sc.entries {
			if now.Sub(entry.Timestamp) > SignatureCacheTTL {
				delete(sc.entries, k)
			}
		}
		isEmpty := len(sc.entries) == 0
		sc.mu.Unlock()
		// Remove session if empty
		if isEmpty {
			signatureCache.Delete(key)
		}
		return true
	})
}

// CacheSignature stores a thinking signature for a given session and text.
// Used for Claude models that require signed thinking blocks in multi-turn conversations.
func CacheSignature(sessionID, text, signature string) {
	if sessionID == "" || text == "" || signature == "" {
		return
	}
	if len(signature) < MinValidSignatureLen {
		return
	}

	sc := getOrCreateSession(sessionID)
	textHash := hashText(text)

	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Evict expired entries if at capacity
	if len(sc.entries) >= MaxEntriesPerSession {
		now := time.Now()
		for key, entry := range sc.entries {
			if now.Sub(entry.Timestamp) > SignatureCacheTTL {
				delete(sc.entries, key)
			}
		}
		// If still at capacity, remove oldest entries
		if len(sc.entries) >= MaxEntriesPerSession {
			// Find and remove oldest quarter
			oldest := make([]struct {
				key string
				ts  time.Time
			}, 0, len(sc.entries))
			for key, entry := range sc.entries {
				oldest = append(oldest, struct {
					key string
					ts  time.Time
				}{key, entry.Timestamp})
			}
			// Sort by timestamp (oldest first) using sort.Slice
			sort.Slice(oldest, func(i, j int) bool {
				return oldest[i].ts.Before(oldest[j].ts)
			})

			toRemove := len(oldest) / 4
			if toRemove < 1 {
				toRemove = 1
			}

			for i := 0; i < toRemove; i++ {
				delete(sc.entries, oldest[i].key)
			}
		}
	}

	sc.entries[textHash] = SignatureEntry{
		Signature: signature,
		Timestamp: time.Now(),
	}
}

// GetCachedSignature retrieves a cached signature for a given session and text.
// Returns empty string if not found or expired.
func GetCachedSignature(sessionID, text string) string {
	if sessionID == "" || text == "" {
		return ""
	}

	val, ok := signatureCache.Load(sessionID)
	if !ok {
		return ""
	}
	sc := val.(*sessionCache)

	textHash := hashText(text)

	sc.mu.RLock()
	entry, exists := sc.entries[textHash]
	sc.mu.RUnlock()

	if !exists {
		return ""
	}

	// Check if expired
	if time.Since(entry.Timestamp) > SignatureCacheTTL {
		sc.mu.Lock()
		delete(sc.entries, textHash)
		sc.mu.Unlock()
		return ""
	}

	return entry.Signature
}

// ClearSignatureCache clears signature cache for a specific session or all sessions.
func ClearSignatureCache(sessionID string) {
	if sessionID != "" {
		signatureCache.Delete(sessionID)
	} else {
		signatureCache.Range(func(key, _ any) bool {
			signatureCache.Delete(key)
			return true
		})
	}
}

// HasValidSignature checks if a signature is valid (non-empty and long enough)
func HasValidSignature(signature string) bool {
	return signature != "" && len(signature) >= MinValidSignatureLen
}
