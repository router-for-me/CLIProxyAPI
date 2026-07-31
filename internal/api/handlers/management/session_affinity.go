package management

import (
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// sessionAffinityRow is the dashboard-facing projection of one logical
// session (grouped across models/providers when a single sessionID has several
// bindings). Mirrors the cooldown_providers row shape so the dashboard can
// reuse the same table chrome.
type sessionAffinityRow struct {
	SessionID  string   `json:"session_id"`
	Provider   string   `json:"provider,omitempty"`
	AuthID     string   `json:"auth_id"`
	AuthIndex  string   `json:"auth_index,omitempty"`
	AuthFile   string   `json:"auth_file,omitempty"`
	Models     []string `json:"models,omitempty"`
	Bindings   int      `json:"bindings"`
	ExpiresAt  string   `json:"expires_at"`
	Available  bool     `json:"available"`
	Status     string   `json:"status"`                // "available" | "unavailable" | "stale_warning"
	StatusNote string   `json:"status_note,omitempty"` // e.g. "auth disabled", "in cooldown until ..."
}

// GetSessionAffinity returns the live snapshot of session→auth bindings held
// by the in-memory affinity cache. The data is sourced from the auth manager's
// active selector (not persisted tables) and is per-process — a restart drops
// every binding. When session affinity is disabled in config the response
// carries affinity_enabled=false and no records so the dashboard renders its
// "affinity disabled" banner. Returns 503 when the auth manager is
// unavailable so the dashboard can detect the absence cleanly (same contract as
// GetCooldownProviders).
func (h *Handler) GetSessionAffinity(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	if !h.authManager.SessionAffinityEnabled() {
		c.JSON(http.StatusOK, gin.H{
			"affinity_enabled": false,
			"records":          []sessionAffinityRow{},
		})
		return
	}

	bindings := h.authManager.SessionAffinitySnapshot()
	rows := buildSessionAffinityRows(bindings, h.authManager)

	log.Debugf("session-affinity: returning %d bindings -> %d rows", len(bindings), len(rows))
	c.JSON(http.StatusOK, gin.H{
		"affinity_enabled": true,
		"records":          rows,
	})
}

// RevokeSessionAffinity manually drops every binding keyed by the given
// session ID so the next request in that session re-selects a healthy
// credential via round-robin. This is the operator escape hatch for sessions
// pinned to a stuck/erroring auth that the lazy failover has not yet migrated
// away. Returns the remaining total binding count so the dashboard can
// reflect the post-revoke state immediately.
func (h *Handler) RevokeSessionAffinity(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if req.SessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	if !h.authManager.SessionAffinityEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session affinity is not enabled"})
		return
	}

	h.authManager.InvalidateSessionAffinityBinding(req.SessionID)
	remaining := len(h.authManager.SessionAffinitySnapshot())
	log.Infof("session-affinity: revoked session %s, %d bindings remaining", truncateForLog(req.SessionID), remaining)
	c.JSON(http.StatusOK, gin.H{
		"revoked":   req.SessionID,
		"remaining": remaining,
	})
}

