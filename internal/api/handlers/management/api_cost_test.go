package management

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/apicost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestGetAPICostReturnsBucketsAccountsAndUnpricedModels(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	store := apicost.NewStore()
	previousStore, previousPricing := apiCostStore, apiCostPricing
	apiCostStore = func() *apicost.Store { return store }
	apiCostPricing = func() apicost.PricingStatus { return apicost.PricingStatus{Source: "embedded", Models: 3} }
	t.Cleanup(func() { apiCostStore, apiCostPricing = previousStore, previousPricing })

	now := time.Now().UTC().Truncate(time.Hour)
	store.Import([]apicost.Bucket{
		{Hour: now.Add(-40 * 24 * time.Hour), Model: "claude-opus-5-5", AuthID: "cost-claude", Requests: 1, CostUSD: 9},
		{Hour: now, Model: "claude-opus-5-5", Provider: "claude", AuthID: "cost-claude", Requests: 2, CostUSD: 1.5},
		{Hour: now, Model: "mystery", Provider: "claude", AuthID: "cost-gone", Requests: 1, UnpricedRequests: 1},
	})
	manager := coreauth.NewManager(nil, nil, nil)
	registerRuntimeAuth(t, manager, &coreauth.Auth{ID: "cost-claude", Provider: "claude"})
	registerRuntimeAuth(t, manager, &coreauth.Auth{ID: "cost-unused", Provider: "codex"})
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	get := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/observability/usage/cost"+query, nil)
		h.GetAPICost(ginCtx)
		return rec
	}

	rec := get("")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if got := ojson.GetBytesNoCopy(body, "buckets").Array(); len(got) != 2 {
		t.Fatalf("buckets = %s, want the two recent buckets", ojson.GetBytesNoCopy(body, "buckets").Raw)
	}
	if got := ojson.GetBytesNoCopy(body, "buckets.0.cost_usd").Float(); got != 1.5 {
		t.Fatalf("cost_usd = %v", got)
	}
	accounts := ojson.GetBytesNoCopy(body, "accounts").Array()
	if len(accounts) != 1 || accounts[0].Get("id").String() != "cost-claude" {
		t.Fatalf("accounts = %s, want only the registered credential with usage", ojson.GetBytesNoCopy(body, "accounts").Raw)
	}
	if got := ojson.GetBytesNoCopy(body, "unpriced_models.0.model").String(); got != "mystery" {
		t.Fatalf("unpriced_models = %s", ojson.GetBytesNoCopy(body, "unpriced_models").Raw)
	}
	if got := ojson.GetBytesNoCopy(body, "pricing.source").String(); got != "embedded" {
		t.Fatalf("pricing = %s", ojson.GetBytesNoCopy(body, "pricing").Raw)
	}

	since := now.Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	if got := ojson.GetBytesNoCopy(get("?since="+since).Body.Bytes(), "buckets").Array(); len(got) != 3 {
		t.Fatalf("since buckets = %d, want 3", len(got))
	}
	if rec := get("?since=last-week"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid since status = %d", rec.Code)
	}
}
