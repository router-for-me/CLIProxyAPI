package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
)

func withUsageStatsStore(t *testing.T) *usagestats.Store {
	t.Helper()
	if errConfigure := usagestats.Configure(true, filepath.Join(t.TempDir(), usagestats.DefaultFileName)); errConfigure != nil {
		t.Fatalf("configure usage statistics: %v", errConfigure)
	}
	t.Cleanup(func() {
		if errShutdown := usagestats.Shutdown(); errShutdown != nil {
			t.Errorf("shutdown usage statistics: %v", errShutdown)
		}
	})
	return usagestats.Current()
}

func serveUsageStats(t *testing.T, handler gin.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	handler(ginCtx)
	return rec
}

func TestUsageStatsEndpointsReportDisabled(t *testing.T) {
	h := &Handler{}
	for name, handler := range map[string]gin.HandlerFunc{
		"stats": h.GetUsageStats, "speed": h.GetUsageSpeed, "settings": h.GetUsageSettings, "put settings": h.PutUsageSettings,
		"quota samples": h.GetUsageQuotaSamples, "limit value": h.GetUsageLimitValue,
	} {
		rec := serveUsageStats(t, handler, http.MethodGet, "/v8/management/observability/usage/stats", "{}")
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "usage_statistics_disabled") {
			t.Errorf("%s: status = %d body = %s, want 409 usage_statistics_disabled", name, rec.Code, rec.Body.String())
		}
	}
}

