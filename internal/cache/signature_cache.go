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

	// MaxGroupCount caps the number of distinct model groups held by the
	// outer signatureCache. GetModelGroup returns the raw model name for
	// any non-{gpt,claude,gemini} model, so a workload with many unique
	// unknown model names could otherwise grow the outer map until the
	// 10-minute purge ran. With this cap, total memory is bounded at
	// roughly MaxGroupCount * SignatureGroupCapacity entries.
	MaxGroupCount = 64
)

// signatureCache stores signatures by model group -> textHash -> SignatureEntry
var signatureCache sync.Map

// groupCount tracks the number of groups currently in signatureCache so the
// fast read path can stay sync.Map.Load. Written under groupEvictMu when
// inserting/evicting.
var groupCount atomic.Int64

// groupEvictMu serialises eviction passes. The fast read path never takes it.
var groupEvictMu sync.Mutex

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
//
// lastAccess (unix nanos, atomic) tracks the most recent get/set on this
// group so the outer-map eviction can pick the least-recently-used group
// without taking mu.
type groupCache struct {
	mu         sync.Mutex
	entries    map[string]*lruEntry
	head       *lruEntry // sentinel; head.next is most-recently-used
	tail       *lruEntry // sentinel; tail.prev is least-recently-used
	capacity   int
	lastAccess atomic.Int64
}

func newGroupCache(capacity int, now time.Time) *groupCache {
	head := &lruEntry{}
	tail := &lruEntry{}
	head.next = tail
	tail.prev = head
	c := &groupCache{
		entries:  make(map[string]*lruEntry),
		head:     head,
		tail:     tail,
		capacity: capacity,
	}
	// Initialise lastAccess to creation time so a freshly inserted group
	// is not the LRU eviction target before its first set/get fires
	// (Codex Stage 1 exit review IMPORTANT BE-2).
	c.lastAccess.Store(now.UnixNano())
	return c
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
//
// lastAccess is intentionally NOT updated on read. CacheSignature fires
// every time the system caches a new sig, so writes alone keep the
// group's lastAccess fresh. Bumping it on every read added per-call
// atomic.Int64 contention that pushed Get_Hit_Parallel past the ±5%
// bench target. The LRU policy degrades to "least recently written
// group" — fine because production model surfaces stay well under the
// 64-group cap and eviction effectively never fires.
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
	c.lastAccess.Store(now.UnixNano())
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

// getOrCreateGroupCache gets or creates a cache bucket for a model group.
// Cold inserts (and any required LRU eviction to honor MaxGroupCount)
// are serialised under groupEvictMu so the cap holds exactly under
// concurrent first-time inserts (Codex Stage 1 exit review IMPORTANT
// BE-2). The fast-path Load remains lock-free.
func getOrCreateGroupCache(groupKey string) *groupCache {
	// Start background cleanup on first access
	cacheCleanupOnce.Do(startCacheCleanup)

	if val, ok := signatureCache.Load(groupKey); ok {
		return val.(*groupCache)
	}

	groupEvictMu.Lock()
	defer groupEvictMu.Unlock()

	// Re-check after taking the lock — another inserter may have just
	// stored this exact group key.
	if val, ok := signatureCache.Load(groupKey); ok {
		return val.(*groupCache)
	}

	// Evict in a loop while we are at or above the cap. Without the loop,
	// a burst of concurrent inserts that all observed count<cap before
	// taking the lock could each insert and push the count past the cap;
	// the loop ensures only one inserter at a time crosses the boundary.
	for groupCount.Load() >= MaxGroupCount {
		if !evictOldestGroupLocked() {
			break
		}
	}

	sc := newGroupCache(SignatureGroupCapacity, clock())
	if actual, loaded := signatureCache.LoadOrStore(groupKey, sc); loaded {
		return actual.(*groupCache)
	}
	groupCount.Add(1)
	return sc
}

// evictOldestGroupLocked removes the group with the oldest lastAccess
// timestamp. Caller must hold groupEvictMu. Returns true if an eviction
// occurred. False means the outer map was empty; the caller should
// stop trying to evict.
func evictOldestGroupLocked() bool {
	var oldestKey any
	oldestAccess := int64(1<<63 - 1)
	signatureCache.Range(func(key, value any) bool {
		sc := value.(*groupCache)
		if access := sc.lastAccess.Load(); access < oldestAccess {
			oldestAccess = access
			oldestKey = key
		}
		return true
	})
	if oldestKey != nil {
		if _, ok := signatureCache.LoadAndDelete(oldestKey); ok {
			groupCount.Add(-1)
			return true
		}
	}
	return false
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
// Decrements groupCount for each group it removes so the outer-map cap
// stays in sync.
func purgeExpiredCaches() {
	now := clock()
	signatureCache.Range(func(key, value any) bool {
		sc := value.(*groupCache)
		if sc.purgeExpired(now, SignatureCacheTTL) {
			if _, ok := signatureCache.LoadAndDelete(key); ok {
				groupCount.Add(-1)
			}
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
// Keeps groupCount in sync with the outer map size.
func ClearSignatureCache(modelName string) {
	if modelName == "" {
		signatureCache.Range(func(key, _ any) bool {
			if _, ok := signatureCache.LoadAndDelete(key); ok {
				groupCount.Add(-1)
			}
			return true
		})
		return
	}
	groupKey := GetModelGroup(modelName)
	if _, ok := signatureCache.LoadAndDelete(groupKey); ok {
		groupCount.Add(-1)
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
