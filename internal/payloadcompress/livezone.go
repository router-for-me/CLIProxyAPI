package payloadcompress

import (
	"crypto/sha256"
	"sync"
	"time"
)

// Live zone is a per-session memo of the last compression result.
//
// Every engine here is a pure function, so recompressing an unchanged prefix
// yields identical bytes and costs no tokens. The zone therefore buys CPU, not
// savings: it lets a long conversation skip the whole engine pipeline for
// everything the previous request already compressed and run it only over the
// newly appended tail. It mirrors OmniRoute's liveZoneCompression.
//
// The reuse proof is a single body hash: if the body up to the previously
// compressed length is byte-identical, every one of those segments is
// unchanged, sits at the same offset, and its compressed form is still correct.
// The session id comes from the caller, which is this proxy's native affinity
// identity, so a conversation is bucketed here exactly as it is for routing.
type liveZoneEntry struct {
	// count is the number of segments texts covers.
	count int
	// rawLen is the body length those segments spanned, and hash digests it.
	rawLen int
	hash   [sha256.Size]byte
	// transformed holds the compressed text of every segment, prefix included.
	transformed []string
	at          time.Time
}

const (
	liveZoneMaxEntries = 100
	liveZoneSweepEvery = 64
	// liveZoneTTL is upstream's DEFAULT_TTL_MINUTES. Upstream passes it as a call
	// argument, never as config, so this is not tunable here either.
	liveZoneTTL = 5 * time.Minute
)

type liveZoneCache struct {
	mu      sync.Mutex
	entries map[string]*liveZoneEntry
	order   []string
	ops     int
}

var liveZone = &liveZoneCache{entries: make(map[string]*liveZoneEntry)}

// liveZoneKey identifies one conversation. The session id already folds in the
// model, provider, system prompt, first user message and tool list, so a config
// or tool change gets a fresh entry instead of a stale reuse.
func liveZoneKey(model, sessionID string) string {
	return model + "\x00" + sessionID
}

// liveZoneReuse reports how many leading segments a previous request already
// compressed, and returns that request's entry. start is 0 on a miss.
func liveZoneReuse(cfg Config, model, sessionID string, body []byte, segs []segment) (*liveZoneEntry, int) {
	key := liveZoneKey(model, sessionID)
	liveZone.mu.Lock()
	entry := liveZone.entries[key]
	if entry != nil && time.Since(entry.at) > liveZoneTTL {
		entry = nil
	}
	liveZone.mu.Unlock()
	if entry == nil || entry.count == 0 || entry.count > len(segs) {
		return nil, 0
	}
	if entry.rawLen <= 0 || entry.rawLen > len(body) {
		return nil, 0
	}
	// The previously compressed region must be a byte-identical prefix. When it
	// is, every one of those segments is unchanged at the same offset.
	if sha256.Sum256(body[:entry.rawLen]) != entry.hash {
		return nil, 0
	}
	return entry, entry.count
}

// liveZoneStore records the compressed result of this request so the next turn
// can reuse it. Nothing is stored in shadow mode, where the caller gets the raw
// body back and the zone would describe a payload that was never sent.
func liveZoneStore(cfg Config, model, sessionID string, body []byte, segs []segment, texts []string) {
	if !cfg.LiveZone.Enabled || cfg.Shadow || sessionID == "" {
		return
	}
	if len(segs) == 0 || len(texts) != len(segs) {
		return
	}
	rawLen := segs[len(segs)-1].end
	if rawLen <= 0 || rawLen > len(body) {
		return
	}
	entry := &liveZoneEntry{
		count:       len(segs),
		rawLen:      rawLen,
		hash:        sha256.Sum256(body[:rawLen]),
		transformed: append(make([]string, 0, len(texts)), texts...),
		at:          time.Now(),
	}

	key := liveZoneKey(model, sessionID)
	liveZone.mu.Lock()
	defer liveZone.mu.Unlock()
	if _, exists := liveZone.entries[key]; !exists {
		liveZone.order = append(liveZone.order, key)
	}
	liveZone.entries[key] = entry
	liveZone.ops++
	if liveZone.ops%liveZoneSweepEvery == 0 {
		liveZone.sweep(liveZoneTTL)
	}
	for len(liveZone.order) > liveZoneMaxEntries {
		drop := liveZone.order[0]
		liveZone.order = liveZone.order[1:]
		delete(liveZone.entries, drop)
	}
}

// sweep drops expired entries. Callers hold mu.
func (c *liveZoneCache) sweep(ttl time.Duration) {
	now := time.Now()
	kept := c.order[:0]
	for _, key := range c.order {
		if entry := c.entries[key]; entry != nil && now.Sub(entry.at) > ttl {
			delete(c.entries, key)
			continue
		}
		kept = append(kept, key)
	}
	c.order = kept
}

// resetLiveZone clears the zone. Tests use it to isolate sessions.
func resetLiveZone() {
	liveZone.mu.Lock()
	defer liveZone.mu.Unlock()
	liveZone.entries = make(map[string]*liveZoneEntry)
	liveZone.order = nil
	liveZone.ops = 0
}
