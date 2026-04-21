package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
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

	// CacheCleanupInterval controls how often stale entries are purged
	CacheCleanupInterval = 10 * time.Minute

	// SignatureCacheMaxEntriesPerGroup bounds per-model-group memory usage.
	SignatureCacheMaxEntriesPerGroup = 10000
)

// CacheCleanupRestartDelay prevents tight restart loops after a panic.
var CacheCleanupRestartDelay = time.Second

// signatureCache stores signatures by model group -> textHash -> SignatureEntry
var signatureCache sync.Map

// cacheCleanupOnce ensures the background cleanup goroutine starts only once
var cacheCleanupOnce sync.Once

// groupCache is the inner map type
type groupCache struct {
	mu      sync.RWMutex
	entries map[string]SignatureEntry
}

func missingCachedSignature(groupKey string) string {
	if groupKey == "gemini" {
		return "skip_thought_signature_validator"
	}
	return ""
}

// hashText creates a stable, Unicode-safe key from text content
func hashText(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])[:SignatureTextHashLen]
}

// getOrCreateGroupCache gets or creates a cache bucket for a model group
func getOrCreateGroupCache(groupKey string) *groupCache {
	// Start background cleanup on first access
	cacheCleanupOnce.Do(startCacheCleanup)

	if val, ok := signatureCache.Load(groupKey); ok {
		return val.(*groupCache)
	}
	sc := &groupCache{entries: make(map[string]SignatureEntry)}
	actual, _ := signatureCache.LoadOrStore(groupKey, sc)
	return actual.(*groupCache)
}

// startCacheCleanup launches a background goroutine that periodically
// removes caches where all entries have expired.
func startCacheCleanup() {
	go runProtectedCacheCleanupLoop(CacheCleanupInterval, purgeExpiredCaches, nil)
}

// purgeExpiredCaches removes caches with no valid (non-expired) entries.
func purgeExpiredCaches() {
	now := time.Now()
	signatureCache.Range(func(key, value any) bool {
		sc := value.(*groupCache)
		sc.mu.Lock()
		// Remove expired entries
		for k, entry := range sc.entries {
			if now.Sub(entry.Timestamp) > SignatureCacheTTL {
				delete(sc.entries, k)
			}
		}
		isEmpty := len(sc.entries) == 0
		sc.mu.Unlock()
		// Remove cache bucket if empty
		if isEmpty {
			signatureCache.Delete(key)
		}
		return true
	})
}

func runProtectedCacheCleanupLoop(interval time.Duration, cleanup func(), stop <-chan struct{}) {
	for {
		panicValue := runCacheCleanupLoop(interval, cleanup, stop)
		if panicValue == nil {
			return
		}
		log.Errorf("signature cache cleanup loop panicked, restarting: %v\n%s", panicValue, debug.Stack())
		if stop != nil && stopRequested(stop) {
			return
		}
		time.Sleep(CacheCleanupRestartDelay)
	}
}

func runCacheCleanupLoop(interval time.Duration, cleanup func(), stop <-chan struct{}) (panicValue any) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer func() {
		if recovered := recover(); recovered != nil {
			panicValue = recovered
		}
	}()

	for {
		select {
		case <-ticker.C:
			cleanup()
		case <-stop:
			return nil
		}
	}
}

