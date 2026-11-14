package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// inFlightResponse is the read-only payload for the standalone in-flight
// request count endpoint. It intentionally exposes counts only: no request
// bodies, prompts, client addresses, or credential identifiers.
type inFlightResponse struct {
	InFlight        int64            `json:"in_flight"`
	OldestStartedAt *time.Time       `json:"oldest_started_at,omitempty"`
	ByProvider      map[string]int64 `json:"by_provider"`
}

// GetInFlightRequests reports the current in-flight request count on a
// standalone instance. In Home mode the snapshot stays empty because the
// execution registry is the authoritative in-flight source there.
func (h *Handler) GetInFlightRequests(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	resp := inFlightResponse{ByProvider: map[string]int64{}}
	if h.authManager != nil {
		snapshot := h.authManager.InFlightSnapshot()
		resp.InFlight = snapshot.InFlight
		if !snapshot.OldestStartedAt.IsZero() {
			oldest := snapshot.OldestStartedAt.UTC()
			resp.OldestStartedAt = &oldest
		}
		if len(snapshot.ByProvider) > 0 {
			resp.ByProvider = snapshot.ByProvider
		}
	}
	c.JSON(http.StatusOK, resp)
}
