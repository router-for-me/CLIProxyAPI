package management

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	managementAuthCacheTTL        = time.Hour
	managementAuthCacheMaxEntries = 64
)

type managementAuthCacheEntry struct {
	expiresAt time.Time
}

type managementAuthLookup struct {
	done chan struct{}
	ok   bool
}

// managementAuthCache caches verified management secret digests for a bounded TTL
// so repeated polling requests do not re-run bcrypt on every call.
type managementAuthCache struct {
	mu            sync.Mutex
	hmacKey       []byte
	generation    uint64
	entries       map[[sha256.Size]byte]managementAuthCacheEntry
	inflight      map[[sha256.Size]byte]*managementAuthLookup
	nowFunc       func() time.Time
	bcryptCompare func(hashedPassword, password []byte) error
}

func (c *managementAuthCache) now() time.Time {
	if c != nil && c.nowFunc != nil {
		return c.nowFunc()
	}
	return time.Now()
}

func (c *managementAuthCache) compare(hashedPassword, password []byte) error {
	if c != nil && c.bcryptCompare != nil {
		return c.bcryptCompare(hashedPassword, password)
	}
	return bcrypt.CompareHashAndPassword(hashedPassword, password)
}

func (c *managementAuthCache) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.generation++
	clear(c.entries)
	c.mu.Unlock()
}

func (c *managementAuthCache) ensureHMACKeyLocked() bool {
	if len(c.hmacKey) > 0 {
		return true
	}
	key := make([]byte, 32)
	if _, errRead := rand.Read(key); errRead != nil {
		return false
	}
	c.hmacKey = key
	return true
}

func (c *managementAuthCache) digestLocked(secretHash, provided string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, c.hmacKey)
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(secretHash)))
	_, _ = mac.Write(lenBuf[:])
	_, _ = mac.Write([]byte(secretHash))
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(provided)))
	_, _ = mac.Write(lenBuf[:])
	_, _ = mac.Write([]byte(provided))
	var out [sha256.Size]byte
	mac.Sum(out[:0])
	return out
}

func (c *managementAuthCache) evictLocked(now time.Time) {
	for k, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, k)
		}
	}
	for len(c.entries) >= managementAuthCacheMaxEntries {
		var oldestKey [sha256.Size]byte
		var oldestAt time.Time
		first := true
		for k, entry := range c.entries {
			if first || entry.expiresAt.Before(oldestAt) {
				oldestKey = k
				oldestAt = entry.expiresAt
				first = false
			}
		}
		if first {
			break
		}
		delete(c.entries, oldestKey)
	}
}

func (c *managementAuthCache) verify(secretHash, provided string) bool {
	if c == nil || secretHash == "" {
		return false
	}

	c.mu.Lock()
	if !c.ensureHMACKeyLocked() {
		c.mu.Unlock()
		return c.compare([]byte(secretHash), []byte(provided)) == nil
	}
	digest := c.digestLocked(secretHash, provided)
	now := c.now()
	if entry, ok := c.entries[digest]; ok {
		if now.Before(entry.expiresAt) {
			c.mu.Unlock()
			return true
		}
		delete(c.entries, digest)
	}
	if pending := c.inflight[digest]; pending != nil {
		c.mu.Unlock()
		<-pending.done
		return pending.ok
	}
	if c.inflight == nil {
		c.inflight = make(map[[sha256.Size]byte]*managementAuthLookup)
	}
	lookup := &managementAuthLookup{done: make(chan struct{})}
	c.inflight[digest] = lookup
	gen := c.generation
	c.mu.Unlock()

	return c.finishLookup(secretHash, provided, digest, gen, lookup)
}

func (c *managementAuthCache) finishLookup(secretHash, provided string, digest [sha256.Size]byte, gen uint64, lookup *managementAuthLookup) bool {
	ok := false
	defer func() {
		c.mu.Lock()
		if ok && c.generation == gen {
			// Start the TTL after verification completes so cold-path CPU time
			// does not shorten the effective cache window.
			verifiedAt := c.now()
			if c.entries == nil {
				c.entries = make(map[[sha256.Size]byte]managementAuthCacheEntry)
			}
			if len(c.entries) >= managementAuthCacheMaxEntries {
				c.evictLocked(verifiedAt)
			}
			c.entries[digest] = managementAuthCacheEntry{
				expiresAt: verifiedAt.Add(managementAuthCacheTTL),
			}
		}
		lookup.ok = ok
		delete(c.inflight, digest)
		close(lookup.done)
		c.mu.Unlock()
	}()

	ok = c.compare([]byte(secretHash), []byte(provided)) == nil
	return ok
}
