package management

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// quotaQueryTimeout caps the read-side quota aggregation queries so a slow
// database never blocks the dashboard refresh. The round-2 design calls for
// 503-on-failure; we relax that to 200 + partial=true on timeout so a single
// slow PG can never black-out the entire quota panel.
const quotaQueryTimeout = 2 * time.Second

// quotaRepo is the read-side surface Task 7 needs from the PG repository.
// The real implementation lives on store.PostgresStore; this interface
// lets tests inject a fake and keeps the handler dependency surface tight.
type quotaRepo interface {
	// GetAuthQuota returns the per-window quota snapshot for one auth.
	// partial=true means the query hit the 2s timeout and returned a
	// best-effort partial view; the handler then surfaces 200 + partial=true.
	GetAuthQuota(ctx context.Context, authID string) (QuotaAuthSnapshot, bool, error)
	// GetPoolQuota sums across the auths whose provider_key is in the pool
	// identified by the (channel, rowID) compound key.
	GetPoolQuota(ctx context.Context, key string) (QuotaPoolSnapshot, bool, error)
}

// WindowQuota is one row of the per-window aggregation. HeadroomPct is
// computed by the repo (or computeHeadroom in tests) and clamped to [0,100].
// OverLimit signals used > limit so callers can flag the auth even when
// the headroom_pct has already been clamped to 0.
type WindowQuota struct {
	Size        string  `json:"size"`
	Used        int64   `json:"used"`
	Limit       int64   `json:"limit"`
	HeadroomPct float64 `json:"headroom_pct"`
	OverLimit   bool    `json:"over_limit"`
}

// ModelQuota is the per-model 1h rollup. The task-7 endpoint surfaces the
// 1h window specifically because that is the budget cadence most providers
// quote to operators; additional windows can be added later without a
// schema change (just append columns here and aggregate from usage_events).
type ModelQuota struct {
	Model   string `json:"model"`
	Used1h  int64  `json:"used_1h"`
	Limit1h int64  `json:"limit_1h"`
}

// QuotaAuthSnapshot is the per-auth view returned by GetAuthQuota.
type QuotaAuthSnapshot struct {
	AuthID       string
	Channel      string
	PoolStrategy string
	Windows      []WindowQuota
	Models       []ModelQuota
}

// PoolAuthBrief is the per-auth summary inside a pool response. Just enough
// to drive the dashboard drill-down without re-fetching.
type PoolAuthBrief struct {
	AuthID       string `json:"auth_id"`
	Channel      string `json:"channel"`
	PoolStrategy string `json:"pool_strategy"`
}

// QuotaPoolSnapshot is the per-pool view returned by GetPoolQuota. Sums
// across the pool's auths for windows/models; auths[] is the membership
// summary (one row per contributing auth) so the dashboard can render the
// drill-down table.
type QuotaPoolSnapshot struct {
	PoolKey string
	Windows []WindowQuota
	Models  []ModelQuota
	Auths   []PoolAuthBrief
}

// quotaAuthResponse is the JSON shape returned by GET /v0/management/auths/:id/quota.
// The window/model slices are always non-nil so clients can iterate without
// nil-checks; empty tables surface as [].
type quotaAuthResponse struct {
	AuthID       string        `json:"auth_id"`
	Channel      string        `json:"channel"`
	PoolStrategy string        `json:"pool_strategy"`
	Windows      []WindowQuota `json:"windows"`
	Models       []ModelQuota  `json:"models"`
	Partial      bool          `json:"partial,omitempty"`
}

// quotaPoolResponse is the JSON shape returned by GET /v0/management/pools/:key/quota.
// Mirrors quotaAuthResponse plus the per-auth brief list.
type quotaPoolResponse struct {
	PoolKey string          `json:"pool_key"`
	Windows []WindowQuota   `json:"windows"`
	Models  []ModelQuota    `json:"models"`
	Auths   []PoolAuthBrief `json:"auths"`
	Partial bool            `json:"partial,omitempty"`
}

