package management

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const recoveredUsageFixture = `{
  "five_hour": null,
  "seven_day": {"utilization": 12, "locked_reason": null},
  "seven_day_opus": null,
  "cedar_ember": {"at_limit": false, "cooldown_until": null, "exhausted": []},
  "limits": [{"severity": "normal", "percent": 12}]
}`

func TestClaudeUsageProvesRecovery(t *testing.T) {
	if !claudeUsageProvesRecovery([]byte(recoveredUsageFixture)) {
		t.Fatal("recovered fixture rejected")
	}
	for name, edit := range map[string]func(map[string]any){
		"missing-cedar":        func(v map[string]any) { delete(v, "cedar_ember") },
		"reset-flag-not-quota": func(v map[string]any) { v["seven_day"].(map[string]any)["utilization"] = float64(100) },
		"missing-utilization":  func(v map[string]any) { delete(v["seven_day"].(map[string]any), "utilization") },
		"negative-utilization": func(v map[string]any) { v["seven_day"].(map[string]any)["utilization"] = float64(-1) },
		"string-utilization":   func(v map[string]any) { v["seven_day"].(map[string]any)["utilization"] = "0" },
		"all-null-windows":     func(v map[string]any) { v["seven_day"] = nil },
		"missing-window":       func(v map[string]any) { delete(v, "five_hour") },
		"scoped-limit":         func(v map[string]any) { v["seven_day_opus"] = map[string]any{"utilization": 100.0} },
		"locked":               func(v map[string]any) { v["seven_day"].(map[string]any)["locked_reason"] = "organization" },
		"cedar-at-limit":       func(v map[string]any) { v["cedar_ember"].(map[string]any)["at_limit"] = true },
		"missing-cooldown":     func(v map[string]any) { delete(v["cedar_ember"].(map[string]any), "cooldown_until") },
		"provider-deadline":    func(v map[string]any) { v["cedar_ember"].(map[string]any)["cooldown_until"] = "later" },
		"exhausted":            func(v map[string]any) { v["cedar_ember"].(map[string]any)["exhausted"] = []any{"weekly"} },
		"missing-exhausted":    func(v map[string]any) { delete(v["cedar_ember"].(map[string]any), "exhausted") },
		"missing-limits":       func(v map[string]any) { delete(v, "limits") },
		"empty-limits":         func(v map[string]any) { v["limits"] = []any{} },
		"critical-limit":       func(v map[string]any) { v["limits"] = []any{map[string]any{"severity": "critical", "percent": 0.0}} },
		"full-limit":           func(v map[string]any) { v["limits"] = []any{map[string]any{"severity": "normal", "percent": 100.0}} },
		"unknown-schema":       func(v map[string]any) { v["new_restriction"] = map[string]any{"blocked": true} },
	} {
		t.Run(name, func(t *testing.T) {
			var v map[string]any
			if err := json.Unmarshal([]byte(recoveredUsageFixture), &v); err != nil {
				t.Fatal(err)
			}
			edit(v)
			body, _ := json.Marshal(v)
			if claudeUsageProvesRecovery(body) {
				t.Fatal("ambiguous/exhausted provider usage accepted")
			}
		})
	}
	for _, raw := range []string{"null", "{}", "[]", "{", recoveredUsageFixture + "{}"} {
		if claudeUsageProvesRecovery([]byte(raw)) {
			t.Fatal("invalid shape accepted")
		}
	}
}

type quotaRecoveryDoer func(*http.Request) (*http.Response, error)

func (f quotaRecoveryDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestClaudeQuotaRecoveryTransportFailClosed(t *testing.T) {
	auth := &coreauth.Auth{Metadata: map[string]any{"access_token": "synthetic"}}
	for _, status := range []int{200, 302, 401, 403, 429, 500} {
		h := &Handler{claudeQuotaClient: quotaRecoveryDoer(func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" || r.URL.String() != claudeUsageRecoveryURL ||
				r.Header.Get("Authorization") != "Bearer synthetic" || r.Body != nil {
				t.Fatal("request changed account, spent credits, or called inference")
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(recoveredUsageFixture))}, nil
		})}
		got, _ := h.claudeQuotaRecovered(context.Background(), auth)
		if got != (status == 200) {
			t.Fatalf("unexpected recovery at HTTP %d", status)
		}
	}
	for _, kind := range []string{"network", "retry-after", "age", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			h := &Handler{claudeQuotaClient: quotaRecoveryDoer(func(*http.Request) (*http.Response, error) {
				if kind == "network" {
					return nil, errors.New("secret transport detail")
				}
				header, body := http.Header{}, recoveredUsageFixture
				switch kind {
				case "retry-after":
					header.Set("Retry-After", "60")
				case "age":
					header.Set("Age", "120")
				case "oversized":
					body = strings.Repeat(" ", 512*1024+1)
				}
				return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			if got, _ := h.claudeQuotaRecovered(context.Background(), auth); got {
				t.Fatal("untrustworthy response accepted")
			}
		})
	}
}

func TestReconcileClaudeQuotaHandlerLoopback(t *testing.T) {
	until := time.Now().Add(time.Hour)
	manager := coreauth.NewManager(nil, nil, nil)
	a := &coreauth.Auth{
		ID: "reconcile-handler", Provider: "claude", Status: coreauth.StatusError,
		Metadata:    map[string]any{"access_token": "synthetic", "expired": until.Format(time.RFC3339)},
		Unavailable: true, NextRetryAfter: until,
		LastError: &coreauth.Error{Code: coreauth.ErrorCodeCredentialQuota, HTTPStatus: 429},
		Quota:     coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: until},
	}
	if _, err := manager.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	providerCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		if r.Method != "GET" || r.URL.RequestURI() != "/api/oauth/usage?cedar_ember=1&skip_spend=1" {
			t.Error("unexpected provider request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, recoveredUsageFixture)
	}))
	defer provider.Close()
	h.claudeQuotaClient = quotaRecoveryDoer(func(r *http.Request) (*http.Response, error) {
		// Test-only redirection; the production endpoint is fixed and disallows redirects.
		local, _ := http.NewRequestWithContext(r.Context(), "GET", provider.URL+r.URL.RequestURI(), nil)
		local.Header = r.Header.Clone()
		return provider.Client().Do(local)
	})
	router := gin.New()
	router.POST("/reconcile", h.ReconcileClaudeQuota)
	request := func(index string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/reconcile", strings.NewReader(`{"auth_index":"`+index+`"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(a.ID); rec.Code != 404 {
		t.Fatal("accepted a file ID instead of auth_index")
	}
	index := a.EnsureIndex()
	if rec := request(index); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"reconciled"`) {
		t.Fatalf("reconcile: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request(index); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"unchanged"`) {
		t.Fatalf("repeat: %d %s", rec.Code, rec.Body.String())
	}
	if providerCalls != 1 {
		t.Fatalf("healthy accounts polled: calls=%d", providerCalls)
	}
}
