package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// fakeQuotaRepo is a test double for the quota-share repo methods. The handler
// depends on GetAuthQuota/GetPoolQuota and the 2-second timeout behavior; this
// fake lets tests drive both the success and timeout paths without spinning
// up a real PG instance.
type fakeQuotaRepo struct {
	authSnapshot QuotaAuthSnapshot
	authErr      error
	authDelay    time.Duration // sleep before returning; > 2s exercises timeout
	authPartial  bool          // force partial=true on the auth path

	poolSnapshot QuotaPoolSnapshot
	poolErr      error
	poolDelay    time.Duration
	poolPartial  bool
}

func (f *fakeQuotaRepo) GetAuthQuota(ctx context.Context, authID string) (QuotaAuthSnapshot, bool, error) {
	if f.authDelay > 0 {
		select {
		case <-ctx.Done():
			// Caller (handler) hit its 2s timeout. Surface a partial snapshot
			// so the handler can return 200 with partial=true.
			return QuotaAuthSnapshot{AuthID: authID, Channel: "fake", PoolStrategy: "fallback"}, true, nil
		case <-time.After(f.authDelay):
		}
	}
	if f.authErr != nil {
		return QuotaAuthSnapshot{}, false, f.authErr
	}
	out := f.authSnapshot
	if out.AuthID == "" {
		out.AuthID = authID
	}
	return out, f.authPartial, nil
}

func (f *fakeQuotaRepo) GetPoolQuota(ctx context.Context, key string) (QuotaPoolSnapshot, bool, error) {
	if f.poolDelay > 0 {
		select {
		case <-ctx.Done():
			return QuotaPoolSnapshot{PoolKey: key}, true, nil
		case <-time.After(f.poolDelay):
		}
	}
	if f.poolErr != nil {
		return QuotaPoolSnapshot{}, false, f.poolErr
	}
	out := f.poolSnapshot
	if out.PoolKey == "" {
		out.PoolKey = key
	}
	return out, f.poolPartial, nil
}

// quotaShareHandlersTest wires a handler with the fake repo. The handler
// gates the routes on pgEnabled(); we wire pgUsage/pgControl so that check
// passes. The quota repo itself is the fake — tests do not need a real PG.
func quotaShareHandlersTest(t *testing.T, repo quotaRepo) *Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	// Wire minimal PG-ish state so pgEnabled() returns true. Use a zero-value
	// PostgresStore stub == nil? — the handler only checks pgControl/pgUsage
	// pointers. We must use SetPGControl for the pgControl check (which calls
	// h.pgControl.DB() == nil downstream); for quota-share we instead expose
	// pgEnabled() via a dedicated setter to keep the test self-contained.
	h.setQuotaRepo(repo)
	h.setPGEnabledForTest(true)
	return h
}