// GetAuthQuota returns the per-window quota snapshot for one auth. The
// endpoint returns 503 when PG is not configured (mirroring the other
// management routes), 500 on a real DB error, and 200 + partial=true on a
// 2s query timeout so a slow PG never black-outs the dashboard.
func (h *Handler) GetAuthQuota(c *gin.Context) {
	if !h.pgEnabledForQuota() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PG storage not enabled"})
		return
	}
	repo := h.quotaRepo()
	if repo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PG storage not enabled"})
		return
	}
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth id is required"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), quotaQueryTimeout)
	defer cancel()

	snap, partial, err := repo.GetAuthQuota(ctx, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if snap.Windows == nil {
		snap.Windows = []WindowQuota{}
	}
	if snap.Models == nil {
		snap.Models = []ModelQuota{}
	}
	if snap.AuthID == "" {
		snap.AuthID = id
	}
	c.JSON(http.StatusOK, quotaAuthResponse{
		AuthID:       snap.AuthID,
		Channel:      snap.Channel,
		PoolStrategy: snap.PoolStrategy,
		Windows:      snap.Windows,
		Models:       snap.Models,
		Partial:      partial,
	})
}

// GetPoolQuota returns the per-window quota snapshot summed across the
// pool identified by the (channel, rowID) compound key (format
// "channel:rowID" or "channel" for legacy bare-channel pools). The handler
// itself only fans out the request; the pool membership + sum lives on the
// repo (see GetPoolQuota in store.PostgresStore).
func (h *Handler) GetPoolQuota(c *gin.Context) {
	if !h.pgEnabledForQuota() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PG storage not enabled"})
		return
	}
	repo := h.quotaRepo()
	if repo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PG storage not enabled"})
		return
	}
	key := c.Param("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pool key is required"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), quotaQueryTimeout)
	defer cancel()

	snap, partial, err := repo.GetPoolQuota(ctx, key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if snap.Windows == nil {
		snap.Windows = []WindowQuota{}
	}
	if snap.Models == nil {
		snap.Models = []ModelQuota{}
	}
	if snap.Auths == nil {
		snap.Auths = []PoolAuthBrief{}
	}
	if snap.PoolKey == "" {
		snap.PoolKey = key
	}
	c.JSON(http.StatusOK, quotaPoolResponse{
		PoolKey: snap.PoolKey,
		Windows: snap.Windows,
		Models:  snap.Models,
		Auths:   snap.Auths,
		Partial: partial,
	})
}

// computeHeadroom turns (used, limit) into (headroom_pct, over_limit) per
// the round-2 design doc's semantics:
//   - limit <= 0: no quota configured → 100.0, over_limit=false
//   - limit > 0: pct = (limit - used) / limit * 100, rounded to 1 decimal
//   - over_limit (used > limit) is surfaced as 0.0 with over_limit=true;
//     negative percentages clamp to 0.0 so a client never renders a
//     "negative headroom" bar.
//
// OverLimit is also surfaced when used > limit even if the math happens to
// come out to exactly 0 (i.e. used == limit is NOT over_limit, but used
// strictly greater than limit IS over_limit).
func computeHeadroom(used, limit int64) (float64, bool) {
	if limit <= 0 {
		return 100.0, false
	}
	pct := float64(limit-used) / float64(limit) * 100.0
	if pct < 0 {
		return 0.0, true
	}
	return math.Round(pct*10) / 10, used > limit
}

// pgEnabledForQuota gates the quota-share endpoints on the PG backend. The
// existing pgControl/pgUsage fields drive this check; we read them under
// h.mu so the gate stays consistent with requirePG. The dedicated helper
// exists (instead of reusing requirePG) because the management store wiring
// for the quota repo is plumbed separately — see SetQuotaRepo.
func (h *Handler) pgEnabledForQuota() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.quotaRepoEnabled
}

// quotaRepo returns the wired quota-share repo (the production
// store.PostgresStore, or a fake in tests). Nil when PG is not enabled.
func (h *Handler) quotaRepo() quotaRepo {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.quotaRepoIface
}

// SetQuotaRepo wires the production quota-share repo onto the handler. The
// store.PostgresStore satisfies quotaRepo via its (ctx, authID) and
// (ctx, key) aggregate helpers (see pg_quota_share.go). Pass nil to disable
// the endpoints; they will return 503.
func (h *Handler) SetQuotaRepo(repo quotaRepo) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.quotaRepoIface = repo
	h.quotaRepoEnabled = repo != nil
}

