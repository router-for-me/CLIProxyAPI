package management

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const codexRecoveryFixture = `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":12,"limit_window_seconds":18000},"secondary_window":null},"additional_rate_limits":[],"code_review_rate_limit":null}`
const antigravityRecoveryFixture = `{"groups":[{"displayName":"Plan","buckets":[{"window":"weekly","remainingFraction":0.8}]}]}`
const antigravityModelsRecoveryFixture = `{"models":{"gemini-test":{"quotaInfo":{"remainingFraction":0.8}}}}`
const kimiRecoveryFixture = `{"usage":{"used":10,"limit":100,"remaining":90},"limits":[{"detail":{"used":1,"limit":10,"remaining":9}}]}`

func TestProviderUsageProvesRecovery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		parse func([]byte) bool
	}{
		{"codex", codexRecoveryFixture, codexUsageProvesRecovery},
		{"antigravity", antigravityRecoveryFixture, antigravitySummaryRecovered},
		{"kimi", kimiRecoveryFixture, kimiUsageProvesRecovery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.parse([]byte(tc.body)) {
				t.Fatal("valid recovery evidence rejected")
			}
			for _, body := range []string{"{}", "null", "[]", "garbage"} {
				if tc.parse([]byte(body)) {
					t.Fatal("empty/invalid evidence accepted")
				}
			}
			var value map[string]any
			_ = json.Unmarshal([]byte(tc.body), &value)
			value["unknown_restriction"] = map[string]any{"utilization": 1.0, "blocked": true}
			raw, _ := json.Marshal(value)
			if tc.parse(raw) {
				t.Fatal("unknown blocker accepted")
			}
		})
	}
	for _, body := range []string{
		strings.Replace(codexRecoveryFixture, `"allowed":true`, `"allowed":false`, 1),
		strings.Replace(codexRecoveryFixture, `"limit_reached":false`, `"limit_reached":true`, 1),
		strings.Replace(codexRecoveryFixture, `"used_percent":12`, `"used_percent":100`, 1),
		strings.Replace(codexRecoveryFixture, `"additional_rate_limits":[]`, `"additional_rate_limits":[{"rate_limit":{"allowed":false}}]`, 1),
		strings.Replace(codexRecoveryFixture, `"code_review_rate_limit":null`, `"code_review_rate_limit":{"allowed":false}`, 1),
		strings.Replace(codexRecoveryFixture, `"additional_rate_limits":[]`, `"credits":{"unlimited":true}`, 1),
	} {
		if codexUsageProvesRecovery([]byte(body)) {
			t.Fatal("scoped/exhausted/missing Codex quota accepted")
		}
	}
	for _, fraction := range []string{"0", "-1", "1.1", "null", `"0.8"`} {
		if antigravitySummaryRecovered([]byte(strings.ReplaceAll(antigravityRecoveryFixture, "0.8", fraction))) {
			t.Fatal("ambiguous Antigravity fraction accepted")
		}
	}
	for _, body := range []string{
		strings.Replace(kimiRecoveryFixture, `"used":10`, `"used":100`, 1),
		strings.Replace(kimiRecoveryFixture, `"remaining":9`, `"remaining":0`, 1),
		strings.Replace(kimiRecoveryFixture, `"remaining":90`, `"remaining":99`, 1),
		`{"usage":null,"limits":[],"usages":{"limit_month_total":{"used_ratio":1}}}`,
	} {
		if kimiUsageProvesRecovery([]byte(body)) {
			t.Fatal("exhausted or contradictory Kimi usage accepted")
		}
	}
	if !kimiUsageProvesRecovery([]byte(`{"usage":{"used":"10","limit":"100","remaining":"90"},"limits":[]}`)) {
		t.Fatal("documented Kimi numeric strings rejected")
	}
	for _, bad := range []string{"NaN", "Inf", "invalid", "-1"} {
		body := `{"usage":{"used":"` + bad + `","limit":"100","remaining":"90"},"limits":[]}`
		if kimiUsageProvesRecovery([]byte(body)) {
			t.Fatal("invalid Kimi numeric evidence accepted")
		}
	}
}

