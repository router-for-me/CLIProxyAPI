package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetUsageBillingSettingsReturnsNormalizedSecretSafeSnapshot(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		RemoteManagement: config.RemoteManagement{
			SecretKey: "raw-management-secret",
		},
		UsageStatisticsEnabled:       true,
		UsageStatisticsRetentionDays: 0,
		UsagePricing: config.UsagePricingConfig{
			Currency: " cny ",
			Version:  " prices-v2 ",
			Rules: []config.UsagePricingRule{{
				Provider: " ", Model: " gpt-* ", InputPerMillion: " 1.25 ",
			}},
		},
	}}
	h.cfg.APIKeys = []string{"raw-client-secret"}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-billing-settings", nil)
	h.GetUsageBillingSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "raw-client-secret") || strings.Contains(rec.Body.String(), "raw-management-secret") {
		t.Fatalf("response exposed a secret: %s", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	var response usageBillingSettingsResponse
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if !response.Enabled || response.RetentionDays != config.DefaultUsageStatisticsRetentionDays {
		t.Fatalf("usage settings = %#v", response)
	}
	if response.Pricing.Currency != "CNY" || response.Pricing.Version != "prices-v2" || len(response.Pricing.Rules) != 1 {
		t.Fatalf("pricing = %#v", response.Pricing)
	}
	rule := response.Pricing.Rules[0]
	if rule.Provider != "*" || rule.Model != "gpt-*" || rule.ServiceTier != "*" || rule.InputPerMillion != "1.25" {
		t.Fatalf("rule = %#v", rule)
	}
	if len(response.Revision) != 64 || response.Limits.MaxRules != maxUsageSettingsPricingRules || response.Limits.MaxRetentionDays != config.MaxUsageStatisticsRetentionDays {
		t.Fatalf("revision or limits missing: %#v", response)
	}

	response.Pricing.Rules[0].Model = "mutated"
	if h.cfg.UsagePricing.Rules[0].Model != " gpt-* " {
		t.Fatal("response rules share memory with config")
	}
}

func TestUsageBillingSettingsRevisionIsDeterministicForNormalizedValues(t *testing.T) {
	left := usageBillingSettingsSnapshotLocked(&config.Config{
		UsageStatisticsRetentionDays: 0,
		UsagePricing: config.UsagePricingConfig{Rules: []config.UsagePricingRule{{
			Provider: " ", Model: " gpt-* ", InputPerMillion: " 1.00 ",
		}}},
	})
	right := usageBillingSettingsSnapshotLocked(&config.Config{
		UsageStatisticsRetentionDays: config.DefaultUsageStatisticsRetentionDays,
		UsagePricing: config.UsagePricingConfig{
			Currency: "usd", Version: "default",
			Rules: []config.UsagePricingRule{{
				Provider: "*", Model: "gpt-*", ServiceTier: "*", InputPerMillion: "1.00",
			}},
		},
	})
	if left.Revision != right.Revision {
		t.Fatalf("equivalent settings revisions differ: %q != %q", left.Revision, right.Revision)
	}

	right.Pricing.Rules[0].InputPerMillion = "2.00"
	if left.Revision == usageBillingSettingsRevision(right) {
		t.Fatal("revision did not change with pricing")
	}
}

func TestPatchUsageBillingSettingsNormalizesAndPersists(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			UsageStatisticsRetentionDays: 30,
			UsagePricing: config.UsagePricingConfig{
				Currency: "USD", Version: "v1",
			},
		},
		configFilePath: writeTestConfigFile(t),
	}
	revision := usageBillingSettingsSnapshotLocked(h.cfg).Revision
	body := `{
		"enabled":true,
		"retention_days":3650,
		"pricing":{
			"currency":" cny ",
			"version":" rates-2026 ",
			"rules":[{
				"provider":" ",
				"model":" glm-* ",
				"service-tier":" ",
				"input-per-million":" 0.25 ",
				"output-per-million":"2"
			}]
		},
		"expected_revision":"` + revision + `"
	}`

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/usage-billing-settings", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchUsageBillingSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !h.cfg.UsageStatisticsEnabled || h.cfg.UsageStatisticsRetentionDays != 3650 {
		t.Fatalf("usage config = %#v", h.cfg)
	}
	if h.cfg.UsagePricing.Currency != "CNY" || h.cfg.UsagePricing.Version != "rates-2026" || len(h.cfg.UsagePricing.Rules) != 1 {
		t.Fatalf("pricing = %#v", h.cfg.UsagePricing)
	}
	rule := h.cfg.UsagePricing.Rules[0]
	if rule.Provider != "*" || rule.Model != "glm-*" || rule.ServiceTier != "*" || rule.InputPerMillion != "0.25" || rule.OutputPerMillion != "2" {
		t.Fatalf("normalized rule = %#v", rule)
	}
}

