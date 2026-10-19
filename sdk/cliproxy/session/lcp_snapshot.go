package session

import (
	"container/list"
	"sort"
	"strings"
	"time"
)

// MerklePrefixSnapshotEntry is a durable LCP affinity group.
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

// Snapshot returns non-expired LCP groups from least to most recently used, so
// Restore (which assigns access order in input order) keeps the recency that
// newest-match tie-breaking and LRU eviction depend on.
func (m *MerklePrefixMatcher) Snapshot() []MerklePrefixSnapshotEntry {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepareLocked()
	now := m.now()
	entries := make([]MerklePrefixSnapshotEntry, 0, m.groupCount)
	recency := make([]uint64, 0, m.groupCount)
	for _, ns := range m.groups {
		for _, group := range ns.groups {
			if group == nil || group.authID == "" || !now.Before(group.expiresAt) {
				continue
			}
			entries = append(entries, MerklePrefixSnapshotEntry{
				Namespace:         group.namespace,
				AuthID:            group.authID,
				SessionID:         group.sessionID,
				ParentSessionID:   group.parentSessionID,
				MinPrefixLength:   group.minPrefixLength,
				IsFork:            group.isFork,
				IsCompaction:      group.isCompaction,
				NodeKind:          group.nodeKind,
				EnvironmentDigest: group.environmentDigest,
				Fingerprints:      append([]string(nil), group.fingerprints...),
				TailFingerprints:  append([]string(nil), group.tailFingerprints...),
				ExpiresAt:         group.expiresAt,
			})
			recency = append(recency, group.lastAccessNumber)
		}
	}
	order := make([]int, len(entries))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(i, j int) bool {
		left, right := order[i], order[j]
		if recency[left] != recency[right] {
			return recency[left] < recency[right]
		}
		if entries[left].Namespace != entries[right].Namespace {
			return entries[left].Namespace < entries[right].Namespace
		}
		return sequenceKey(entries[left].Fingerprints) < sequenceKey(entries[right].Fingerprints)
	})
	sorted := make([]MerklePrefixSnapshotEntry, len(entries))
	for index, source := range order {
		sorted[index] = entries[source]
	}
	return sorted
}

// Restore replaces the matcher with non-expired snapshot groups, capping each
// expiry at the matcher's current TTL.
func (m *MerklePrefixMatcher) Restore(entries []MerklePrefixSnapshotEntry) int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.groups = make(map[string]*lcpNamespace)
	m.lru.Init()
	m.lruElements = make(map[*lcpGroup]*list.Element)
	m.groupCount = 0
	m.prefixCount = 0
	m.accessCounter = 0
	m.operations = 0
	now := m.now()
	restored := 0
	for _, item := range entries {
		fingerprints, minPrefixLength, ok := m.sanitizeFingerprints(item.Fingerprints, item.MinPrefixLength)
		if !ok || strings.TrimSpace(item.Namespace) == "" || strings.TrimSpace(item.AuthID) == "" || !now.Before(item.ExpiresAt) {
			continue
		}
		group := &lcpGroup{
			key:               sequenceKey(fingerprints),
			namespace:         strings.TrimSpace(item.Namespace),
			authID:            strings.TrimSpace(item.AuthID),
			sessionID:         item.SessionID,
			parentSessionID:   item.ParentSessionID,
			minPrefixLength:   minPrefixLength,
			isFork:            item.IsFork,
			isCompaction:      item.IsCompaction,
			nodeKind:          item.NodeKind,
			environmentDigest: item.EnvironmentDigest,
			fingerprints:      append([]string(nil), fingerprints...),
			tailFingerprints:  append([]string(nil), item.TailFingerprints...),
			prefixKeys:        rollingPrefixKeys(fingerprints),
			expiresAt:         item.ExpiresAt,
		}
		// Never restore an expiry beyond the current TTL (it may have been lowered
		// since the snapshot was written).
		if limit := now.Add(m.ttl); m.ttl > 0 && group.expiresAt.After(limit) {
			group.expiresAt = limit
		}
		if len(group.tailFingerprints) == 0 {
			group.tailFingerprints = extractTailFingerprints(group.fingerprints)
		}
		if group.environmentDigest == "" {
			group.environmentDigest = environmentDigest(group.fingerprints, minPrefixLength)
		}
		if group.sessionID == "" {
			firstKey := ""
			if len(group.prefixKeys) > 0 {
				firstKey = group.prefixKeys[minPrefixLength-1]
			}
			group.sessionID = newLCPSessionID(group.namespace, firstKey)
		}
		m.addGroupLocked(group)
		restored++
	}
	return restored
}
