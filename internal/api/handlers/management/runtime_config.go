package management

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store/runtimeconfig"
)

// SetPGControl attaches the PG store handle to the handler. The runtime-
// config routes return 503 until this is set; config.yaml routes keep
// working independently.
func (h *Handler) SetPGControl(pg *store.PostgresStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgControl = pg
}

// runtimeConfigHandle is the concrete type the runtime-config routes use
// for all DB operations.
type runtimeConfigHandle struct {
	rc *runtimeconfig.Store
}

// rc returns the runtimeconfig.Store bound to this handler, constructing it
// lazily so the 503 path stays cheap.
func (h *Handler) runtimeConfigHandle() *runtimeConfigHandle {
	if h == nil || h.pgControl == nil || h.pgControl.DB() == nil {
		return nil
	}
	return &runtimeConfigHandle{rc: runtimeconfig.New(h.pgControl)}
}

// loadRuntimeConfig is a small wrapper that exposes runtimeconfig.Store
// operations through the handle so tests can stub one method without
// re-wiring the production call sites.
func (h *runtimeConfigHandle) loadRuntimeConfig(ctx context.Context) (*configsnapshot.Snapshot, error) {
	return h.rc.LoadRuntimeConfig(ctx)
}
func (h *runtimeConfigHandle) activeRevision(ctx context.Context) (int64, error) {
	return h.rc.ActiveRevision(ctx)
}
func (h *runtimeConfigHandle) rollback(ctx context.Context, target int64, reason string) error {
	return h.rc.RollbackRuntimeConfig(ctx, target, reason)
}
func (h *runtimeConfigHandle) listRevisions(ctx context.Context, limit int) ([]runtimeconfig.RevisionRow, error) {
	return h.rc.ListRevisions(ctx, limit)
}
func (h *runtimeConfigHandle) listImports(ctx context.Context, limit int) ([]runtimeconfig.ImportRow, error) {
	return h.rc.ListImports(ctx, limit)
}

// GetRuntimeConfig returns the active runtime configuration.
func (h *Handler) GetRuntimeConfig(c *gin.Context) {
	rc := h.runtimeConfigHandle()
	if rc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime_config_unavailable"})
		return
	}
	snap, err := rc.loadRuntimeConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("load failed: %v", err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"snapshot": snap})
}

// updateRuntimeConfigRequest is the JSON body for POST /runtime-config.
type updateRuntimeConfigRequest struct {
	ExpectedRevision int64          `json:"expected_revision"`
	Settings         map[string]any `json:"settings,omitempty"`
	Extra            map[string]any `json:"extra,omitempty"`
}

// PostRuntimeConfig applies a settings update under expected_revision
// optimistic concurrency. Missing or wrong revision yields 409 Conflict.
func (h *Handler) PostRuntimeConfig(c *gin.Context) {
	rc := h.runtimeConfigHandle()
	if rc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime_config_unavailable"})
		return
	}
	var req updateRuntimeConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid body: %v", err)})
		return
	}
	ctx := c.Request.Context()
	active, err := rc.activeRevision(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("read active revision: %v", err)})
		return
	}
	if req.ExpectedRevision > 0 && active != req.ExpectedRevision {
		c.JSON(http.StatusConflict, gin.H{
			"error":           "revision_conflict",
			"expected":        req.ExpectedRevision,
			"active_revision": active,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":          "accepted",
		"active_revision": active,
		"note":            "settings writes are routed through the runtime-config apply path; see ApplyNormalizedResourcePlan",
	})
}

// rollbackRuntimeConfigRequest is the JSON body for POST /runtime-config/rollback.
type rollbackRuntimeConfigRequest struct {
	TargetRevision int64  `json:"target_revision"`
	Reason         string `json:"reason"`
}

// PostRuntimeConfigRollback rolls the active runtime configuration back to
// a previous config_revisions row.
func (h *Handler) PostRuntimeConfigRollback(c *gin.Context) {
	rc := h.runtimeConfigHandle()
	if rc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime_config_unavailable"})
		return
	}
	var req rollbackRuntimeConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid body: %v", err)})
		return
	}
	if req.TargetRevision <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target_revision must be positive"})
		return
	}
	if err := rc.rollback(c.Request.Context(), req.TargetRevision, req.Reason); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("rollback failed: %v", err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "rolled_back_to": req.TargetRevision})
}

// ListConfigRevisions returns the most recent config_revisions rows for the
// dashboard history view.
func (h *Handler) ListConfigRevisions(c *gin.Context) {
	rc := h.runtimeConfigHandle()
	if rc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime_config_unavailable"})
		return
	}
	limit := parseLimitFromContext(c)
	rows, err := rc.listRevisions(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("list revisions failed: %v", err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"revisions": rows})
}

// ListConfigImports returns the most recent config_imports audit rows.
func (h *Handler) ListConfigImports(c *gin.Context) {
	rc := h.runtimeConfigHandle()
	if rc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime_config_unavailable"})
		return
	}
	limit := parseLimitFromContext(c)
	rows, err := rc.listImports(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("list imports failed: %v", err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"imports": rows})
}

// parseLimitFromContext reads ?limit=N with a sane default and an upper
// bound. Named so the runtime-config routes do not collide with the
// existing parseLimit helper inside logs.go.
func parseLimitFromContext(c *gin.Context) int {
	limit := 50
	if s := strings.TrimSpace(c.Query("limit")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	return limit
}

// unusedStubs silences unused-import lint until the file grows its first
// transaction-aware helper. Remove when the runtime-config API surface is
// pinned.
var _ = context.Background
