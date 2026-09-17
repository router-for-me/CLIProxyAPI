package management

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configstore"
	pgconfigstore "github.com/router-for-me/CLIProxyAPI/v7/internal/configstore/pg"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store/runtimeconfig"
	log "github.com/sirupsen/logrus"
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
	if pg != nil {
		h.configRepo = pgconfigstore.Open(pg)
		// Round-2 quota-share endpoints (Task 7) share the same PostgresStore
		// handle. Wire the adapter so /v0/management/auths/:id/quota and
		// /v0/management/pools/:key/quota flip from 503 to data-bearing
		// whenever PG is configured.
		h.quotaRepoIface = NewStoreQuotaRepoAdapter(pg)
		h.quotaRepoEnabled = true
	} else {
		h.configRepo = nil
		h.quotaRepoIface = nil
		h.quotaRepoEnabled = false
	}
	// Round-2 headroom production wiring (Task 7 closes the loop on Task 5's
	// stub). When the auth manager is already attached, replace its default
	// unlimited-100% headroom lookup with the usage_windows-backed
	// implementation so the headroom selector becomes functional in
	// production. nil PG keeps the static stub in place (preserves the
	// pre-Task-7 behavior on file-only deployments).
	if h.authManager != nil {
		if pg != nil {
			h.authManager.SetHeadroomLookup(newUsageWindowsHeadroomLookupFromStore(pg))
		} else {
			h.authManager.SetHeadroomLookup(nil)
		}
	}
}

// SetConfigRepository attaches a pre-built configstore.Repository. Tests
// pass an in-memory stub; production goes through SetPGControl above.
func (h *Handler) SetConfigRepository(repo configstore.Repository) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.configRepo = repo
}

// ReloadLatestFn re-renders the ephemeral bridge from the latest committed
// runtime_config snapshot. Implemented by runtimebridge.Coordinator.ReloadLatest.
type ReloadLatestFn func(ctx context.Context) error

// SetReloadCoordinator attaches the bridge reload hook. Called from
// cmd/server/main.go when the server boots with an active bridge.
func (h *Handler) SetReloadCoordinator(fn ReloadLatestFn) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reloadCoordinator = fn
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

// triggerReload invokes the bridge reload hook when one is bound. Phase 2
// keeps this best-effort: a reload failure is logged but does not undo the
// committed DB revision (matching the design's "committed but pending
// reload" state).
func (h *Handler) triggerReload(ctx context.Context) {
	h.mu.Lock()
	fn := h.reloadCoordinator
	h.mu.Unlock()
	if fn == nil {
		return
	}
	if err := fn(ctx); err != nil {
		log.WithError(err).Error("runtime-config bridge reload failed; committed revision is pending reload")
	}
}

// updateRuntimeConfigRequest is the JSON body for POST /runtime-config.
type updateRuntimeConfigRequest struct {
	ExpectedRevision int64          `json:"expected_revision"`
	Settings         map[string]any `json:"settings,omitempty"`
	Extra            map[string]any `json:"extra,omitempty"`
}

// snapshotFromInMemoryCfg builds a bootstrap-shaped Snapshot from the
// live in-memory *config.Config. It mirrors configsnapshot.SnapshotFromConfig
// but avoids the round-trip through yaml.Marshal + yaml.Unmarshal that the
// CLI importer uses, because the management handler already holds a
// parsed *config.Config.
//
// ResourceRefs is left empty; resources live in the normalized tables.
// UpdatedSource is the channel constant so reload/diff outputs are
// unambiguous.
func snapshotFromInMemoryCfg(cfg *config.Config) (configsnapshot.Snapshot, error) {
	snap := configsnapshot.NewEmpty()
	snap.UpdatedSource = "dashboard"
	if cfg == nil {
		return snap, nil
	}
	// Phase 3 minimum: repackage every top-level field from cfg through the
	// scalarKeySet partition. We round-trip via SnapshotFromConfig's
	// implementation (yaml marshal + decode + partition) to keep the
	// projection identical to the CLI path.
	return configsnapshot.SnapshotFromConfig(cfg)
}

// PostRuntimeConfig applies a settings update under expected_revision
// optimistic concurrency. The handler is the production replacement of the
// Phase-1 stub: it routes through configstore.Repository.Save and the
// reload coordinator.
func (h *Handler) PostRuntimeConfig(c *gin.Context) {
	if h.pgControl == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "runtime_config_unavailable"})
		return
	}
	var req updateRuntimeConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid body: %v", err)})
		return
	}
	if len(req.Settings) == 0 && len(req.Extra) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "settings or extra is required"})
		return
	}

	ctx := c.Request.Context()
	repo := pgconfigstore.Open(h.pgControl)

	candidate := configsnapshot.NewEmpty()
	candidate.UpdatedSource = "dashboard"
	if req.Settings != nil {
		candidate.Settings = req.Settings
	}
	if req.Extra != nil {
		candidate.Extra = req.Extra
	}

	actor := strings.TrimSpace(c.GetHeader("X-Management-User"))
	if actor == "" {
		actor = "dashboard"
	}
	audit := configstore.SaveAudit{
		Actor:  actor,
		Reason: fmt.Sprintf("dashboard save via %s", c.Request.URL.Path),
		Source: "dashboard",
	}

	saved, err := repo.Save(ctx, req.ExpectedRevision, &candidate, audit)
	if err != nil {
		var conflict *configstore.RevisionConflictError
		if errorsAs(err, &conflict) {
			c.JSON(http.StatusConflict, gin.H{
				"error":           "revision_conflict",
				"expected":        req.ExpectedRevision,
				"active_revision": conflict.Current,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("save failed: %v", err)})
		return
	}

	reloadStatus := h.applyReload(ctx)
	c.JSON(http.StatusOK, gin.H{
		"status":          "ok",
		"snapshot":        saved,
		"reload_status":   reloadStatus,
		"active_revision": saved.Revision,
	})
}

// applyReload fires the bound reload coordinator (when one exists) and
// returns the reload status string the dashboard surfaces.
func (h *Handler) applyReload(ctx context.Context) string {
	h.mu.Lock()
	fn := h.reloadCoordinator
	h.mu.Unlock()
	if fn == nil {
		return "skipped"
	}
	if err := fn(ctx); err != nil {
		log.WithError(err).Warn("runtime-config bridge reload failed; committed revision is pending reload")
		return "pending"
	}
	return "applied"
}

// errorsAs is a tiny wrapper around errors.As so this file does not need
// to import the errors package directly for a single call site.
func errorsAs(err error, target interface{}) bool {
	if err == nil {
		return false
	}
	if t, ok := target.(**configstore.RevisionConflictError); ok {
		if c, ok := err.(*configstore.RevisionConflictError); ok {
			*t = c
			return true
		}
	}
	return false
}

// snapshotFromInMemoryCfg is currently unused; kept as the documented path
// for the management save helper so the import stays stable. Remove when
// the helper is wired into handler.go's persistLocked.
var _ = snapshotFromInMemoryCfg
var _ = log.WithError

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
	reloadStatus := h.applyReload(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{
		"status":         "ok",
		"rolled_back_to": req.TargetRevision,
		"reload_status":  reloadStatus,
	})
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