func stopRequested(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// CacheSignature stores a thinking signature for a given model group and text.
// Used for Claude models that require signed thinking blocks in multi-turn conversations.
func CacheSignature(modelName, text, signature string) {
	if text == "" || signature == "" {
		return
	}
	if len(signature) < MinValidSignatureLen {
		return
	}

	groupKey := GetModelGroup(modelName)
	textHash := hashText(text)
	sc := getOrCreateGroupCache(groupKey)
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.entries[textHash] = SignatureEntry{
		Signature: signature,
		Timestamp: time.Now(),
	}
	enforceGroupEntryLimitLocked(sc.entries)
}

func enforceGroupEntryLimitLocked(entries map[string]SignatureEntry) {
	excessEntries := len(entries) - SignatureCacheMaxEntriesPerGroup
	if excessEntries <= 0 {
		return
	}

	for range excessEntries {
		oldestKey, ok := oldestEntryKey(entries)
		if !ok {
			return
		}
		delete(entries, oldestKey)
	}
}

func oldestEntryKey(entries map[string]SignatureEntry) (string, bool) {
	var (
		oldestKey string
		oldestAt  time.Time
		found     bool
	)
	for key, entry := range entries {
		if !found || entry.Timestamp.Before(oldestAt) {
			oldestKey = key
			oldestAt = entry.Timestamp
			found = true
		}
	}
	return oldestKey, found
}

// GetCachedSignature retrieves a cached signature for a given model group and text.
// Returns empty string if not found or expired.
func GetCachedSignature(modelName, text string) string {
	groupKey := GetModelGroup(modelName)

	if text == "" {
		return missingCachedSignature(groupKey)
	}
	val, ok := signatureCache.Load(groupKey)
	if !ok {
		return missingCachedSignature(groupKey)
	}
	sc := val.(*groupCache)
	textHash := hashText(text)
	now := time.Now()
	if signature, ok := lookupCachedSignature(sc, textHash, now); ok {
		return signature
	}
	return missingCachedSignature(groupKey)
}

func lookupCachedSignature(sc *groupCache, textHash string, now time.Time) (string, bool) {
	sc.mu.RLock()
	entry, exists := sc.entries[textHash]
	if exists && now.Sub(entry.Timestamp) <= SignatureCacheTTL {
		sc.mu.RUnlock()
		return entry.Signature, true
	}
	sc.mu.RUnlock()

	if !exists {
		return "", false
	}
	return removeExpiredSignature(sc, textHash, now)
}

func removeExpiredSignature(sc *groupCache, textHash string, now time.Time) (string, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	entry, exists := sc.entries[textHash]
	if !exists {
		return "", false
	}
	if now.Sub(entry.Timestamp) > SignatureCacheTTL {
		delete(sc.entries, textHash)
		return "", false
	}
	return entry.Signature, true
}

// ClearSignatureCache clears signature cache for a specific model group or all groups.
func ClearSignatureCache(modelName string) {
	if modelName == "" {
		signatureCache.Range(func(key, _ any) bool {
			signatureCache.Delete(key)
			return true
		})
		return
	}
	groupKey := GetModelGroup(modelName)
	signatureCache.Delete(groupKey)
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

var signatureCacheEnabled atomic.Bool
var signatureBypassStrictMode atomic.Bool

func init() {
	signatureCacheEnabled.Store(true)
	signatureBypassStrictMode.Store(false)
}

// SetSignatureCacheEnabled switches Antigravity signature handling between cache mode and bypass mode.
func SetSignatureCacheEnabled(enabled bool) {
	previous := signatureCacheEnabled.Swap(enabled)
	if previous == enabled {
		return
	}
	if !enabled {
		log.Info("antigravity signature cache DISABLED - bypass mode active, cached signatures will not be used for request translation")
	}
}

// SignatureCacheEnabled returns whether signature cache validation is enabled.
func SignatureCacheEnabled() bool {
	return signatureCacheEnabled.Load()
}

// SetSignatureBypassStrictMode controls whether bypass mode uses strict protobuf-tree validation.
func SetSignatureBypassStrictMode(strict bool) {
	previous := signatureBypassStrictMode.Swap(strict)
	if previous == strict {
		return
	}
	if strict {
		log.Debug("antigravity bypass signature validation: strict mode (protobuf tree)")
	} else {
		log.Debug("antigravity bypass signature validation: basic mode (R/E + 0x12)")
	}
}

// SignatureBypassStrictMode returns whether bypass mode uses strict protobuf-tree validation.
func SignatureBypassStrictMode() bool {
	return signatureBypassStrictMode.Load()
}
