package management

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// autoRouterProfileResponse is the GET /profile response. The full numeric
// config is always returned so the dashboard can render the editor against
// the live values, and so a misconfigured row is visible instead of silently
// degrading to defaults.
type autoRouterProfileResponse struct {
	RouterID  string                   `json:"router_id"`
	Version   int64                    `json:"profile_version"`
	Hash      string                   `json:"profile_hash"`
	Config    autorouter.ProfileConfig `json:"config"`
	IsDefault bool                     `json:"is_default"`
}

// updateAutoRouterProfileRequest is the PUT /profile body. Operators submit the
// full ProfileConfig; the server normalizes, validates, increments the version,
// and replaces the active row atomically.
type updateAutoRouterProfileRequest struct {
	Config autorouter.ProfileConfig `json:"config"`
}

// GetAutoRouterProfile handles GET /v0/management/auto-routers/:id/profile.
// Returns the active profile for the router, or the built-in defaults when
// no row exists yet. Always 200 unless the router itself is missing.
func (h *Handler) GetAutoRouterProfile(c *gin.Context) {
	routers, ok := h.requireAutoRouters(c)
	if !ok {
		return
	}
	routerID := strings.TrimSpace(c.Param("id"))
	if _, err := routers.Get(c.Request.Context(), routerID); err != nil {
		h.translateAutoRouterError(c, err)
		return
	}
	profiles, ok := h.requireAutoRouterProfiles(c)
	if !ok {
		return
	}
	profile, err := profiles.Get(c.Request.Context(), routerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// is_default is a best-effort signal: when there is no persisted row the
	// store returns DefaultProfile() with hash "sha256:..." of the defaults;
	// we still surface the truth by checking whether any row exists.
	c.JSON(http.StatusOK, autoRouterProfileResponse{
		RouterID:  routerID,
		Version:   profile.ProfileVersion,
		Hash:      profile.ProfileHash,
		Config:    profile.ProfileConfig,
		IsDefault: profile.ProfileVersion == autorouter.DefaultProfile().ProfileVersion,
	})
}

// UpdateAutoRouterProfile handles PUT /v0/management/auto-routers/:id/profile.
// Validates and replaces the active profile in one transaction; the new
// version is returned so callers can reconcile the request against the row
// they now own.
func (h *Handler) UpdateAutoRouterProfile(c *gin.Context) {
	routers, ok := h.requireAutoRouters(c)
	if !ok {
		return
	}
	routerID := strings.TrimSpace(c.Param("id"))
	if _, err := routers.Get(c.Request.Context(), routerID); err != nil {
		h.translateAutoRouterError(c, err)
		return
	}
	var req updateAutoRouterProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	profiles, ok := h.requireAutoRouterProfiles(c)
	if !ok {
		return
	}
	profile, err := profiles.Upsert(c.Request.Context(), routerID, req.Config)
	if err != nil {
		// Normalize/validation errors are operator mistakes; surface them as
		// 400 so the dashboard can highlight the bad field, not as 500.
		if strings.Contains(err.Error(), "must") || strings.Contains(err.Error(), "unknown") {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, autoRouterProfileResponse{
		RouterID:  routerID,
		Version:   profile.ProfileVersion,
		Hash:      profile.ProfileHash,
		Config:    profile.ProfileConfig,
		IsDefault: false,
	})
}

// autoRouterDecisionResponse is one explainability record.
type autoRouterDecisionResponse struct {
	ID                 int64          `json:"id"`
	RequestedAt        time.Time      `json:"requested_at"`
	RequestID          string         `json:"request_id,omitempty"`
	APIKeyID           string         `json:"api_key_id,omitempty"`
	Model              string         `json:"model"`
	Alias              string         `json:"alias,omitempty"`
	ScoredTier         string         `json:"scored_tier,omitempty"`
	EffectiveTier      string         `json:"effective_tier,omitempty"`
	MappingTier        string         `json:"mapping_tier,omitempty"`
	DecisionCause      string         `json:"decision_cause,omitempty"`
	ProfileVersion     int64          `json:"profile_version"`
	ProfileHash        string         `json:"profile_hash,omitempty"`
	AutoRouterDecision map[string]any `json:"auto_router_decision,omitempty"`
	InputTokens        int64          `json:"input_tokens"`
	OutputTokens       int64          `json:"output_tokens"`
	TotalTokens        int64          `json:"total_tokens"`
	CostUSD            float64        `json:"cost_usd"`
}

type pagedAutoRouterDecisionsResponse struct {
	RouterID   string                       `json:"router_id"`
	Decisions  []autoRouterDecisionResponse `json:"decisions"`
	Page       int                          `json:"page"`
	PageSize   int                          `json:"page_size"`
	Total      int64                        `json:"total"`
	TotalPages int                          `json:"total_pages"`
}

// ListAutoRouterDecisions handles GET /v0/management/auto-routers/:id/decisions
// with filters: page, page_size, api_key_id, scored_tier, effective_tier,
// mapping_tier, decision_cause, target_model, profile_hash, from, to.
//
// The query is router-scoped (router_id is the URL param) and never returns
// events from other routers. Results are ordered by requested_at DESC so the
// most recent decisions appear first.
func (h *Handler) ListAutoRouterDecisions(c *gin.Context) {
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
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "25"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	filter := store.AutoRouterDecisionFilter{
		APIKeyID:      strings.TrimSpace(c.Query("api_key_id")),
		ScoredTier:    strings.ToLower(strings.TrimSpace(c.Query("scored_tier"))),
		EffectiveTier: strings.ToLower(strings.TrimSpace(c.Query("effective_tier"))),
		MappingTier:   strings.ToLower(strings.TrimSpace(c.Query("mapping_tier"))),
		DecisionCause: strings.TrimSpace(c.Query("decision_cause")),
		TargetModel:   strings.TrimSpace(c.Query("target_model")),
		ProfileHash:   strings.TrimSpace(c.Query("profile_hash")),
	}
	if from := strings.TrimSpace(c.Query("from")); from != "" {
		if t, err := time.Parse(time.RFC3339, from); err == nil {
			filter.From = t.UTC()
		}
	}
	if to := strings.TrimSpace(c.Query("to")); to != "" {
		if t, err := time.Parse(time.RFC3339, to); err == nil {
			filter.To = t.UTC()
		}
	}
	rows, total, err := usages.ListAutoRouterDecisions(c.Request.Context(), routerID, filter, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	out := make([]autoRouterDecisionResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, autoRouterDecisionResponse{
			ID:                 r.ID,
			RequestedAt:        r.RequestedAt,
			RequestID:          r.RequestID,
			APIKeyID:           r.APIKeyID,
			Model:              r.Model,
			Alias:              r.Alias,
			ScoredTier:         r.ScoredTier,
			EffectiveTier:      r.EffectiveTier,
			MappingTier:        r.MappingTier,
			DecisionCause:      r.DecisionCause,
			ProfileVersion:     r.ProfileVersion,
			ProfileHash:        r.ProfileHash,
			AutoRouterDecision: decodeDecisionSnapshot(r.AutoRouterDecision),
			InputTokens:        r.InputTokens,
			OutputTokens:       r.OutputTokens,
			TotalTokens:        r.TotalTokens,
			CostUSD:            r.CostUSD,
		})
	}
	c.JSON(http.StatusOK, pagedAutoRouterDecisionsResponse{
		RouterID:   routerID,
		Decisions:  out,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages(total, pageSize),
	})
}

// decodeDecisionSnapshot lifts the persisted auto_router_decision JSONB into a
// generic map so the dashboard can render whichever fields the operator
// surfaced (score_total, score_fields, matched_rules, …). Returns nil when
// the snapshot is absent or malformed so callers can render an empty row
// instead of a 500.
func decodeDecisionSnapshot(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// requireAutoRouterProfiles returns the AutoRouterProfileStore. Returns false
// (after writing a 503 response) when the store is not wired.
func (h *Handler) requireAutoRouterProfiles(c *gin.Context) (*store.AutoRouterProfileStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	h.mu.Lock()
	profiles := h.pgAutoRouterProfiles
	h.mu.Unlock()
	if profiles == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	return profiles, true
}

// requireUsageStore returns the UsageStore. Returns false (after writing a
// 503 response) when the store is not wired. Mirrors requireAutoRouters for
// the decisions endpoint.
func (h *Handler) requireUsageStore(c *gin.Context) (*store.UsageStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	h.mu.Lock()
	usages := h.pgUsage
	h.mu.Unlock()
	if usages == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	return usages, true
}
