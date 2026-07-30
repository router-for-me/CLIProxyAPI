package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// PublicModelHealthModel is the curated per-model shape exposed by the
// unauthenticated uptime endpoint. It deliberately omits internal identifiers
// (auth IDs, raw error text, provider-key topology) so a public status page
// can show per-model health without leaking routing internals — mirroring how
// root_branding.go exposes only curated operator strings publicly.
type PublicModelHealthModel struct {
	ModelID         string   `json:"model_id"`
	Status          string   `json:"status"`
	TokensPerSecond *float64 `json:"tokens_per_second,omitempty"`
	ResponseTimeMs  int64    `json:"response_time_ms"`
	CheckedAt       string   `json:"checked_at,omitempty"`
}

// rollupModelHealthStatus computes the top-level service status from the per-
// model snapshot rows: unavailable if any model is unavailable, degraded if
// any is degraded, otherwise operational. An empty model set returns "unknown"
// so a fresh deployment (no probes yet) is distinguishable from "all healthy".
func rollupModelHealthStatus(rows []store.ModelHealthRow) string {
	if len(rows) == 0 {
		return store.ModelHealthStatusUnknown
	}
	hasDegraded, hasUnavailable := false, false
	for _, r := range rows {
		switch r.Status {
		case store.ModelHealthStatusUnavailable:
			hasUnavailable = true
		case store.ModelHealthStatusDegraded:
			hasDegraded = true
		}
	}
	if hasUnavailable {
		return store.ModelHealthStatusUnavailable
	}
	if hasDegraded {
		return store.ModelHealthStatusDegraded
	}
	return store.ModelHealthStatusOperational
}

// GetPublicModelHealthUptime handles GET /v0/model-health/uptime.
//
// This is an UNAUTHENTICATED public endpoint (mounted directly on s.engine in
// server_routes.go, like /healthz) so an operator can publish a status page
// or feed an external uptime monitor without a management token. It exposes a
// curated per-model view (status, TPS, response time, last check time) with a
// top-level rollup status. Internal identifiers (auth IDs, provider topology,
// raw error text) are intentionally omitted.
//
// When the PG backend is not configured, the endpoint degrades gracefully to a
// 200 with status "unknown" and an empty model list rather than 503ing, so a
// public uptime monitor never sees the proxy as down just because health
// checks are disabled.
func (h *Handler) GetPublicModelHealthUptime(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h == nil {
		c.JSON(http.StatusOK, gin.H{
			"status":       store.ModelHealthStatusUnknown,
			"generated_at": time.Now().UTC().Format(time.RFC3339),
			"models":       []PublicModelHealthModel{},
		})
		return
	}
	h.mu.Lock()
	s := h.pgModelHealth
	h.mu.Unlock()
	if s == nil {
		c.JSON(http.StatusOK, gin.H{
			"status":       store.ModelHealthStatusUnknown,
			"generated_at": time.Now().UTC().Format(time.RFC3339),
			"models":       []PublicModelHealthModel{},
		})
		return
	}
	rows, err := s.ListSnapshots(c.Request.Context())
	if err != nil {
		// Log but do not 500 on the public surface; degrade to unknown so an
		// external monitor treats a transient DB hiccup as "unknown health"
		// rather than "the proxy is down".
		log.WithError(err).Warn("model-health/uptime: list snapshots failed")
		c.JSON(http.StatusOK, gin.H{
			"status":       store.ModelHealthStatusUnknown,
			"generated_at": time.Now().UTC().Format(time.RFC3339),
			"models":       []PublicModelHealthModel{},
		})
		return
	}
	// Hide operator-excluded models from the public status too, mirroring the
	// authenticated latest-status endpoint. Excluded models are not probed, so
	// their last snapshot grows stale; surfacing a stale "operational" on a
	// public status page would misrepresent actual health. GetSettings is
	// non-fatal: on error an empty exclusion set makes the filter a no-op (the
	// same behavior the endpoint had before exclusion was read-aware), which is
	// preferable to failing a public uptime monitor.
	settings, errSet := s.GetSettings(c.Request.Context())
	if errSet != nil {
		log.WithError(errSet).Warn("model-health/uptime: load settings failed; showing all snapshot models")
		settings = store.ModelHealthSettings{Enabled: true, ExcludedModels: []string{}, MaxTokens: 1, IntervalSeconds: 900}
	}
	rows = store.FilterExcludedSnapshots(rows, settings.ExcludedModels)
	models := make([]PublicModelHealthModel, 0, len(rows))
	for _, r := range rows {
		checkedAt := ""
		if !r.CheckedAt.IsZero() {
			checkedAt = r.CheckedAt.UTC().Format(time.RFC3339)
		}
		models = append(models, PublicModelHealthModel{
			ModelID:         r.ModelID,
			Status:          r.Status,
			TokensPerSecond: r.TokensPerSecond,
			ResponseTimeMs:  r.ResponseTimeMs,
			CheckedAt:       checkedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"status":       rollupModelHealthStatus(rows),
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"models":       models,
	})
}