func TestPatchUsageBillingSettingsMergesPartialPricing(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			UsageStatisticsRetentionDays: 30,
			UsagePricing: config.UsagePricingConfig{
				Currency: "USD",
				Version:  "v1",
				Rules: []config.UsagePricingRule{{
					Provider: "openai", Model: "gpt-*", InputPerMillion: "1",
				}},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}
	revision := usageBillingSettingsSnapshotLocked(h.cfg).Revision

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/usage-billing-settings", strings.NewReader(`{
		"pricing":{"currency":"CNY"},
		"expected_revision":"`+revision+`"
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchUsageBillingSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if h.cfg.UsagePricing.Currency != "CNY" || h.cfg.UsagePricing.Version != "v1" || len(h.cfg.UsagePricing.Rules) != 1 || h.cfg.UsagePricing.Rules[0].Model != "gpt-*" {
		t.Fatalf("partial pricing update replaced unrelated values: %#v", h.cfg.UsagePricing)
	}
}

func TestPatchUsageBillingSettingsRequiresExpectedRevision(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			UsageStatisticsRetentionDays: 30,
			UsagePricing: config.UsagePricingConfig{
				Currency: "USD", Version: "v1",
			},
		},
		configFilePath: writeTestConfigFile(t),
	}
	before := usageBillingSettingsSnapshotLocked(h.cfg)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/usage-billing-settings", strings.NewReader(`{"enabled":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchUsageBillingSettings(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if after := usageBillingSettingsSnapshotLocked(h.cfg); after.Revision != before.Revision {
		t.Fatalf("missing CAS revision changed config: %#v", h.cfg)
	}
}

func TestPatchUsageBillingSettingsAllowsOnlyOneWriterPerRevision(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			UsageStatisticsRetentionDays: 30,
			UsagePricing: config.UsagePricingConfig{
				Currency: "USD", Version: "v1",
			},
		},
		configFilePath: writeTestConfigFile(t),
	}
	revision := usageBillingSettingsSnapshotLocked(h.cfg).Revision

	patch := func(retention int) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		body := fmt.Sprintf(`{"retention_days":%d,"expected_revision":%q}`, retention, revision)
		c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/usage-billing-settings", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.PatchUsageBillingSettings(c)
		return rec
	}

	if first := patch(31); first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d; body=%s", first.Code, http.StatusOK, first.Body.String())
	}
	if second := patch(32); second.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want %d; body=%s", second.Code, http.StatusConflict, second.Body.String())
	}
	if h.cfg.UsageStatisticsRetentionDays != 31 {
		t.Fatalf("retention = %d, want first writer value 31", h.cfg.UsageStatisticsRetentionDays)
	}
}

func TestPatchUsageBillingSettingsRejectsStaleRevisionWithoutChanges(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			UsageStatisticsEnabled:       false,
			UsageStatisticsRetentionDays: 30,
			UsagePricing: config.UsagePricingConfig{
				Currency: "USD", Version: "v1",
			},
		},
		configFilePath: writeTestConfigFile(t),
	}
	before := usageBillingSettingsSnapshotLocked(h.cfg)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/usage-billing-settings", strings.NewReader(`{
		"enabled":true,
		"retention_days":90,
		"expected_revision":"stale"
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchUsageBillingSettings(c)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	after := usageBillingSettingsSnapshotLocked(h.cfg)
	if before.Revision != after.Revision || h.cfg.UsageStatisticsEnabled || h.cfg.UsageStatisticsRetentionDays != 30 {
		t.Fatalf("stale update changed config: %#v", h.cfg)
	}
}

