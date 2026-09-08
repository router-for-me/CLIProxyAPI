package management

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// simulateEventCap bounds how many stored decision events a simulation replays
// in-process; anything beyond the cap is flagged truncated.
const simulateEventCap = 10000

// simulateRequest is the body of POST /auto-routers/:id/profile/simulate.
type simulateRequest struct {
	Config   autorouter.ProfileConfig `json:"config"`
	From     string                   `json:"from"`
	To       string                   `json:"to"`
	APIKeyID string                   `json:"api_key_id"`
}

// simulateResponse reports how the candidate profile would reclassify stored
// decisions, with the explicit unsimulable/truncated/unsampled caveats.
type simulateResponse struct {
	RouterID            string                     `json:"router_id"`
	CandidateHash       string                     `json:"candidate_hash"`
	Events              int64                      `json:"events"`
	TotalSnapshotEvents int64                      `json:"total_snapshot_events"`
	Truncated           bool                       `json:"truncated"`
	Moves               []autorouter.TierMove      `json:"moves"`
	Confusion           []autorouter.ConfusionCell `json:"confusion"`
	MovedCount          int64                      `json:"moved_count"`
	UnsimulableRules    []string                   `json:"unsimulable_rules"`
	UnsimulableCount    int                        `json:"unsimulable_count"`
	From                time.Time                  `json:"from"`
	To                  time.Time                  `json:"to"`
}

// SimulateAutoRouterProfile handles POST /v0/management/auto-routers/:id/profile/simulate.
// It never writes: the candidate profile is validated and normalized, stored
// decision snapshots in the window are recomputed under it, and the outcome is
// reported. Applying the candidate remains a separate explicit PUT to the
// profile endpoint.
func (h *Handler) SimulateAutoRouterProfile(c *gin.Context) {
	routers, ok := h.requireAutoRouters(c)
	if !ok {
		return
	}
	routerID := strings.TrimSpace(c.Param("id"))
	if _, err := routers.Get(c.Request.Context(), routerID); err != nil {
		h.translateAutoRouterError(c, err)
		return
	}
	usages, ok := h.requireUsageStore(c)
	if !ok {
		return
	}
	var req simulateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	// Validate + normalize the candidate with the same rules as the upsert
	// path so "simulate ok → apply ok" is guaranteed.
	normalized, err := autorouter.NormalizeProfile(req.Config)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	hash, err := autorouter.ProfileHash(normalized)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	from, to, ok := parseSimulateWindow(req.From, req.To)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "time range exceeds 90 days"}})
		return
	}
	filter := store.UsageFilter{RouterID: routerID, APIKeyID: strings.TrimSpace(req.APIKeyID), From: from, To: to}
	events, total, err := usages.SelectAutoRouterDecisionEvents(c.Request.Context(), routerID, filter, simulateEventCap)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	stored := make([]autorouter.StoredDecision, 0, len(events))
	for _, ev := range events {
		sd, perr := storedDecisionFromSnapshot(ev)
		if perr != nil {
			// A malformed snapshot must not fail the whole simulation; skip it.
			continue
		}
		stored = append(stored, sd)
	}
	result := autorouter.SimulateProfile(stored, normalized, nil)
	c.JSON(http.StatusOK, simulateResponse{
		RouterID:            routerID,
		CandidateHash:       hash,
		Events:              result.Events,
		TotalSnapshotEvents: total,
		Truncated:           total > int64(len(events)),
		Moves:               result.Moves,
		Confusion:           result.Confusion,
		MovedCount:          result.MovedCount(),
		UnsimulableRules:    result.UnsimulableRules,
		UnsimulableCount:    result.UnsimulableCount,
		From:                from,
		To:                  to,
	})
}

// parseSimulateWindow parses the RFC3339 bounds, defaulting to the last 7
// days, and enforces the 90-day cap shared with the stats endpoints.
func parseSimulateWindow(fromStr, toStr string) (time.Time, time.Time, bool) {
	var from, to time.Time
	if fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			from = t.UTC()
		}
	}
	if toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			to = t.UTC()
		}
	}
	if from.IsZero() && to.IsZero() {
		to = time.Now().UTC()
		from = to.Add(-7 * 24 * time.Hour)
	}
	window := to.Sub(from)
	if window < 0 || window > maxAutoRouterStatsWindow {
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

// storedDecisionFromSnapshot reduces one raw event row into the simulation
// input, decoding the persisted DecisionSnapshot jsonb.
func storedDecisionFromSnapshot(ev store.AutoRouterDecisionEvent) (autorouter.StoredDecision, error) {
	var snap autorouter.DecisionSnapshot
	if err := json.Unmarshal(ev.AutoRouterDecision, &snap); err != nil {
		return autorouter.StoredDecision{}, err
	}
	return autorouter.StoredDecision{
		RequestID:        ev.RequestID,
		ScoreFields:      snap.ScoreFields,
		ScoreTotal:       snap.ScoreTotal,
		ReasoningMarkers: snap.ReasoningMarkers,
		ScoredTier:       snap.ScoredTier,
		EffectiveTier:    snap.EffectiveTier,
		MappingTier:      snap.MappingTier,
		DecisionCause:    snap.DecisionCause,
		MatchedRules:     snap.MatchedRules,
	}, nil
}

// GetAutoRouterDecision handles GET /v0/management/auto-routers/:id/decisions/:request_id:
// the full decision snapshot of one request joined with its event metrics.
func (h *Handler) GetAutoRouterDecision(c *gin.Context) {
	routers, ok := h.requireAutoRouters(c)
	if !ok {
		return
	}
	routerID := strings.TrimSpace(c.Param("id"))
	requestID := strings.TrimSpace(c.Param("request_id"))
	if requestID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "request_id is required"}})
		return
	}
	if _, err := routers.Get(c.Request.Context(), routerID); err != nil {
		h.translateAutoRouterError(c, err)
		return
	}
	usages, ok := h.requireUsageStore(c)
	if !ok {
		return
	}
	row, err := usages.GetAutoRouterDecisionByRequest(c.Request.Context(), routerID, requestID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	if row == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "no decision event for this request_id"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"router_id":  routerID,
		"request_id": row.RequestID,
		"decision":   row.AutoRouterDecision,
		"event": gin.H{
			"requested_at":     row.RequestedAt,
			"model":            row.Model,
			"alias":            row.Alias,
			"scored_tier":      row.ScoredTier,
			"effective_tier":   row.EffectiveTier,
			"mapping_tier":     row.MappingTier,
			"decision_cause":   row.DecisionCause,
			"profile_version":  row.ProfileVersion,
			"profile_hash":     row.ProfileHash,
			"input_tokens":     row.InputTokens,
			"output_tokens":    row.OutputTokens,
			"total_tokens":     row.TotalTokens,
			"cost_usd":         row.CostUSD,
			"latency_ms":       row.LatencyMs,
			"ttft_ms":          row.TTFTMs,
			"failed":           row.Failed,
			"fail_status_code": row.FailStatusCode,
		},
	})
}
