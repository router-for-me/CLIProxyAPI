package auth

import (
	"container/list"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const (
	defaultHomeSessionAliasTTL = time.Hour
	homeSessionAliasCleanupOps = 256
	homeSessionAliasSoftLimit  = 4096
)

type homeSessionAliasEntry struct {
	canonical string
	expiresAt time.Time
	aliases   []string
}

// homeSessionAliasCache reconciles multiple client identifiers for one Home
// session without changing Home's single-session-ID protocol.
type homeSessionAliasCache struct {
	mu               sync.Mutex
	entries          map[string]homeSessionAliasEntry
	groups           map[string]homeSessionAliasEntry
	evictionOrder    *list.List
	evictionElements map[string]*list.Element
	ops              uint64
}

func (c *homeSessionAliasCache) canonical(primary, fallback string, ttl time.Duration, now time.Time) string {
	primary = strings.TrimSpace(primary)
	fallback = strings.TrimSpace(fallback)
	if primary == "" {
		return ""
	}
	if ttl <= 0 {
		ttl = defaultHomeSessionAliasTTL
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	c.ops++
	if c.ops%homeSessionAliasCleanupOps == 0 {
		c.cleanupLocked(now)
	}

	canonical := primary
	aliases := mergeSessionAliases(nil, primary, fallback)
	previousGroups := make(map[string]homeSessionAliasEntry, 2)
	remember := func(entry homeSessionAliasEntry) {
		previousGroups[entry.canonical] = entry
	}

	primaryFound := false
	canonicalFromLiveAlias := false
	if existing, ok := c.entryLocked(primary, now); ok {
		primaryFound = true
		canonicalFromLiveAlias = true
		canonical = existing.canonical
		remember(existing)
		aliases = mergeSessionAliases(aliases, existing.aliases...)
	}
	if fallback != "" && fallback != primary {
		if existing, ok := c.entryLocked(fallback, now); ok {
			canonicalFromLiveAlias = true
			if !primaryFound {
				canonical = existing.canonical
			}
			remember(existing)
			aliases = mergeSessionAliases(aliases, existing.aliases...)
		}
	}
	if canonicalFromLiveAlias {
		if existing, ok := c.groupLocked(canonical, now); ok {
			remember(existing)
			aliases = mergeSessionAliases(aliases, existing.aliases...)
		}
	}
	if !canonicalFromLiveAlias {
		if _, ok := c.groupLocked(canonical, now); ok {
			return canonical
		}
	}
	aliases = compactHomeSessionAliases(mergeSessionAliases(aliases, canonical))
	for _, previous := range previousGroups {
		c.removeGroupLocked(previous)
	}

	c.setGroupLocked(homeSessionAliasEntry{
		canonical: canonical,
		expiresAt: now.Add(ttl),
		aliases:   aliases,
	})
	c.enforceLimitLocked(homeSessionAliasSoftLimit)
	return canonical
}

func (c *homeSessionAliasCache) ensureInitializedLocked() {
	if c.entries == nil {
		c.entries = make(map[string]homeSessionAliasEntry)
	}
	if c.groups == nil {
		c.groups = make(map[string]homeSessionAliasEntry)
	}
	if c.evictionOrder == nil {
		c.evictionOrder = list.New()
	}
	if c.evictionElements == nil {
		c.evictionElements = make(map[string]*list.Element)
	}
}

func (c *homeSessionAliasCache) entryLocked(alias string, now time.Time) (homeSessionAliasEntry, bool) {
	entry, ok := c.entries[alias]
	if !ok {
		return homeSessionAliasEntry{}, false
	}
	if now.Before(entry.expiresAt) {
		return entry, true
	}
	if group, exists := c.groups[entry.canonical]; exists && sameHomeSessionAliasGroup(group, entry) {
		c.removeGroupLocked(group)
	} else {
		delete(c.entries, alias)
	}
	return homeSessionAliasEntry{}, false
}

func (c *homeSessionAliasCache) groupLocked(canonical string, now time.Time) (homeSessionAliasEntry, bool) {
	entry, ok := c.groups[canonical]
	if !ok {
		return homeSessionAliasEntry{}, false
	}
	if now.Before(entry.expiresAt) {
		return entry, true
	}
	c.removeGroupLocked(entry)
	return homeSessionAliasEntry{}, false
}

func (c *homeSessionAliasCache) setGroupLocked(entry homeSessionAliasEntry) {
	if existing, ok := c.groups[entry.canonical]; ok {
		c.removeGroupLocked(existing)
	}
	entry.aliases = append([]string(nil), entry.aliases...)
	c.groups[entry.canonical] = entry
	for _, alias := range entry.aliases {
		c.entries[alias] = entry
	}
	c.evictionElements[entry.canonical] = c.evictionOrder.PushBack(entry.canonical)
}

func (c *homeSessionAliasCache) removeGroupLocked(entry homeSessionAliasEntry) {
	current, ok := c.groups[entry.canonical]
	if !ok || !sameHomeSessionAliasGroup(current, entry) {
		return
	}
	for _, alias := range current.aliases {
		mapped, exists := c.entries[alias]
		if exists && sameHomeSessionAliasGroup(mapped, current) {
			delete(c.entries, alias)
		}
	}
	delete(c.groups, current.canonical)
	if element, exists := c.evictionElements[current.canonical]; exists {
		c.evictionOrder.Remove(element)
		delete(c.evictionElements, current.canonical)
	}
}

func sameHomeSessionAliasGroup(left, right homeSessionAliasEntry) bool {
	return left.canonical == right.canonical && left.expiresAt.Equal(right.expiresAt) &&
		equalSessionAliases(left.aliases, right.aliases)
}

func (c *homeSessionAliasCache) enforceLimitLocked(limit int) {
	if limit <= 0 {
		return
	}
	for len(c.entries) > limit {
		oldest := c.evictionOrder.Front()
		if oldest == nil {
			return
		}
		canonical, _ := oldest.Value.(string)
		entry, ok := c.groups[canonical]
		if !ok {
			c.evictionOrder.Remove(oldest)
			delete(c.evictionElements, canonical)
			continue
		}
		c.removeGroupLocked(entry)
	}
}

func (c *homeSessionAliasCache) cleanupLocked(now time.Time) {
	for _, entry := range c.groups {
		if !now.Before(entry.expiresAt) {
			c.removeGroupLocked(entry)
		}
	}
}

func (c *homeSessionAliasCache) clear() {
	c.mu.Lock()
	c.entries = nil
	c.groups = nil
	c.evictionOrder = nil
	c.evictionElements = nil
	c.ops = 0
	c.mu.Unlock()
}

func homeSessionAliasTTL(cfg *internalconfig.Config) time.Duration {
	if cfg == nil {
		return defaultHomeSessionAliasTTL
	}
	raw := strings.TrimSpace(cfg.Routing.SessionAffinityTTL)
	if raw == "" {
		return defaultHomeSessionAliasTTL
	}
	parsed, errParse := time.ParseDuration(raw)
	if errParse != nil || parsed <= 0 {
		return defaultHomeSessionAliasTTL
	}
	return parsed
}

func (m *Manager) homeDispatchSessionID(opts cliproxyexecutor.Options) string {
	primary, fallback := extractSessionIDs(opts.Headers, opts.OriginalRequest, opts.Metadata)
	if primary == "" || m == nil {
		return primary
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return m.homeSessionAliases.canonical(primary, fallback, homeSessionAliasTTL(cfg), time.Now())
}
