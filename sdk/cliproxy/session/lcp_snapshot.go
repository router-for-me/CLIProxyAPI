package session

import (
	"container/list"
	"strings"
	"time"
)

// MerklePrefixSnapshotEntry stores the context needed to restore an LCP binding.
type MerklePrefixSnapshotEntry struct {
	Namespace         string    `json:"namespace"`
	AuthID            string    `json:"auth_id"`
	SessionID         string    `json:"session_id"`
	ParentSessionID   string    `json:"parent_session_id,omitempty"`
	MinPrefixLength   int       `json:"min_prefix_length"`
	IsFork            bool      `json:"is_fork,omitempty"`
	IsCompaction      bool      `json:"is_compaction,omitempty"`
	NodeKind          string    `json:"node_kind,omitempty"`
	EnvironmentDigest string    `json:"environment_digest,omitempty"`
	Fingerprints      []string  `json:"fingerprints"`
	TailFingerprints  []string  `json:"tail_fingerprints,omitempty"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// Snapshot returns detached groups in eviction order and the captured revision.
// Input order on restore preserves both LRU eviction and newest-match selection.
func (m *MerklePrefixMatcher) Snapshot() ([]MerklePrefixSnapshotEntry, uint64) {
	if m == nil {
		return nil, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepareLocked()
	m.cleanupLocked(m.now())
	entries := make([]MerklePrefixSnapshotEntry, 0, m.groupCount)
	for element := m.lru.Front(); element != nil; element = element.Next() {
		group := element.Value.(*lcpGroup)
		entries = append(entries, MerklePrefixSnapshotEntry{
			Namespace: group.namespace, AuthID: group.authID,
			SessionID: group.sessionID, ParentSessionID: group.parentSessionID,
			MinPrefixLength: group.minPrefixLength, IsFork: group.isFork,
			IsCompaction: group.isCompaction, NodeKind: group.nodeKind,
			EnvironmentDigest: group.environmentDigest,
			Fingerprints:      append([]string(nil), group.fingerprints...),
			TailFingerprints:  append([]string(nil), group.tailFingerprints...),
			ExpiresAt:         group.expiresAt,
		})
	}
	return entries, m.revision
}

// Restore replaces bindings with valid snapshot groups, capped by the current TTL
// and capacity. Access generations stay monotonic for in-flight result guards.
func (m *MerklePrefixMatcher) Restore(entries []MerklePrefixSnapshotEntry) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.groups = make(map[string]*lcpNamespace)
	m.lru = list.New()
	m.lruElements = make(map[*lcpGroup]*list.Element)
	m.groupCount = 0
	m.prefixCount = 0
	m.revision++
	now := m.now()
	for _, item := range entries {
		fingerprints, minPrefixLength, ok := m.sanitizeFingerprints(item.Fingerprints, item.MinPrefixLength)
		if !ok || strings.TrimSpace(item.Namespace) == "" || strings.TrimSpace(item.AuthID) == "" || !now.Before(item.ExpiresAt) {
			continue
		}
		group := &lcpGroup{
			key: sequenceKey(fingerprints), namespace: item.Namespace, authID: item.AuthID,
			sessionID: item.SessionID, parentSessionID: item.ParentSessionID,
			minPrefixLength: minPrefixLength, isFork: item.IsFork,
			isCompaction: item.IsCompaction, nodeKind: item.NodeKind,
			environmentDigest: item.EnvironmentDigest,
			fingerprints:      append([]string(nil), fingerprints...),
			tailFingerprints:  extractTailFingerprints(item.TailFingerprints),
			prefixKeys:        rollingPrefixKeys(fingerprints),
			expiresAt:         item.ExpiresAt,
		}
		if limit := now.Add(m.ttl); m.ttl > 0 && group.expiresAt.After(limit) {
			group.expiresAt = limit
		}
		if len(group.tailFingerprints) == 0 {
			group.tailFingerprints = extractTailFingerprints(group.fingerprints)
		}
		if group.environmentDigest == "" {
			group.environmentDigest = fallbackEnvironmentDigest(group.fingerprints, minPrefixLength)
		}
		if group.sessionID == "" {
			group.sessionID = newLCPSessionID(group.namespace, group.prefixKeys[minPrefixLength-1])
		}
		if ns := m.groups[group.namespace]; ns != nil {
			if previous := ns.groups[group.key]; previous != nil {
				m.removeGroupLocked(previous)
			}
		}
		m.addGroupLocked(group)
	}
}

// SetTTL updates the lifetime used by future matches and result callbacks.
func (m *MerklePrefixMatcher) SetTTL(ttl time.Duration) {
	if m == nil || ttl <= 0 {
		return
	}
	m.mu.Lock()
	m.ttl = ttl
	m.mu.Unlock()
}