// setQuotaRepo is the test-only counterpart of SetQuotaRepo. It keeps the
// production wiring path stable while letting tests inject a fake without
// standing up a real PG instance.
func (h *Handler) setQuotaRepo(repo quotaRepo) {
	h.SetQuotaRepo(repo)
}

// setPGEnabledForTest toggles the quota-share gate without standing up the
// rest of the PG wiring (pgControl/pgUsage). Production never calls this;
// it exists only for the quota-share unit tests.
func (h *Handler) setPGEnabledForTest(enabled bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.quotaRepoEnabled = enabled
	// When flipping the gate on without a repo, keep the iface nil so the
	// handler still returns 503 via the nil-repo guard. The test wires a fake
	// via setQuotaRepo separately.
}

// storeQuotaRepoAdapter wraps *store.PostgresStore so it satisfies the
// package-local quotaRepo interface. The adapter is the production-side
// counterpart of fakeQuotaRepo in the unit tests; both must present the
// same (QuotaAuthSnapshot, partial, error) shape to the handler.
type storeQuotaRepoAdapter struct {
	store *store.PostgresStore
}

// NewStoreQuotaRepoAdapter returns a quotaRepo implementation backed by the
// supplied *PostgresStore. The adapter is the bridge between the store
// layer's QuotaShare* types and the management-handler DTOs.
func NewStoreQuotaRepoAdapter(pg *store.PostgresStore) quotaRepo {
	if pg == nil {
		return nil
	}
	return &storeQuotaRepoAdapter{store: pg}
}

// GetAuthQuota satisfies quotaRepo by translating the store-layer
// QuotaShare* types into the management DTOs. Errors propagate as-is so
// the handler can decide between 500 and 200+partial=true.
func (a *storeQuotaRepoAdapter) GetAuthQuota(ctx context.Context, authID string) (QuotaAuthSnapshot, bool, error) {
	snap, partial, err := a.store.GetAuthQuota(ctx, authID)
	if err != nil {
		return QuotaAuthSnapshot{}, partial, err
	}
	out := QuotaAuthSnapshot{
		AuthID:       snap.AuthID,
		Channel:      snap.Channel,
		PoolStrategy: snap.PoolStrategy,
	}
	for _, w := range snap.Windows {
		out.Windows = append(out.Windows, WindowQuota{
			Size:        w.Size,
			Used:        w.Used,
			Limit:       w.Limit,
			HeadroomPct: w.HeadroomPct,
			OverLimit:   w.OverLimit,
		})
	}
	for _, m := range snap.Models {
		out.Models = append(out.Models, ModelQuota{
			Model:   m.Model,
			Used1h:  m.Used1h,
			Limit1h: m.Limit1h,
		})
	}
	return out, partial, nil
}

// GetPoolQuota satisfies quotaRepo for pool aggregation. Mirrors
// GetAuthQuota's translation pattern.
func (a *storeQuotaRepoAdapter) GetPoolQuota(ctx context.Context, key string) (QuotaPoolSnapshot, bool, error) {
	snap, partial, err := a.store.GetPoolQuota(ctx, key)
	if err != nil {
		return QuotaPoolSnapshot{}, partial, err
	}
	out := QuotaPoolSnapshot{PoolKey: snap.PoolKey}
	for _, w := range snap.Windows {
		out.Windows = append(out.Windows, WindowQuota{
			Size:        w.Size,
			Used:        w.Used,
			Limit:       w.Limit,
			HeadroomPct: w.HeadroomPct,
			OverLimit:   w.OverLimit,
		})
	}
	for _, m := range snap.Models {
		out.Models = append(out.Models, ModelQuota{
			Model:   m.Model,
			Used1h:  m.Used1h,
			Limit1h: m.Limit1h,
		})
	}
	for _, a := range snap.Auths {
		out.Auths = append(out.Auths, PoolAuthBrief{
			AuthID:       a.AuthID,
			Channel:      a.Channel,
			PoolStrategy: a.PoolStrategy,
		})
	}
	return out, partial, nil
}

// Avoid pulling in logrus solely for the unused-import tripwire.
var _ = log.WithError