// runQuotaShare drives one Gin request through the quota-share handler and
// returns the recorded response. Mirrors runHandler() from runtime_config
// tests but uses a smaller dispatcher surface.
func runQuotaShare(h *Handler, method, path string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Use a tiny router that wires only the per-route handlers we want to
	// test. Using r.GET("/v0/management/auths/:id/quota", ...) keeps the
	// route table tiny and unambiguous for the dispatcher below.
	switch {
	case method == "GET" && path == "/v0/management/auths/auth-1/quota":
		r.GET("/v0/management/auths/:id/quota", h.GetAuthQuota)
	case method == "GET" && path == "/v0/management/auths/auth-timeout/quota":
		r.GET("/v0/management/auths/:id/quota", h.GetAuthQuota)
	case method == "GET" && path == "/v0/management/pools/openai:7/quota":
		r.GET("/v0/management/pools/:key/quota", h.GetPoolQuota)
	default:
		panic("unknown route: " + method + " " + path)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	r.ServeHTTP(w, req)
	return w
}

// TestQuotaShareAuthsHeadroomMath pins the headroom_pct math:
// (limit - used) / limit * 100, rounded to 1 decimal. Also pins the
// over_limit flag, the negative-clamp-to-0 behavior, and the limit<=0
// special case. The handler delegates the math to computeHeadroom; this
// test drives the handler end-to-end via the fake repo.
func TestQuotaShareAuthsHeadroomMath(t *testing.T) {
	repo := &fakeQuotaRepo{
		authSnapshot: QuotaAuthSnapshot{
			AuthID:       "auth-1",
			Channel:      "openai",
			PoolStrategy: "fallback",
			Windows: []WindowQuota{
				{Size: "1m", Used: 12345, Limit: 1000000, HeadroomPct: 98.8, OverLimit: false},
				{Size: "1h", Used: 200000, Limit: 5000000, HeadroomPct: 96.0, OverLimit: false},
				{Size: "1d", Used: 1500000, Limit: 50000000, HeadroomPct: 97.0, OverLimit: false},
			},
			Models: []ModelQuota{
				{Model: "gpt-5", Used1h: 80000, Limit1h: 1000000},
			},
		},
	}
	h := quotaShareHandlersTest(t, repo)
	w := runQuotaShare(h, "GET", "/v0/management/auths/auth-1/quota")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp quotaAuthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AuthID != "auth-1" || resp.Channel != "openai" || resp.PoolStrategy != "fallback" {
		t.Fatalf("identity = %+v, want auth-1/openai/fallback", resp)
	}
	if len(resp.Windows) != 3 {
		t.Fatalf("windows = %d, want 3", len(resp.Windows))
	}
	// Spot-check that the math flows through unchanged.
	if resp.Windows[0].HeadroomPct != 98.8 {
		t.Errorf("1m headroom_pct = %.2f, want 98.8", resp.Windows[0].HeadroomPct)
	}
	if resp.Windows[1].HeadroomPct != 96.0 {
		t.Errorf("1h headroom_pct = %.2f, want 96.0", resp.Windows[1].HeadroomPct)
	}
	if len(resp.Models) != 1 || resp.Models[0].Model != "gpt-5" || resp.Models[0].Used1h != 80000 {
		t.Errorf("models = %+v, want single gpt-5 row with used_1h=80000", resp.Models)
	}

	// Also pin computeHeadroom directly — guards against handler/DTO drift.
	cases := []struct {
		name        string
		used, limit int64
		wantPct     float64
		wantOver    bool
	}{
		{"normal", 200, 1000, 80.0, false},
		{"exact", 1000, 1000, 0.0, false},
		{"over_limit", 1500, 1000, 0.0, true},
		{"zero_limit", 100, 0, 100.0, false},
		{"negative_limit", 100, -1, 100.0, false},
		{"rounding", 333, 1000, 66.7, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pct, over := computeHeadroom(tc.used, tc.limit)
			if pct != tc.wantPct || over != tc.wantOver {
				t.Errorf("computeHeadroom(used=%d, limit=%d) = (%.4f, %v), want (%.4f, %v)",
					tc.used, tc.limit, pct, over, tc.wantPct, tc.wantOver)
			}
		})
	}
}

// TestQuotaShareAuthsHandles2sTimeout pins the round-2 design's contract:
// on a slow (>2s) query the handler must return 200 with partial=true, not
// 500. The fake repo blocks on the context, mirroring the production
// pgx.QueryContext behavior under load.
func TestQuotaShareAuthsHandles2sTimeout(t *testing.T) {
	repo := &fakeQuotaRepo{
		authDelay: 4 * time.Second,
		authSnapshot: QuotaAuthSnapshot{
			AuthID:  "auth-timeout",
			Channel: "claude",
		},
	}
	h := quotaShareHandlersTest(t, repo)
	start := time.Now()
	w := runQuotaShare(h, "GET", "/v0/management/auths/auth-timeout/quota")
	elapsed := time.Since(start)
	// The handler caps at 2s; the test should not block for the full 4s.
	if elapsed > 3*time.Second {
		t.Fatalf("handler took %s, want ~2s (timeout budget)", elapsed)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 on partial; body=%s", w.Code, w.Body.String())
	}
	var resp quotaAuthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Partial {
		t.Errorf("partial = false, want true on 2s timeout")
	}
	if resp.AuthID != "auth-timeout" {
		t.Errorf("auth_id = %q, want auth-timeout (preserved on partial)", resp.AuthID)
	}
}

// TestQuotaShareReturns503WithoutPG pins the management-route contract: when
// PG is not enabled, quota-share endpoints return 503 immediately.
func TestQuotaShareReturns503WithoutPG(t *testing.T) {
	repo := &fakeQuotaRepo{}
	h := quotaShareHandlersTest(t, repo)
	h.setPGEnabledForTest(false)
	w := runQuotaShare(h, "GET", "/v0/management/auths/auth-1/quota")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	w2 := runQuotaShare(h, "GET", "/v0/management/pools/openai:7/quota")
	if w2.Code != http.StatusServiceUnavailable {
		t.Fatalf("pool status = %d, want 503; body=%s", w2.Code, w2.Body.String())
	}
}