func TestGetUsageStatsGroupsFiltersAndPrices(t *testing.T) {
	store := withUsageStatsStore(t)
	at := time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)
	for _, row := range []usagestats.Row{
		{Timestamp: at, Model: "claude-opus-5-5", Project: "alpha", InputTokens: 1_000_000, TotalTokens: 1_000_000},
		{Timestamp: at, Model: "claude-opus-5-5", Project: "beta", InputTokens: 2_000_000, TotalTokens: 2_000_000},
	} {
		if errInsert := store.Insert(context.Background(), row); errInsert != nil {
			t.Fatalf("insert row: %v", errInsert)
		}
	}

	target := "/v8/management/observability/usage/stats?group_by=day,project&filter.project=beta&utc_offset_minutes=-480" +
		"&from=" + msString(at.Add(-time.Hour)) + "&to=" + msString(at.Add(time.Hour))
	rec := serveUsageStats(t, (&Handler{}).GetUsageStats, http.MethodGet, target, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Groups []struct {
			Dimensions  map[string]string `json:"dimensions"`
			TotalTokens int64             `json:"total_tokens"`
			CostUSD     float64           `json:"cost_usd"`
		} `json:"groups"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(payload.Groups) != 1 {
		t.Fatalf("groups = %+v, want only the filtered project", payload.Groups)
	}
	group := payload.Groups[0]
	// 06:00 UTC is still the previous day at UTC-8.
	if group.Dimensions["day"] != "2026-11-11" || group.Dimensions["project"] != "beta" || group.TotalTokens != 2_000_000 || group.CostUSD != 8 {
		t.Fatalf("group = %+v", group)
	}
}

func TestGetUsageStatsRejectsBadQueries(t *testing.T) {
	withUsageStatsStore(t)
	for _, query := range []string{
		"group_by=nonsense", "filter.nonsense=x", "from=abc", "to=abc", "utc_offset_minutes=9000", "limit=0", "from=20&to=10",
	} {
		rec := serveUsageStats(t, (&Handler{}).GetUsageStats, http.MethodGet, "/v8/management/observability/usage/stats?"+query, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d body = %s, want 400", query, rec.Code, rec.Body.String())
		}
	}
	rec := serveUsageStats(t, (&Handler{}).GetUsageSpeed, http.MethodGet, "/v8/management/observability/usage/speed?group_by=day", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("speed by day: status = %d, want 400", rec.Code)
	}
}

func TestUsageSettingsRoundTripAndValidation(t *testing.T) {
	withUsageStatsStore(t)
	h := &Handler{}

	body := `{"model_prices":{"my-model":{"input":1.5,"output":6}},"account_plans":[{"provider":"claude","account":"me@example.com","monthly_usd":200}]}`
	if rec := serveUsageStats(t, h.PutUsageSettings, http.MethodPut, "/v8/management/observability/usage/settings", body); rec.Code != http.StatusOK {
		t.Fatalf("put status = %d body = %s", rec.Code, rec.Body.String())
	}

	rec := serveUsageStats(t, h.GetUsageSettings, http.MethodGet, "/v8/management/observability/usage/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Settings      usagestats.Settings         `json:"settings"`
		DefaultPrices map[string]usagestats.Price `json:"default_prices"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if payload.Settings.ModelPrices["my-model"].Output != 6 || len(payload.Settings.AccountPlans) != 1 {
		t.Fatalf("settings = %+v", payload.Settings)
	}
	if len(payload.DefaultPrices) == 0 {
		t.Fatal("default prices missing from response")
	}

	for name, invalid := range map[string]string{
		"negative price": `{"model_prices":{"m":{"input":-1,"output":1}}}`,
		"unknown field":  `{"model_prices":{},"surprise":true}`,
		"malformed":      `{`,
	} {
		if rec := serveUsageStats(t, h.PutUsageSettings, http.MethodPut, "/v8/management/observability/usage/settings", invalid); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d body = %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
}

func TestGetUsageQuotaSamplesReturnsSeriesWithCarryIn(t *testing.T) {
	store := withUsageStatsStore(t)
	at := time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)
	key := usagestats.QuotaSeriesKey{Provider: "claude", AuthIndex: "auth-1", Account: "me@example.com", Window: "5h"}
	for _, sample := range []usagestats.QuotaSample{
		{QuotaSeriesKey: key, QuotaPoint: usagestats.QuotaPoint{TimestampMs: at.Add(-time.Hour).UnixMilli(), UsedPercent: 30, ResetsAtMs: at.Add(time.Hour).UnixMilli()}},
		{QuotaSeriesKey: key, QuotaPoint: usagestats.QuotaPoint{TimestampMs: at.UnixMilli(), UsedPercent: 31, ResetsAtMs: at.Add(time.Hour).UnixMilli()}},
	} {
		if errRecord := store.RecordQuotaSample(context.Background(), sample); errRecord != nil {
			t.Fatalf("record quota sample: %v", errRecord)
		}
	}

	target := "/v8/management/observability/usage/quota-samples?filter.window=5h&from=" + msString(at.Add(-time.Minute)) + "&to=" + msString(at.Add(time.Minute))
	rec := serveUsageStats(t, (&Handler{}).GetUsageQuotaSamples, http.MethodGet, target, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		From     int64 `json:"from"`
		BucketMs int64 `json:"bucket_ms"`
		Series   []struct {
			Provider  string                  `json:"provider"`
			AuthIndex string                  `json:"auth_index"`
			Account   string                  `json:"account"`
			Window    string                  `json:"window"`
			CarryIn   *usagestats.QuotaPoint  `json:"carry_in"`
			Points    []usagestats.QuotaPoint `json:"points"`
			Thinned   bool                    `json:"thinned"`
		} `json:"series"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if payload.From != at.Add(-time.Minute).UnixMilli() || payload.BucketMs != 0 || len(payload.Series) != 1 {
		t.Fatalf("payload = %s", rec.Body.String())
	}
	series := payload.Series[0]
	if series.Provider != "claude" || series.AuthIndex != "auth-1" || series.Account != "me@example.com" || series.Window != "5h" || series.Thinned {
		t.Fatalf("series = %+v", series)
	}
	if series.CarryIn == nil || series.CarryIn.UsedPercent != 30 || len(series.Points) != 1 || series.Points[0].UsedPercent != 31 {
		t.Fatalf("carry_in = %+v points = %+v", series.CarryIn, series.Points)
	}

	bad := serveUsageStats(t, (&Handler{}).GetUsageQuotaSamples, http.MethodGet, "/v8/management/observability/usage/quota-samples?filter.model=x", "")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown filter: status = %d, want 400", bad.Code)
	}
}

func TestGetUsageLimitValueChargesRisingIntervals(t *testing.T) {
	store := withUsageStatsStore(t)
	at := time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)
	key := usagestats.QuotaSeriesKey{Provider: "claude", AuthIndex: "auth-1", Account: "me@example.com", Window: "7d"}
	for i, percent := range []float64{10, 12} {
		sample := usagestats.QuotaSample{QuotaSeriesKey: key, QuotaPoint: usagestats.QuotaPoint{TimestampMs: at.Add(time.Duration(i) * time.Hour).UnixMilli(), UsedPercent: percent}}
		if errRecord := store.RecordQuotaSample(context.Background(), sample); errRecord != nil {
			t.Fatalf("record quota sample: %v", errRecord)
		}
	}
	// Two opus requests complete inside the rise: $8 of output at $20 per million.
	for _, row := range []usagestats.Row{
		{Timestamp: at.Add(10 * time.Minute), LatencyMs: 1000, Model: "claude-opus-5-5", AuthIndex: "auth-1", OutputTokens: 200_000, TotalTokens: 200_000},
		{Timestamp: at.Add(20 * time.Minute), LatencyMs: 1000, Model: "claude-opus-5-5", AuthIndex: "auth-1", OutputTokens: 200_000, TotalTokens: 200_000},
		{Timestamp: at.Add(30 * time.Minute), LatencyMs: 1000, Model: "claude-opus-5-5", AuthIndex: "auth-2", OutputTokens: 200_000, TotalTokens: 200_000},
	} {
		if errInsert := store.Insert(context.Background(), row); errInsert != nil {
			t.Fatalf("insert row: %v", errInsert)
		}
	}

	target := "/v8/management/observability/usage/limit-value?from=" + msString(at.Add(-time.Minute)) + "&to=" + msString(at.Add(2*time.Hour))
	rec := serveUsageStats(t, (&Handler{}).GetUsageLimitValue, http.MethodGet, target, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	type spend struct {
		Intervals              int64    `json:"intervals"`
		PercentUsed            float64  `json:"percent_used"`
		Requests               int64    `json:"requests"`
		CostUSD                float64  `json:"cost_usd"`
		USDPerPercent          *float64 `json:"usd_per_percent"`
		OutputTokensPerPercent *float64 `json:"output_tokens_per_percent"`
	}
	var payload struct {
		Series []struct {
			Window     string `json:"window"`
			ModelScope string `json:"model_scope"`
			spend
			Models []struct {
				Model string `json:"model"`
				spend
			} `json:"models"`
		} `json:"series"`
		UnpricedModels []string `json:"unpriced_models"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(payload.Series) != 1 || payload.UnpricedModels == nil {
		t.Fatalf("payload = %s", rec.Body.String())
	}
	series := payload.Series[0]
	if series.Window != "7d" || series.ModelScope != "" || series.Intervals != 1 || series.PercentUsed != 2 || series.Requests != 2 ||
		series.CostUSD != 8 || series.USDPerPercent == nil || *series.USDPerPercent != 4 || series.OutputTokensPerPercent == nil || *series.OutputTokensPerPercent != 200_000 {
		t.Fatalf("series = %s", rec.Body.String())
	}
	if len(series.Models) != 1 || series.Models[0].Model != "claude-opus-5-5" || series.Models[0].USDPerPercent == nil || *series.Models[0].USDPerPercent != 4 {
		t.Fatalf("models = %s", rec.Body.String())
	}
}

func msString(t time.Time) string {
	raw, _ := json.Marshal(t.UnixMilli())
	return string(raw)
}