// buildSessionAffinityRows groups the flat per-binding snapshot into one row
// per session ID. A single conversation may carry several cache entries
// (distinct provider::sessionID::model keys — e.g. a session that switched
// between gemini-2.5-pro and gemini-3-flash). They share the same sessionID
// and normally the same pinned auth; we surface them as one row with a models
// list so the operator revokes the whole conversation in one click.
func buildSessionAffinityRows(bindings []coreauth.SessionAffinityBinding, am *coreauth.Manager) []sessionAffinityRow {
	if len(bindings) == 0 {
		return []sessionAffinityRow{}
	}

	// Group by sessionID, preserving deterministic post-group ordering.
	groups := make(map[string][]coreauth.SessionAffinityBinding, len(bindings))
	for _, b := range bindings {
		groups[b.SessionID] = append(groups[b.SessionID], b)
	}

	rows := make([]sessionAffinityRow, 0, len(groups))
	for sessionID, group := range groups {
		sort.Slice(group, func(i, j int) bool {
			if group[i].Provider != group[j].Provider {
				return group[i].Provider < group[j].Provider
			}
			return group[i].Model < group[j].Model
		})

		row := sessionAffinityRow{
			SessionID: sessionID,
			Bindings:  len(group),
		}

		// Distinct providers + models across the session's bindings.
		seenProvider := map[string]struct{}{}
		seenModel := map[string]struct{}{}
		for _, b := range group {
			if b.Provider != "" {
				if _, ok := seenProvider[b.Provider]; !ok {
					seenProvider[b.Provider] = struct{}{}
				}
			}
			if b.Model != "" {
				if _, ok := seenModel[b.Model]; !ok {
					seenModel[b.Model] = struct{}{}
					row.Models = append(row.Models, b.Model)
				}
			}
		}
		if len(seenProvider) == 1 {
			for p := range seenProvider {
				row.Provider = p
			}
		}

		// latest expiry across the group drives the row countdown.
		latest := group[0].ExpiresAt
		for _, b := range group[1:] {
			if b.ExpiresAt.After(latest) {
				latest = b.ExpiresAt
			}
		}
		row.ExpiresAt = formatSessionAffinityTime(latest)

		// Auth availability: pick the first binding's auth for the index
		// (sessions normally pin to one auth; if multiple, we surface the
		// count via Bindings and still mark the row unavailable if any
		// bound auth is blocked, which is the actionable triage signal).
		var anyAuth *coreauth.Auth
		anyBlocked := false
		var blockNote string
		for _, b := range group {
			auth, ok := am.GetByID(b.AuthID)
			if !ok || auth == nil {
				// Binding points at an auth no longer registered. The sticky
				// selector would lazily reselect on next use, but the binding
				// is stale — surface it so the operator can revoke.
				anyBlocked = true
				blockNote = "bound auth no longer registered"
				continue
			}
			if anyAuth == nil {
				anyAuth = auth
			}
			if blocked, note := authUnavailableForSession(auth, b); blocked {
				anyBlocked = true
				if blockNote == "" {
					blockNote = note
				}
			}
		}

		if anyAuth != nil {
			anyAuth.EnsureIndex()
			row.AuthID = anyAuth.ID
			row.AuthIndex = anyAuth.Index
			row.AuthFile = anyAuth.FileName
		}
		if len(group) > 0 && row.AuthID == "" {
			// Fall back to the literal first binding's auth id if lookup failed.
			row.AuthID = group[0].AuthID
		}

		row.Available = !anyBlocked
		switch {
		case anyBlocked:
			row.Status = "unavailable"
			row.StatusNote = blockNote
		case len(group) > 1:
			row.Status = "available"
			row.StatusNote = ""
		default:
			row.Status = "available"
		}

		rows = append(rows, row)
	}

	// Sort rows so unavailable (actionable) sessions surface first; within a
	// status bucket, earliest-expiring sessions come first so imminent drops
	// are at the top.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Available != rows[j].Available {
			return !rows[i].Available // unavailable (Available=false) first
		}
		return parseSessionAffinityTime(rows[i].ExpiresAt).Before(parseSessionAffinityTime(rows[j].ExpiresAt))
	})
	return rows
}

// authUnavailableForSession reports whether the auth the session is pinned to
// is currently blocked from serving that (provider, model) — the condition
// that causes "requests always error and the session hasn't resolved" and is
// the primary motivation for a manual revoke. It mirrors isAuthBlockedForModel
// semantics without needing to reach the unexported helper.
func authUnavailableForSession(auth *coreauth.Auth, b coreauth.SessionAffinityBinding) (blocked bool, note string) {
	if auth == nil {
		return true, "bound auth no longer registered"
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return true, "auth disabled"
	}
	if auth.Unavailable {
		return true, "auth unavailable"
	}
	if !auth.NextRetryAfter.IsZero() && time.Now().Before(auth.NextRetryAfter) {
		return true, "in cooldown until " + formatSessionAffinityTime(auth.NextRetryAfter)
	}
	// Model-level cooldown: check ModelStates for the bound model.
	if b.Model != "" && len(auth.ModelStates) > 0 {
		if st, ok := auth.ModelStates[b.Model]; ok && st != nil && st.Unavailable {
			n := "model in cooldown"
			if !st.NextRetryAfter.IsZero() && time.Now().Before(st.NextRetryAfter) {
				n = "model in cooldown until " + formatSessionAffinityTime(st.NextRetryAfter)
			}
			return true, n
		}
	}
	return false, ""
}

// formatSessionAffinityTime renders a time.Time as RFC3339, returning "" for
// the zero value so the dashboard renders a clean "—". Local copy to avoid
// coupling to the cooldown handler's helper.
func formatSessionAffinityTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseSessionAffinityTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// truncateForLog shortens a session ID for log messages (first 8 chars + "...")
// to avoid leaking full session identifiers into the log stream.
func truncateForLog(id string) string {
	if len(id) <= 20 {
		return id
	}
	return id[:8] + "..."
}