// TestQuotaSharePoolHandlerShape pins the pool-quota response shape: it
// sums across the pool's auths and includes an auths[] brief list. The fake
// repo returns a representative snapshot.
func TestQuotaSharePoolHandlerShape(t *testing.T) {
	repo := &fakeQuotaRepo{
		poolSnapshot: QuotaPoolSnapshot{
			PoolKey: "openai:7",
			Windows: []WindowQuota{
				{Size: "1m", Used: 50000, Limit: 2000000, HeadroomPct: 97.5, OverLimit: false},
				{Size: "1h", Used: 400000, Limit: 10000000, HeadroomPct: 96.0, OverLimit: false},
			},
			Models: []ModelQuota{
				{Model: "gpt-5", Used1h: 160000, Limit1h: 2000000},
			},
			Auths: []PoolAuthBrief{
				{AuthID: "openai:7/key-1", Channel: "openai", PoolStrategy: "fallback"},
				{AuthID: "openai:7/key-2", Channel: "openai", PoolStrategy: "fallback"},
			},
		},
	}
	h := quotaShareHandlersTest(t, repo)
	w := runQuotaShare(h, "GET", "/v0/management/pools/openai:7/quota")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp quotaPoolResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.PoolKey != "openai:7" {
		t.Errorf("pool_key = %q, want openai:7", resp.PoolKey)
	}
	if len(resp.Windows) != 2 || resp.Windows[0].HeadroomPct != 97.5 {
		t.Errorf("windows = %+v, want 2 rows starting with 97.5%%", resp.Windows)
	}
	if len(resp.Auths) != 2 {
		t.Errorf("auths = %d, want 2 brief rows", len(resp.Auths))
	}
	if len(resp.Models) != 1 || resp.Models[0].Model != "gpt-5" {
		t.Errorf("models = %+v, want one gpt-5 row", resp.Models)
	}
}

// TestQuotaShareRepoErrorReturns500 pins the failure path: a non-timeout
// repo error becomes a 500 (NOT a partial=true response). The partial flag
// is reserved for the timeout case.
func TestQuotaShareRepoErrorReturns500(t *testing.T) {
	repo := &fakeQuotaRepo{
		authErr: fmt.Errorf("simulated db failure"),
	}
	h := quotaShareHandlersTest(t, repo)
	w := runQuotaShare(h, "GET", "/v0/management/auths/auth-1/quota")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
	}
}

// TestQuotaSharePartialUnderPGSlowness pins the round-2 review-finding
// fix end-to-end at the handler: when the store reports partial=true
// (because at least one per-window sub-query hit the store's 500ms
// per-window timeout), the handler MUST surface 200 + partial=true and
// preserve the windows that did come back, with zero placeholders for
// the timed-out ones. The fake repo is the test seam — production
// wires the store, which under real PG slowness fires the same path.
//
// This is the spec example: hourly + monthly come back, weekly timed out
// and is recorded as a zero placeholder (Limit=0 → headroom_pct=100.0).
func TestQuotaSharePartialUnderPGSlowness(t *testing.T) {
	repo := &fakeQuotaRepo{
		authPartial: true,
		authSnapshot: QuotaAuthSnapshot{
			AuthID:       "auth-partial",
			Channel:      "openai",
			PoolStrategy: "fallback",
			Windows: []WindowQuota{
				{Size: "hourly", Used: 100, Limit: 1000, HeadroomPct: 90.0, OverLimit: false},
				// weekly timed out — placeholder zero (Limit=0 → headroom=100.0).
				{Size: "weekly", Used: 0, Limit: 0, HeadroomPct: 100.0, OverLimit: false},
				{Size: "monthly", Used: 300, Limit: 5000, HeadroomPct: 94.0, OverLimit: false},
			},
		},
	}
	h := quotaShareHandlersTest(t, repo)
	w := runQuotaShare(h, "GET", "/v0/management/auths/auth-1/quota")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 on partial; body=%s", w.Code, w.Body.String())
	}
	var resp quotaAuthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Partial {
		t.Errorf("partial = false, want true when store reports partial")
	}
	if len(resp.Windows) != 3 {
		t.Fatalf("windows = %d, want 3 (timed-out window preserved as zero placeholder)", len(resp.Windows))
	}
	// hourly and monthly came back; weekly was zeroed by the store.
	if resp.Windows[0].Used != 100 || resp.Windows[0].HeadroomPct != 90.0 {
		t.Errorf("hourly window = %+v, want used=100 headroom=90.0", resp.Windows[0])
	}
	if resp.Windows[1].Used != 0 || resp.Windows[1].HeadroomPct != 100.0 {
		t.Errorf("weekly window = %+v, want used=0 headroom=100.0 (zero placeholder for timed-out window)", resp.Windows[1])
	}
	if resp.Windows[2].Used != 300 || resp.Windows[2].HeadroomPct != 94.0 {
		t.Errorf("monthly window = %+v, want used=300 headroom=94.0", resp.Windows[2])
	}
}
