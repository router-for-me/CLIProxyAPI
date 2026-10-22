package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	log "github.com/sirupsen/logrus"
)

const sessionSnapshotVersion = 1

type sessionSnapshot struct {
	Version   int                                         `json:"version"`
	Groups    []sessionSnapshotGroup                      `json:"groups"`
	LCPGroups []cliproxysession.MerklePrefixSnapshotEntry `json:"lcp_groups,omitempty"`
}

// Groups are stored from oldest to newest to preserve whole-group eviction.
type sessionSnapshotGroup struct {
	AuthID    string                  `json:"auth_id"`
	Aliases   []string                `json:"aliases"`
	ExpiresAt time.Time               `json:"expires_at"`
	Binding   *sessionSnapshotBinding `json:"binding,omitempty"`
}

// Binding is optional so version-one snapshots without selection history remain valid.
type sessionSnapshotBinding struct {
	SessionID string `json:"session_id"`
	Provider  string `json:"provider"`
	Sequence  uint64 `json:"sequence"`
}

type sessionCachePersistence struct {
	cache            *SessionCache
	path             string
	flushMu          sync.Mutex
	savedRevision    uint64
	savedLCPRevision uint64
	warned           bool
	stopCh           chan struct{}
	doneCh           chan struct{}
	stopOnce         sync.Once
	save             func(string, sessionSnapshot) error
}

func (c *SessionCache) configurePersistence(path string, restore bool) {
	c.persistenceMu.Lock()
	defer c.persistenceMu.Unlock()
	if c.stopped || (c.persistence == nil && path == "") ||
		(c.persistence != nil && c.persistence.path == path) {
		return
	}
	if c.persistence != nil {
		c.persistence.stop()
		c.persistence = nil
	}
	if path == "" {
		return
	}
	p := newSessionCachePersistence(c, path)
	if restore {
		if revision, unchanged := c.restoreSnapshot(path); unchanged {
			p.savedRevision = revision
		}
	} else {
		// Live memory is authoritative, including an empty cache with an old file.
		c.mu.Lock()
		c.revision++
		c.mu.Unlock()
	}
	c.persistence = p
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		p.run(ticker.C)
	}()
}

func newSessionCachePersistence(cache *SessionCache, path string) *sessionCachePersistence {
	return &sessionCachePersistence{
		cache: cache, path: path, stopCh: make(chan struct{}), doneCh: make(chan struct{}),
		save: writeSessionSnapshot,
	}
}

func (p *sessionCachePersistence) run(ticks <-chan time.Time) {
	defer close(p.doneCh)
	for {
		select {
		case <-p.stopCh:
			p.flush()
			return
		case <-ticks:
			p.flush()
		}
	}
}

func (p *sessionCachePersistence) stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	<-p.doneCh
}

func (c *SessionCache) flushPersistence() {
	c.persistenceMu.Lock()
	defer c.persistenceMu.Unlock()
	if c.persistence != nil {
		c.persistence.flush()
	}
}

func (p *sessionCachePersistence) flush() {
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	c := p.cache
	lcpGroups, lcpRevision := c.matcher.Snapshot()
	c.mu.Lock()
	c.cleanupLocked(time.Now())
	revision := c.revision
	if revision == p.savedRevision && lcpRevision == p.savedLCPRevision {
		c.mu.Unlock()
		return
	}
	snapshot := sessionSnapshot{Version: sessionSnapshotVersion, Groups: make([]sessionSnapshotGroup, 0, len(c.groups)), LCPGroups: lcpGroups}
	for element := c.evictionOrder.Front(); element != nil; element = element.Next() {
		group := c.groups[element.Value.(string)]
		stored := sessionSnapshotGroup{
			AuthID: group.authID, Aliases: append([]string(nil), group.aliases...), ExpiresAt: group.expiresAt,
		}
		if group.binding.sequence != 0 {
			stored.Binding = &sessionSnapshotBinding{
				SessionID: group.binding.sessionID, Provider: group.binding.provider, Sequence: group.binding.sequence,
			}
		}
		snapshot.Groups = append(snapshot.Groups, stored)
	}
	c.mu.Unlock()
	if errSave := p.save(p.path, snapshot); errSave != nil {
		if !p.warned {
			log.WithError(errSave).Warn("failed to save session affinity snapshot; will retry")
			p.warned = true
		}
		return
	}
	p.savedRevision = revision
	p.savedLCPRevision = lcpRevision
	p.warned = false
}

