package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// cooldownProviderRow is the dashboard-facing projection of one
// CooldownStateRecord, augmented with the auth's runtime Index (the stable
// identifier the dashboard uses to call POST /v0/management/reset-quota).
type cooldownProviderRow struct {
	Provider       string              `json:"provider"`
	AuthID         string              `json:"auth_id"`
	AuthIndex      string              `json:"auth_index,omitempty"`
	AuthFile       string              `json:"auth_file,omitempty"`
	Model          string              `json:"model,omitempty"`
	Level          string              `json:"level"` // "auth" when Model empty, "model" otherwise
	Status         string              `json:"status"`
	NextRetryAfter string              `json:"next_retry_after"`
	Reason         string              `json:"reason,omitempty"`
	Quota          coreauth.QuotaState `json:"quota,omitempty"`
	LastError      *coreauth.Error     `json:"last_error,omitempty"`
	UpdatedAt      string              `json:"updated_at"`
}

// GetCooldownProviders returns the live snapshot of every upstream auth/model
// pair currently in cooldown. The data is sourced from the in-memory auth
// manager (not persisted tables) and is read-only; reset is served by the
// existing POST /v0/management/reset-quota route. Returns 503 when the auth
// manager is unavailable so the dashboard can detect the absence cleanly.
func (h *Handler) GetCooldownProviders(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	records := h.authManager.CooldownStateSnapshot()
	rows := make([]cooldownProviderRow, 0, len(records))
	for _, r := range records {
		level := "model"
		if r.Model == "" {
			level = "auth"
		}
		status := r.Status
		if status == "" {
			status = "cooling"
		}
		row := cooldownProviderRow{
			Provider:       r.Provider,
			AuthID:         r.AuthID,
			AuthFile:       r.AuthFile,
			Model:          r.Model,
			Level:          level,
			Status:         status,
			NextRetryAfter: formatCooldownTime(r.NextRetryAfter),
			Reason:         r.Reason,
			Quota:          r.Quota,
			LastError:      r.LastError,
			UpdatedAt:      formatCooldownTime(r.UpdatedAt),
		}
		// Attach the runtime Index (e.g. "gemini#0") so the dashboard can
		// call reset-quota without reconstructing it client-side. Resolve
		// once; if missing (auth racing removal) leave it empty.
		if auth, ok := h.authManager.GetByID(r.AuthID); ok && auth != nil {
			auth.EnsureIndex()
			row.AuthIndex = auth.Index
		}
		rows = append(rows, row)
	}

	log.Debugf("cooldown-providers: returning %d rows", len(rows))
	c.JSON(http.StatusOK, gin.H{"records": rows})
}

// formatCooldownTime renders a time.Time as RFC3339, returning "" for the
// zero value so the dashboard can render a clean "—" instead of "1970…".
func formatCooldownTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
