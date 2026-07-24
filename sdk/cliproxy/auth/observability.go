package auth

import (
	"sort"
	"strings"
	"time"
)

// SessionAffinitySnapshotItem describes one sticky session binding.
type SessionAffinitySnapshotItem struct {
	SessionID  string    `json:"session_id"`
	AuthID     string    `json:"auth_id"`
	Provider   string    `json:"provider"`
	ModelKey   string    `json:"model_key"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// HashPreviewItem provides per-auth balanced-hash score diagnostics.
type HashPreviewItem struct {
	AuthID      string  `json:"auth_id"`
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	HashScore   float64 `json:"hash_score"`
	Freshness   float64 `json:"freshness_score"`
	Quota       float64 `json:"quota_score"`
	Penalty     float64 `json:"penalty_score"`
	Total       float64 `json:"total_score"`
	Blocked     bool    `json:"blocked"`
	BlockReason string  `json:"block_reason,omitempty"`
}

// SessionAffinitySnapshot returns active sticky-session bindings touched within window.
func (m *Manager) SessionAffinitySnapshot(window time.Duration) []SessionAffinitySnapshotItem {
	if m == nil {
		return nil
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	cutoff := time.Now().UTC().Add(-window)
	items := make([]SessionAffinitySnapshotItem, 0)

	m.mu.Lock()
	if m.sessionAffinity == nil {
		m.mu.Unlock()
		return items
	}
	for sessionID, binding := range m.sessionAffinity {
		lastSeen := m.sessionAffinitySeenAt[sessionID]
		if lastSeen.IsZero() || lastSeen.Before(cutoff) {
			// Opportunistically clean stale entries while scanning diagnostics.
			m.deleteSessionAffinityLocked(sessionID)
			continue
		}
		items = append(items, SessionAffinitySnapshotItem{
			SessionID:  sessionID,
			AuthID:     strings.TrimSpace(binding.AuthID),
			Provider:   strings.TrimSpace(binding.Provider),
			ModelKey:   strings.TrimSpace(binding.ModelKey),
			LastSeenAt: lastSeen,
		})
	}
	m.mu.Unlock()

	sort.Slice(items, func(i, j int) bool {
		if items[i].LastSeenAt.Equal(items[j].LastSeenAt) {
			return items[i].SessionID < items[j].SessionID
		}
		return items[i].LastSeenAt.After(items[j].LastSeenAt)
	})
	return items
}

// BalancedHashPreview returns score breakdown for eligible auths.
func (m *Manager) BalancedHashPreview(provider, model, requestKey string) []HashPreviewItem {
	if m == nil {
		return nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	modelKey := canonicalModelKey(model)
	if modelKey == "" {
		modelKey = model
	}
	requestKey = strings.TrimSpace(requestKey)
	if requestKey == "" {
		requestKey = time.Now().UTC().Format("2006-01-02T15:04")
	}
	now := time.Now()

	m.mu.RLock()
	candidates := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		if provider != "" && strings.ToLower(strings.TrimSpace(auth.Provider)) != provider {
			continue
		}
		candidates = append(candidates, auth)
	}
	m.mu.RUnlock()

	items := make([]HashPreviewItem, 0, len(candidates))
	for _, auth := range candidates {
		hashScore := normalizedHashScore(requestKey + "|" + modelKey + "|" + strings.TrimSpace(auth.ID))
		freshness := authFreshnessScore(auth, model, now)
		quota := authQuotaScore(auth, model)
		penalty := authRecentPenalty(auth, model)
		total := (0.40 * hashScore) + (0.25 * freshness) + (0.25 * quota) + (0.10 * (1.0 - penalty))
		blocked, reason, _ := isAuthBlockedForModel(auth, model, now)

		item := HashPreviewItem{
			AuthID:    strings.TrimSpace(auth.ID),
			Provider:  strings.ToLower(strings.TrimSpace(auth.Provider)),
			Model:     model,
			HashScore: hashScore,
			Freshness: freshness,
			Quota:     quota,
			Penalty:   penalty,
			Total:     total,
			Blocked:   blocked,
		}
		if blocked {
			switch reason {
			case blockReasonCooldown:
				item.BlockReason = "cooldown"
			case blockReasonDisabled:
				item.BlockReason = "disabled"
			case blockReasonOther:
				item.BlockReason = "other"
			default:
				item.BlockReason = "unknown"
			}
		}
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Total == items[j].Total {
			return items[i].AuthID < items[j].AuthID
		}
		return items[i].Total > items[j].Total
	})
	return items
}