// restoreSnapshot returns whether the file already represents the restored state.
func (c *SessionCache) restoreSnapshot(path string) (uint64, bool) {
	data, errRead := os.ReadFile(path)
	if os.IsNotExist(errRead) {
		return 0, false
	}
	if errRead != nil {
		log.WithError(errRead).Warn("failed to read session affinity snapshot; using memory cache")
		return 0, false
	}
	var snapshot sessionSnapshot
	if errDecode := json.Unmarshal(data, &snapshot); errDecode != nil {
		log.Warn("invalid session affinity snapshot; using memory cache")
		return 0, false
	}
	if snapshot.Version != sessionSnapshotVersion {
		log.Warn("unsupported session affinity snapshot version; using memory cache")
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	now := time.Now()
	unchanged := len(c.entries) == 0
	invalid := false
	restored := make(map[string]struct{})
	for _, group := range snapshot.Groups {
		if !validSessionSnapshotGroup(group) {
			invalid = true
			unchanged = false
			continue
		}
		if !now.Before(group.ExpiresAt) {
			unchanged = false
			continue
		}
		if limit := now.Add(c.ttl); c.ttl > 0 && group.ExpiresAt.After(limit) {
			group.ExpiresAt = limit
			unchanged = false
		}
		previous := make([]sessionEntry, 0, len(group.Aliases))
		conflict := false
		for _, alias := range group.Aliases {
			if entry, ok := c.entries[alias]; ok {
				if now.Before(entry.expiresAt) {
					conflict = true
					break
				}
				previous = append(previous, entry)
			}
		}
		if conflict {
			unchanged = false
			continue
		}
		binding := sessionBinding{}
		if stored := group.Binding; stored != nil {
			if stored.Sequence == 0 || strings.TrimSpace(stored.SessionID) == "" || strings.TrimSpace(stored.Provider) == "" {
				// Invalid optional history must not discard an otherwise valid exact binding.
				invalid = true
				unchanged = false
			} else {
				binding = sessionBinding{sessionID: stored.SessionID, provider: stored.Provider, sequence: stored.Sequence}
			}
		}
		groupCount := len(c.groups)
		c.replaceAliasGroupsLocked(group.AuthID, group.ExpiresAt, group.Aliases, binding, previous...)
		restored[group.Aliases[0]] = struct{}{}
		if len(c.groups) < groupCount+1 {
			// Capacity pruning changes the persisted representation.
			unchanged = false
		}
	}
	if c.restoreBindingOrderLocked(restored) {
		unchanged = false
	}
	if invalid {
		log.Warn("ignored invalid data in session affinity snapshot")
	}
	if c.matcher != nil && len(snapshot.LCPGroups) > 0 {
		c.matcher.Restore(snapshot.LCPGroups)
		// Restored groups may be pruned or normalized by TTL and capacity bounds.
		unchanged = false
	}
	if !unchanged {
		c.revision++
	}
	return c.revision, unchanged
}

// Restore selection order separately from eviction order. Live selections are newer
// than restored history; dense ranks also keep persisted counters from overflowing.
func (c *SessionCache) restoreBindingOrderLocked(restored map[string]struct{}) bool {
	keys := make([]string, 0, len(c.groups))
	for key, group := range c.groups {
		if group.binding.sequence != 0 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		_, restoredI := restored[keys[i]]
		_, restoredJ := restored[keys[j]]
		if restoredI != restoredJ {
			return restoredI
		}
		left, right := c.groups[keys[i]].binding.sequence, c.groups[keys[j]].binding.sequence
		if left != right {
			return left < right
		}
		return keys[i] < keys[j]
	})
	changed := false
	for i, key := range keys {
		group := c.groups[key]
		sequence := uint64(i + 1)
		if group.binding.sequence == sequence {
			continue
		}
		group.binding.sequence = sequence
		c.groups[key] = group
		for _, alias := range group.aliases {
			c.entries[alias] = group
		}
		changed = true
	}
	c.bindingSequence = uint64(len(keys))
	return changed
}

func validSessionSnapshotGroup(group sessionSnapshotGroup) bool {
	if strings.TrimSpace(group.AuthID) == "" || group.ExpiresAt.IsZero() || len(group.Aliases) == 0 ||
		len(group.Aliases) > maxStableSessionAliases+1 ||
		!equalSessionAliases(group.Aliases, compactSessionAliases(mergeSessionAliases(nil, group.Aliases...))) {
		return false
	}
	for _, alias := range group.Aliases {
		provider, rest, ok := strings.Cut(alias, "::")
		separator := strings.LastIndex(rest, "::")
		if !ok || provider == "" || separator <= 0 || separator+2 == len(rest) {
			return false
		}
	}
	return true
}

func writeSessionSnapshot(path string, snapshot sessionSnapshot) error {
	data, errEncode := json.Marshal(snapshot)
	if errEncode != nil {
		return fmt.Errorf("encode session affinity snapshot: %w", errEncode)
	}
	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create session affinity directory: %w", errMkdir)
	}
	if errChmod := os.Chmod(dir, 0o700); errChmod != nil {
		return fmt.Errorf("restrict session affinity directory: %w", errChmod)
	}
	file, errCreate := os.CreateTemp(dir, ".session-affinity-*")
	if errCreate != nil {
		return fmt.Errorf("create session affinity temporary file: %w", errCreate)
	}
	closed := false
	defer func() {
		if !closed {
			if errClose := file.Close(); errClose != nil {
				log.WithError(errClose).Warn("failed to close session affinity temporary file")
			}
		}
		if errRemove := os.Remove(file.Name()); errRemove != nil && !os.IsNotExist(errRemove) {
			log.WithError(errRemove).Warn("failed to remove session affinity temporary file")
		}
	}()
	if _, errWrite := file.Write(data); errWrite != nil {
		return fmt.Errorf("write session affinity snapshot: %w", errWrite)
	}
	if errSync := file.Sync(); errSync != nil {
		return fmt.Errorf("sync session affinity snapshot: %w", errSync)
	}
	errClose := file.Close()
	closed = true
	if errClose != nil {
		return fmt.Errorf("close session affinity snapshot: %w", errClose)
	}
	if errRename := os.Rename(file.Name(), path); errRename != nil {
		return fmt.Errorf("replace session affinity snapshot: %w", errRename)
	}
	return nil
}
