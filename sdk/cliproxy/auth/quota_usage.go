package auth

import (
	"net/http"
	"time"
)

// UsageWindow is one allowance window as a management view shows it.
type UsageWindow struct {
	// Label names a window that is not the account's main 5-hour or weekly one,
	// for example a model-scoped weekly limit. Empty for the main windows.
	Label         string    `json:"label,omitempty"`
	UsedPercent   float64   `json:"used_percent"`
	WindowMinutes float64   `json:"window_minutes,omitempty"`
	ResetAt       time.Time `json:"reset_at,omitzero"`
}

// QuotaUsageWindows returns the Claude and Codex windows read from a quota
// observation snapshot, the same figures consume-first ranks on.
func QuotaUsageWindows(quota QuotaState) []UsageWindow {
	windows := quotaWindowsFromSignals(quota.Signals, quota.ObservedAt)
	out := make([]UsageWindow, 0, len(windows))
	for _, window := range windows {
		out = append(out, UsageWindow{UsedPercent: window.used, WindowMinutes: window.minutes, ResetAt: window.resetAt})
	}
	return out
}

// ObserveQuota records quota headers obtained out of band (a provider usage
// endpoint, or a snapshot persisted before a restart) as if the credential had
// returned them on a response. A snapshot older than the current one is
// ignored, so a poll never overwrites fresher headers from live traffic.
func (m *Manager) ObserveQuota(id string, headers http.Header, observedAt time.Time) bool {
	if m == nil || observedAt.IsZero() {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	auth, ok := m.auths[id]
	if !ok || auth == nil || !observedAt.After(auth.Quota.ObservedAt) {
		return false
	}
	return auth.Quota.ObserveResponseHeadersForProvider(auth.Provider, headers, observedAt)
}
