package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/apikeylimits"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestGetAPIKeyPolicyUsageReportsNamesAndModels(t *testing.T) {
	const key = "sk-policy-usage-models-test"
	cfg := &config.Config{AuthDir: t.TempDir()}
	cfg.APIKeys = []string{key}
	cfg.APIKeyPolicies = []config.APIKeyPolicy{{Key: key, Name: "Team A"}}

	store := apikeylimits.Default()
	store.AddUsage(key, apikeylimits.Usage{Model: "small", Tokens: 10})
	store.AddUsage(key, apikeylimits.Usage{Model: "big", Tokens: 90, Cost: 1.5})
	store.AddUsage(key, apikeylimits.Usage{Model: "big", Tokens: 10, Failed: true, AuthID: "acc-x", RequestID: "req-policy-usage"})

	h := NewHandlerWithoutConfigFilePath(cfg, nil)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/access/api-key-usage", nil)
	h.GetAPIKeyPolicyUsage(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Keys []apiKeyPolicyUsage `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Keys) != 1 || payload.Keys[0].Name != "Team A" {
		t.Fatalf("keys = %+v", payload.Keys)
	}
	total, ok := payload.Keys[0].Usage["total"]
	if !ok || total.ResetAt != nil || total.Requests < 3 || total.Failed < 1 {
		t.Fatalf("total window = %+v", total)
	}
	if day := payload.Keys[0].Usage["day"]; day.ResetAt == nil {
		t.Fatal("day window must report reset_at")
	}
	if len(total.Models) != 2 || total.Models[0].Model != "big" || total.Models[0].Tokens < 100 ||
		total.Models[0].Requests < 2 || total.Models[0].Failed < 1 || total.Models[1].Model != "small" {
		t.Fatalf("models = %+v", total.Models)
	}
	if len(payload.Keys[0].Daily) == 0 || len(payload.Keys[0].Hourly) == 0 ||
		len(payload.Keys[0].Daily[len(payload.Keys[0].Daily)-1].Models) != 2 {
		t.Fatalf("history daily=%+v hourly=%+v", payload.Keys[0].Daily, payload.Keys[0].Hourly)
	}
	if accs := total.Accounts; len(accs) == 0 || accs[0].ID != "acc-x" || accs[0].Name != "acc-x" {
		t.Fatalf("accounts = %+v", total.Accounts)
	}

	rec = httptest.NewRecorder()
	ginCtx, _ = gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/access/api-key-requests?limit=1", nil)
	h.GetAPIKeyRequests(ginCtx)
	var logs struct {
		Requests []apiKeyRequestEntry `json:"requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &logs); err != nil || len(logs.Requests) != 1 {
		t.Fatalf("requests decode err=%v body=%s", err, rec.Body.String())
	}
	if r := logs.Requests[0]; r.RequestID != "req-policy-usage" || r.KeyName != "Team A" || r.Key != key || r.AccountName != "acc-x" || !r.Failed {
		t.Fatalf("request entry = %+v", r)
	}
}
