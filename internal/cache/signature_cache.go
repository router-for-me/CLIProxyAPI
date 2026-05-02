package cache

import (
	"crypto/sha256"
	"encoding/hex"
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

	// SignatureGroupCapacity bounds the per-group LRU. Hash space is 64-bit, so
	// without a bound the cache grew indefinitely with unique requests. The
	// bound trades a hit-rate floor for a memory ceiling; eviction policy is
	// least-recently-used (sliding TTL refresh on read also bumps recency).
	SignatureGroupCapacity = 10_000
)

// signatureCache stores signatures by model group -> textHash -> SignatureEntry
var signatureCache sync.Map

// cacheCleanupOnce ensures the background cleanup goroutine starts only once
var cacheCleanupOnce sync.Once

// clock returns the current time. Tests override it to exercise TTL passage
// without sleeping. Production callers always see time.Now().
var clock = time.Now

// lruEntry is one signature kept in the per-group LRU's doubly-linked list.
type lruEntry struct {
	textHash   string
	signature  string
	timestamp  time.Time
	prev, next *lruEntry
}

// groupCache is the inner per-model-group cache: a bounded LRU keyed by
// textHash. Sliding TTL is maintained by refreshing entry.timestamp on every
// successful read; the linked list maintains recency order for eviction when
// the entry count exceeds capacity.
//
// All operations hold mu — moveToFront on read mutates the list, so an
// RWMutex would not give read-side concurrency anyway.
type groupCache struct {
	mu       sync.Mutex
	entries  map[string]*lruEntry
	head     *lruEntry // sentinel; head.next is most-recently-used
	tail     *lruEntry // sentinel; tail.prev is least-recently-used
	capacity int
}

func newGroupCache(capacity int) *groupCache {
	head := &lruEntry{}
	tail := &lruEntry{}
	head.next = tail
	tail.prev = head
	return &groupCache{
		entries:  make(map[string]*lruEntry),
		head:     head,
		tail:     tail,
		capacity: capacity,
	}
}

// link inserts e immediately after head (most-recently-used position).
func (c *groupCache) link(e *lruEntry) {
	e.prev = c.head
	e.next = c.head.next
	c.head.next.prev = e
	c.head.next = e
}

// unlink removes e from the list.
func (c *groupCache) unlink(e *lruEntry) {
	e.prev.next = e.next
	e.next.prev = e.prev
	e.prev = nil
	e.next = nil
}

func (c *groupCache) moveToFront(e *lruEntry) {
	c.unlink(e)
	c.link(e)
}

// get returns the signature and true if the entry exists and is within TTL,
// refreshing its timestamp (sliding) and bumping it to most-recently-used.
// Expired or missing entries return ("", false) and remove themselves.
func (c *groupCache) get(textHash string, now time.Time, ttl time.Duration) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[textHash]
	if !ok {
		return "", false
	}
	if now.Sub(e.timestamp) > ttl {
		c.unlink(e)
		delete(c.entries, textHash)
		return "", false
	}
	e.timestamp = now
	c.moveToFront(e)
	return e.signature, true
}

// set inserts or updates an entry, evicting the LRU tail if capacity is exceeded.
func (c *groupCache) set(textHash, signature string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[textHash]; ok {
		e.signature = signature
		e.timestamp = now
		c.moveToFront(e)
		return
	}
	e := &lruEntry{textHash: textHash, signature: signature, timestamp: now}
	c.link(e)
	c.entries[textHash] = e
	if len(c.entries) > c.capacity {
		oldest := c.tail.prev
		if oldest != c.head {
			c.unlink(oldest)
			delete(c.entries, oldest.textHash)
		}
	}
}

// purgeExpired removes entries older than ttl. Returns true if the group is
// now empty (caller may delete the group entirely).
func (c *groupCache) purgeExpired(now time.Time, ttl time.Duration) (empty bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if now.Sub(e.timestamp) > ttl {
			c.unlink(e)
			delete(c.entries, k)
		}
	}
	return len(c.entries) == 0
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
	sc := newGroupCache(SignatureGroupCapacity)
	actual, _ := signatureCache.LoadOrStore(groupKey, sc)
	return actual.(*groupCache)
}

// startCacheCleanup launches a background goroutine that periodically
// removes caches where all entries have expired.
func startCacheCleanup() {
	go func() {
		ticker := time.NewTicker(CacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredCaches()
		}
	}()
}

// purgeExpiredCaches removes caches with no valid (non-expired) entries.
func purgeExpiredCaches() {
	now := clock()
	signatureCache.Range(func(key, value any) bool {
		sc := value.(*groupCache)
		if sc.purgeExpired(now, SignatureCacheTTL) {
			signatureCache.Delete(key)
		}
		return true
	})
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
	sc.set(textHash, signature, clock())
}

// GetCachedSignature retrieves a cached signature for a given model group and text.
// Returns empty string if not found or expired. For the gemini group, returns the
// "skip_thought_signature_validator" miss sentinel for empty/miss/expired keys.
func GetCachedSignature(modelName, text string) string {
	groupKey := GetModelGroup(modelName)

	if text == "" {
		if groupKey == "gemini" {
			return "skip_thought_signature_validator"
		}
		return ""
	}
	val, ok := signatureCache.Load(groupKey)
	if !ok {
		if groupKey == "gemini" {
			return "skip_thought_signature_validator"
		}
		return ""
	}
	sc := val.(*groupCache)
	textHash := hashText(text)

	sig, hit := sc.get(textHash, clock(), SignatureCacheTTL)
	if !hit {
		if groupKey == "gemini" {
			return "skip_thought_signature_validator"
		}
		return ""
	}
	return sig
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
