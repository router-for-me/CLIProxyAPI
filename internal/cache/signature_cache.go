package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
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
	SignatureCacheTTL = 3 * time.Hour

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
func CacheSignature(modelName, text, signature string) {
	if text == "" || signature == "" {
		return
	}
	if len(signature) < MinValidSignatureLen {
		return
	}

	text = fmt.Sprintf("%s#%s", GetModelGroup(modelName), text)
	textHash := hashText(text)
	sc := getOrCreateSession(textHash)
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.entries[textHash] = SignatureEntry{
		Signature: signature,
		Timestamp: time.Now(),
	}
}

// GetCachedSignature retrieves a cached signature for a given session and text.
// Returns empty string if not found or expired.
func GetCachedSignature(modelName, text string) string {
	family := GetModelGroup(modelName)

	if text == "" {
		if family == "gemini" {
			return "skip_thought_signature_validator"
		}
		return ""
	}
	text = fmt.Sprintf("%s#%s", GetModelGroup(modelName), text)
	val, ok := signatureCache.Load(hashText(text))
	if !ok {
		if family == "gemini" {
			return "skip_thought_signature_validator"
		}
		return ""
	}
	sc := val.(*sessionCache)

	textHash := hashText(text)

	now := time.Now()

	sc.mu.Lock()
	entry, exists := sc.entries[textHash]
	if !exists {
		sc.mu.Unlock()
		if family == "gemini" {
			return "skip_thought_signature_validator"
		}
		return ""
	}
	if now.Sub(entry.Timestamp) > SignatureCacheTTL {
		delete(sc.entries, textHash)
		sc.mu.Unlock()
		if family == "gemini" {
			return "skip_thought_signature_validator"
		}
		return ""
	}

	// Refresh TTL on access (sliding expiration).
	entry.Timestamp = now
	sc.entries[textHash] = entry
	sc.mu.Unlock()

	return entry.Signature
}

// ClearSignatureCache clears signature cache for a specific session or all sessions.
func ClearSignatureCache(sessionID string) {
	if sessionID != "" {
		signatureCache.Range(func(key, _ any) bool {
			kStr, ok := key.(string)
			if ok && strings.HasSuffix(kStr, "#"+sessionID) {
				signatureCache.Delete(key)
			}
			return true
		})
	} else {
		signatureCache.Range(func(key, _ any) bool {
			signatureCache.Delete(key)
			return true
		})
	}
}

// HasValidSignature checks if a signature is valid (non-empty and long enough)
func HasValidSignature(modelName, signature string) bool {
	return (signature != "" && len(signature) >= MinValidSignatureLen) || (signature == "skip_thought_signature_validator" && GetModelGroup(modelName) == "gemini")
}

func GetModelGroup(modelName string) string {
	if strings.Contains(modelName, "gpt") {
		return "gpt"
	} else if strings.Contains(modelName, "claude") {
		return "claude"
	} else if strings.Contains(modelName, "gemini") {
		return "gemini"
	}
	return modelName
}