func providerReconcileAuth(provider string) *coreauth.Auth {
	deadline := time.Now().Add(time.Hour)
	return &coreauth.Auth{
		ID: "quota-" + provider, Provider: provider, Status: coreauth.StatusError,
		Metadata: map[string]any{"access_token": "synthetic-" + provider, "expired": deadline.Format(time.RFC3339),
			"account_id": "test-account", "project_id": "test-project"},
		Quota: coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: deadline},
		ModelStates: map[string]*coreauth.ModelState{"gemini-test": {
			Unavailable: true, NextRetryAfter: deadline,
			Quota: coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline},
		}},
	}
}

func TestQuotaRecoveryProviderDispatch(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "antigravity", "kimi"} {
		t.Run(provider, func(t *testing.T) {
			auth := providerReconcileAuth(provider)
			calls := 0
			h := &Handler{quotaRecoveryClient: quotaRecoveryDoer(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("Authorization") != "Bearer synthetic-"+provider {
					t.Fatal("wrong account token")
				}
				body := ""
				switch provider {
				case "claude":
					if req.Method != "GET" || req.URL.String() != claudeUsageRecoveryURL {
						t.Fatal("unexpected Claude route")
					}
					body = recoveredUsageFixture
				case "codex":
					if req.Method != "GET" || req.URL.String() != codexUsageRecoveryURL ||
						req.Header.Get("ChatGPT-Account-Id") != "test-account" {
						t.Fatal("wrong Codex account/route")
					}
					body = codexRecoveryFixture
				case "antigravity":
					if req.Method != "POST" || req.URL.Host != "daily-cloudcode-pa.googleapis.com" {
						t.Fatal("unexpected Google route")
					}
					raw, _ := io.ReadAll(req.Body)
					if string(raw) != `{"project":"test-project"}` {
						t.Fatal("wrong Google project")
					}
					switch req.URL.Path {
					case "/v1internal:retrieveUserQuotaSummary":
						body = antigravityRecoveryFixture
					case "/v1internal:fetchAvailableModels":
						body = antigravityModelsRecoveryFixture
					default:
						t.Fatal("inference route used")
					}
				case "kimi":
					if req.Method != "GET" || req.URL.String() != "https://api.kimi.com/coding/v1/usages" {
						t.Fatal("wrong Kimi route")
					}
					body = kimiRecoveryFixture
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			manager := coreauth.NewManager(nil, nil, nil)
			_, _ = manager.Register(context.Background(), auth)
			other := providerReconcileAuth("other")
			_, _ = manager.Register(context.Background(), other)
			got, _, err := manager.ReconcileQuota(context.Background(), auth.ID, h.quotaRecoveryAdapter(provider))
			if err != nil || got == nil || got.Quota.Exceeded {
				t.Fatalf("provider %s did not reconcile: %v", provider, err)
			}
			wantCalls := 1
			if provider == "antigravity" {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatal("unexpected extra provider calls")
			}
			otherAfter, _ := manager.GetByID(other.ID)
			if !otherAfter.Quota.Exceeded {
				t.Fatal("healthy evidence cleared another account")
			}
		})
	}
}

func TestQuotaRecoveryAntigravityModelCoverage(t *testing.T) {
	auth := providerReconcileAuth("antigravity")
	for _, body := range []string{
		`{"models":{}}`,
		`{"models":{"gemini-test-disabled":{"quotaInfo":{"remainingFraction":1}}}}`,
		`{"models":{"gemini-test":{"quotaInfo":{"remainingFraction":0}}}}`,
		`{"models":{"gemini-test":{"quotaInfo":{"remainingFraction":1},"blocked":true}}}`,
		`{"models":{"gemini-test":{"disabled":true,"quotaInfo":{"remainingFraction":1}}}}`,
	} {
		if antigravityModelsRecovered([]byte(body), auth) {
			t.Fatal("missing/disabled/exhausted model accepted")
		}
	}
}

func TestQuotaRecoveryCodexIdentity(t *testing.T) {
	for _, mode := range []string{"missing", "different", "matching"} {
		t.Run(mode, func(t *testing.T) {
			a := providerReconcileAuth("codex")
			if mode == "missing" {
				delete(a.Metadata, "account_id")
			}
			calls := 0
			h := &Handler{quotaRecoveryClient: quotaRecoveryDoer(func(*http.Request) (*http.Response, error) {
				calls++
				account := "test-account"
				if mode == "different" {
					account = "another-account"
				}
				body := strings.Replace(codexRecoveryFixture, "{", `{"account_id":"`+account+`",`, 1)
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			recovered, err := h.codexQuotaRecovered(context.Background(), a)
			if err != nil || recovered != (mode == "matching") {
				t.Fatal("Codex account binding violated")
			}
			if mode == "missing" && calls != 0 {
				t.Fatal("implicit workspace was queried")
			}
		})
	}
}

func TestQuotaRecoveryNativeTransports(t *testing.T) {
	h := &Handler{}
	for _, provider := range []string{"claude", "codex", "antigravity", "kimi"} {
		client, err := h.quotaRecoveryHTTPClient(context.Background(), providerReconcileAuth(provider))
		if err != nil {
			t.Fatal(err)
		}
		if client.Timeout != 0 || client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
			t.Fatal("provider client changed timeout or redirect policy")
		}
		if provider == "claude" && !strings.Contains(reflect.TypeOf(client.Transport).String(), "claude.") {
			t.Fatal("Claude OAuth reverted to generic TLS")
		}
		client.CloseIdleConnections()
	}
}

func TestQuotaRecoveryCapabilitiesAndUnsupported(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	h.quotaRecoveryClient = quotaRecoveryDoer(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported provider generated a provider request")
		return nil, nil
	})
	for _, provider := range []string{"xai", "gemini-cli", "gemini", "unknown-plugin"} {
		if h.quotaRecoveryAdapter(provider) != nil {
			t.Fatal("unsupported provider guessed recovery")
		}
		a := providerReconcileAuth(provider)
		_, _ = manager.Register(context.Background(), a)
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest("POST", "/reconcile", strings.NewReader(`{"auth_index":"`+a.EnsureIndex()+`"}`))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.ReconcileQuota(ctx)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "unsupported_provider") {
			t.Fatal(rec.Body.String())
		}
		after, _ := manager.GetByID(a.ID)
		if !after.Quota.Exceeded {
			t.Fatal("unsupported cooldown was reset")
		}
	}
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	h.SetConfig(cfg)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	h.QuotaReconcileCapabilities(ctx)
	if !strings.Contains(rec.Body.String(), "home_unsupported") {
		t.Fatal("Home falsely advertised local support")
	}
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest("POST", "/reconcile", strings.NewReader(`{}`))
	h.ReconcileQuota(ctx)
	if rec.Code != 409 {
		t.Fatal("Home mode permitted local mutation")
	}
}

func TestQuotaRecoveryCodexPartition(t *testing.T) {
	a := providerReconcileAuth("codex")
	a.Metadata["use_siwc"] = true
	a.Metadata["siwc"] = map[string]any{"access_token": "nested", "account_id": "other", "expired": a.Metadata["expired"]}
	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(context.Background(), a)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	calls := 0
	h.quotaRecoveryClient = quotaRecoveryDoer(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(codexRecoveryFixture))}, nil
	})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest("POST", "/reconcile", strings.NewReader(`{"auth_index":"`+a.EnsureIndex()+`"}`))
	h.ReconcileQuota(ctx)
	after, _ := manager.GetByID(a.ID)
	if !strings.Contains(rec.Body.String(), "unsupported_credential") || calls != 0 || !after.Quota.Exceeded || coreauth.CanReconcileQuota(a, time.Now()) {
		t.Fatalf("ambiguous partition: status=%s calls=%d blocked=%v", rec.Body.String(), calls, after.Quota.Exceeded)
	}
}

func TestQuotaRecoveryAntigravityResponseLimits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		catalog bool
		size    int
		want    bool
	}{{"large catalog", true, 512<<10 + 1, true}, {"oversized catalog", true, 8<<20 + 1, false}, {"oversized summary", false, 512<<10 + 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			a := providerReconcileAuth("antigravity")
			h := &Handler{quotaRecoveryClient: quotaRecoveryDoer(func(req *http.Request) (*http.Response, error) {
				catalog := strings.HasSuffix(req.URL.Path, ":fetchAvailableModels")
				body := antigravityRecoveryFixture
				if catalog {
					body = antigravityModelsRecoveryFixture
				}
				if catalog == tc.catalog {
					body += strings.Repeat(" ", tc.size-len(body))
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			got, _ := h.antigravityQuotaRecovered(context.Background(), a)
			if got != tc.want {
				t.Fatalf("recovered=%v want %v", got, tc.want)
			}
		})
	}
}