func TestPatchUsageBillingSettingsRollsBackWhenPersistenceFails(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			UsageStatisticsEnabled:       false,
			UsageStatisticsRetentionDays: 30,
			UsagePricing: config.UsagePricingConfig{
				Currency: "USD", Version: "v1",
			},
		},
		configFilePath: filepath.Join(t.TempDir(), "missing", "config.yaml"),
	}
	before := usageBillingSettingsSnapshotLocked(h.cfg)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/usage-billing-settings", strings.NewReader(`{
		"enabled":true,
		"expected_revision":"`+before.Revision+`"
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchUsageBillingSettings(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	after := usageBillingSettingsSnapshotLocked(h.cfg)
	if after.Revision != before.Revision || h.cfg.UsageStatisticsEnabled {
		t.Fatalf("failed persistence changed config: %#v", h.cfg)
	}
}

func TestPutUsageBillingSettingsRejectsInvalidValues(t *testing.T) {
	tooManyRules := make([]config.UsagePricingRule, maxUsageSettingsPricingRules+1)
	for index := range tooManyRules {
		tooManyRules[index] = config.UsagePricingRule{InputPerMillion: "1"}
	}

	tests := []struct {
		name    string
		request usageBillingSettingsInput
	}{
		{name: "retention too low", request: usageBillingSettingsInput{RetentionDays: intPointer(0)}},
		{name: "retention too high", request: usageBillingSettingsInput{RetentionDays: intPointer(config.MaxUsageStatisticsRetentionDays + 1)}},
		{name: "too many rules", request: usageBillingSettingsInput{Pricing: &usageBillingSettingsPricingInput{Rules: usagePricingRulesPointer(tooManyRules)}}},
		{name: "long provider", request: usageBillingSettingsInput{Pricing: testUsagePricing(strings.Repeat("x", maxUsageSettingsPatternLength+1), "1")}},
		{name: "pattern control", request: usageBillingSettingsInput{Pricing: testUsagePricing("openai\n", "1")}},
		{name: "long currency", request: usageBillingSettingsInput{Pricing: &usageBillingSettingsPricingInput{Currency: stringPointer(strings.Repeat("X", maxUsageSettingsCurrencyLength+1))}}},
		{name: "version control", request: usageBillingSettingsInput{Pricing: &usageBillingSettingsPricingInput{Version: stringPointer("bad\tversion")}}},
		{name: "negative rate", request: usageBillingSettingsInput{Pricing: testUsagePricing("openai", "-1")}},
		{name: "exponent rate", request: usageBillingSettingsInput{Pricing: testUsagePricing("openai", "1e3")}},
		{name: "signed rate", request: usageBillingSettingsInput{Pricing: testUsagePricing("openai", "+1")}},
		{name: "empty rates", request: usageBillingSettingsInput{Pricing: testUsagePricing("openai", "")}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := &Handler{
				cfg: &config.Config{
					UsageStatisticsRetentionDays: 30,
					UsagePricing: config.UsagePricingConfig{
						Currency: "USD", Version: "v1",
					},
				},
				configFilePath: writeTestConfigFile(t),
			}
			before := usageBillingSettingsSnapshotLocked(h.cfg).Revision
			test.request.ExpectedRevision = stringPointer(before)
			encoded, errMarshal := json.Marshal(test.request)
			if errMarshal != nil {
				t.Fatalf("marshal request: %v", errMarshal)
			}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/usage-billing-settings", strings.NewReader(string(encoded)))
			c.Request.Header.Set("Content-Type", "application/json")
			h.PutUsageBillingSettings(c)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if after := usageBillingSettingsSnapshotLocked(h.cfg).Revision; after != before {
				t.Fatalf("invalid update changed revision: %q -> %q", before, after)
			}
		})
	}
}

func testUsagePricing(provider, inputRate string) *usageBillingSettingsPricingInput {
	rules := []config.UsagePricingRule{{
		Provider: provider, InputPerMillion: inputRate,
	}}
	return &usageBillingSettingsPricingInput{
		Rules: &rules,
	}
}

func intPointer(value int) *int {
	return &value
}

func stringPointer(value string) *string {
	return &value
}

func usagePricingRulesPointer(value []config.UsagePricingRule) *[]config.UsagePricingRule {
	return &value
}
