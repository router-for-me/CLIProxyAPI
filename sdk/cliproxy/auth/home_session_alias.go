package auth

import (
	"container/list"
	"sort"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
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

func (c *homeSessionAliasCache) canonicalForCaller(callerScope string, aliases []string, ttl time.Duration, now time.Time) string {
	callerScope = strings.TrimSpace(callerScope)
	if callerScope == "" {
		return c.canonical(aliases, ttl, now)
	}
	suffix := "\x00caller:" + callerScope
	scopedAliases := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if alias = strings.TrimSpace(alias); alias != "" {
			scopedAliases = append(scopedAliases, alias+suffix)
		}
	}
	canonical := c.canonical(scopedAliases, ttl, now)
	return strings.TrimSuffix(canonical, suffix)
}

func (c *homeSessionAliasCache) canonical(aliases []string, ttl time.Duration, now time.Time) string {
	cleaned := make([]string, 0, len(aliases))
	seenInput := make(map[string]struct{}, len(aliases))
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		if a != "" {
			if _, ok := seenInput[a]; !ok {
				seenInput[a] = struct{}{}
				cleaned = append(cleaned, a)
			}
		}
	}
	if len(cleaned) == 0 {
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

	previousGroups := make(map[string]homeSessionAliasEntry)
	var matchedCanonicals []string
	matchedCanonicalSet := make(map[string]struct{})

	allAliases := mergeSessionAliases(nil, cleaned...)

	canonicalFromLiveAlias := false
	for _, a := range cleaned {
		if existing, ok := c.entryLocked(a, now); ok {
			canonicalFromLiveAlias = true
			previousGroups[existing.canonical] = existing
			allAliases = mergeSessionAliases(allAliases, existing.aliases...)
			if _, ok := matchedCanonicalSet[existing.canonical]; !ok {
				matchedCanonicalSet[existing.canonical] = struct{}{}
				matchedCanonicals = append(matchedCanonicals, existing.canonical)
			}
		}
	}

	for _, canon := range matchedCanonicals {
		if existing, ok := c.groupLocked(canon, now); ok {
			previousGroups[existing.canonical] = existing
			allAliases = mergeSessionAliases(allAliases, existing.aliases...)
		}
	}

	var canonical string
	if len(matchedCanonicals) == 0 {
		canonical = cleaned[0]
	} else if len(matchedCanonicals) == 1 {
		canonical = matchedCanonicals[0]
	} else {
		sort.Strings(matchedCanonicals)
		canonical = matchedCanonicals[0]
	}

	if !canonicalFromLiveAlias {
		if _, ok := c.groupLocked(canonical, now); ok {
			return canonical
		}
	}

	allAliases = compactHomeSessionAliases(mergeSessionAliases(allAliases, canonical))
	for _, previous := range previousGroups {
		c.removeGroupLocked(previous)
	}

	c.setGroupLocked(homeSessionAliasEntry{
		canonical: canonical,
		expiresAt: now.Add(ttl),
		aliases:   allAliases,
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

// homeDispatchSession resolves the canonical session Home should route on, together
// with the structured identity and aliases Home needs to reconcile the same session
// across CPA nodes that observed it through different signals.
func (m *Manager) homeDispatchSession(opts cliproxyexecutor.Options) home.DispatchSession {
	identity := sessionIdentityFromOptions(opts)
	session := home.DispatchSession{
		ID:             identity.ID,
		Aliases:        identity.Aliases,
		Source:         identity.Source,
		Confidence:     identity.Confidence,
		Scope:          identity.Scope,
		ClientType:     identity.ClientType,
		ThreadID:       identity.ThreadID,
		ParentThreadID: identity.ParentThreadID,
		RequestKind:    identity.RequestKind,
		ThreadSource:   identity.ThreadSource,
		TurnID:         identity.TurnID,
		ClientProvided: identity.ClientProvided,
	}
	if session.ID == "" || m == nil {
		return session
	}
	observedIDs := identity.IDs()
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	callerScope := optionsMetadataString(opts, cliproxyexecutor.CallerScopeMetadataKey)
	canonical := m.homeSessionAliases.canonicalForCaller(callerScope, observedIDs, homeSessionAliasTTL(cfg), time.Now())
	if canonical == "" {
		return session
	}
	session.ID = canonical
	aliases := make([]string, 0, len(observedIDs)-1)
	for _, id := range observedIDs {
		if id != canonical {
			aliases = append(aliases, id)
		}
	}
	session.Aliases = aliases
	return session
}
