package management

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAPICallCodexQuotaRecovery(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		url         string
		provider    string
		activeLimit string
		status      int
		setup       func(*testing.T, *coreauth.Manager, *coreauth.Auth)
		wantReset   bool
	}{
		{name: "confirmed quota recovery", wantReset: true},
		{name: "shared quota with sparse model usage", activeLimit: "premium", wantReset: true},
		{name: "shared quota without model usage", activeLimit: "premium", wantReset: true,
			body: `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":false}}`},
		{name: "shared quota cannot override model restriction", activeLimit: "premium",
			body: `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":false},"model_usage":{"gpt-6-sol":{"available":false}}}`},
		{name: "shared quota cannot override missing availability", activeLimit: "premium",
			body: `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":false},"model_usage":{"gpt-6-sol":{}}}`},
		{name: "shared quota cannot override null availability", activeLimit: "premium",
			body: `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":false},"model_usage":{"gpt-6-sol":{"available":null}}}`},
		{name: "shared quota cannot override null model", activeLimit: "premium",
			body: `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":false},"model_usage":{"gpt-6-sol":null}}`},
		{name: "unknown quota pool", activeLimit: "codex_bengalfox"},
		{name: "missing limit reached flag",
			body: `{"account_id":"quota-account","rate_limit":{"allowed":true},"model_usage":{"gpt-6-astra":{"available":true}}}`},
		{name: "null limit reached flag",
			body: `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":null},"model_usage":{"gpt-6-astra":{"available":true}}}`},
		{name: "shared quota still reached", activeLimit: "premium",
			body: `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":true}}`},
		{name: "shared quota without allowed flag", activeLimit: "premium",
			body: `{"account_id":"quota-account","rate_limit":{"limit_reached":false}}`},
		{name: "percentages alone", body: `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0}}}`},
		{name: "model still restricted", body: `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":false},"model_usage":{"gpt-6-astra":{"available":false,"credits_would_enable":true}}}`},
		{name: "only another model available", body: `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":false},"model_usage":{"gpt-6-sol":{"available":true}}}`},
		{name: "wrong account", body: `{"account_id":"other-account","rate_limit":{"allowed":true,"limit_reached":false},"model_usage":{"gpt-6-astra":{"available":true}}}`},
		{name: "generic limit reached", body: `{"account_id":"quota-account","rate_limit":{"allowed":false,"limit_reached":true},"model_usage":{"gpt-6-astra":{"available":true}}}`},
		{name: "malformed response", body: `{"model_usage":`},
		{name: "invalid availability type", body: `{"account_id":"quota-account","rate_limit":{"allowed":true},"model_usage":{"gpt-6-astra":{"available":"true"}}}`},
		{name: "unauthorized upstream", status: http.StatusUnauthorized},
		{name: "upstream unavailable", status: http.StatusServiceUnavailable},
		{name: "unrelated host", url: "https://example.com/backend-api/wham/usage"},
		{name: "unrelated endpoint", url: "https://chatgpt.com/backend-api/codex/models"},
		{name: "other provider", provider: "claude"},
		{name: "operator disabled auth", setup: func(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
			updated, _ := manager.GetByID(auth.ID)
			updated.Disabled, updated.Status = true, coreauth.StatusDisabled
			if _, errUpdate := manager.Update(t.Context(), updated); errUpdate != nil {
				t.Fatalf("disable auth: %v", errUpdate)
			}
		}},
		{name: "disabled sibling model", setup: func(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
			updated, _ := manager.GetByID(auth.ID)
			updated.ModelStates["gpt-6-luna"].Status = coreauth.StatusDisabled
			if _, errUpdate := manager.Update(t.Context(), updated); errUpdate != nil {
				t.Fatalf("disable model: %v", errUpdate)
			}
		}},
		{name: "unconfirmed second quota", setup: func(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
			recordUsageLimit(t, manager, auth.ID, auth.Provider, "gpt-6-sol", false, nil)
		}},
		{name: "all recorded quotas confirmed", wantReset: true,
			body: `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":false},"model_usage":{"gpt-6-astra":{"available":true},"gpt-6-sol":{"available":true}}}`,
			setup: func(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
				recordUsageLimit(t, manager, auth.ID, auth.Provider, "gpt-6-sol", false, nil)
			}},
		{name: "credential observation cannot identify model quota pool", setup: func(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
			recordUsageLimit(t, manager, auth.ID, auth.Provider, "gpt-6-sol", false, nil)
			updated, _ := manager.GetByID(auth.ID)
			updated.Quota.Signals = map[string]string{"X-Codex-Active-Limit": "premium"}
			if _, errUpdate := manager.Update(t.Context(), updated); errUpdate != nil {
				t.Fatalf("update credential observation: %v", errUpdate)
			}
		}},
		{name: "shared quota with unconfirmed additional pool", activeLimit: "premium", setup: func(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
			recordUsageLimit(t, manager, auth.ID, auth.Provider, "gpt-6-luna", false,
				http.Header{"X-Codex-Active-Limit": {"codex_bengalfox"}})
		}},
		{name: "unrelated model failure", setup: func(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
			manager.MarkResult(t.Context(), coreauth.Result{
				AuthID: auth.ID, Provider: auth.Provider, Model: "gpt-6-sol",
				Error: &coreauth.Error{HTTPStatus: http.StatusNotFound, Message: "model not found"},
			})
		}},
		{name: "forced cooldown", setup: func(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
			manager.MarkResult(t.Context(), coreauth.Result{
				AuthID: auth.ID, Provider: auth.Provider, Model: "gpt-6-astra",
				Error: &coreauth.Error{Code: coreauth.ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"type":"usage_limit_reached"}}`},
			})
		}},
		{name: "capacity rejection", setup: func(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
			manager.MarkResult(t.Context(), coreauth.Result{
				AuthID: auth.ID, Provider: auth.Provider, Model: "gpt-6-astra",
				Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"type":"server_error","message":"Selected model is at capacity"}}`},
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := tc.provider
			if provider == "" {
				provider = "codex"
			}
			h, auth, store := newQuotaRecoveryHandler(t, provider)
			if tc.activeLimit != "" {
				if _, _, errReset := h.authManager.ResetQuota(t.Context(), auth.ID); errReset != nil {
					t.Fatalf("reset fixture quota: %v", errReset)
				}
				recordUsageLimit(t, h.authManager, auth.ID, provider, "gpt-6-sol", true,
					http.Header{"X-Codex-Active-Limit": {tc.activeLimit}, "X-Codex-Primary-Used-Percent": {"100"}})
			}
			if tc.setup != nil {
				tc.setup(t, h.authManager, auth)
			}
			before, _ := h.authManager.GetByID(auth.ID)
			body := tc.body
			if body == "" {
				body = recoveredCodexUsage
			}
			status := tc.status
			if status == 0 {
				status = http.StatusOK
			}
			useQuotaTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer quota-token" {
					t.Error("upstream did not receive selected credential")
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
			})
			url := tc.url
			if url == "" {
				url = "https://chatgpt.com/backend-api/wham/usage"
			}
			recorder := callQuotaTestAPI(t, h, auth, url)
			if recorder.Code != http.StatusOK {
				t.Fatalf("API status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
			}
			var response apiCallResponse
			if errDecode := json.Unmarshal(recorder.Body.Bytes(), &response); errDecode != nil {
				t.Fatalf("decode response: %v", errDecode)
			}
			if response.StatusCode != status || response.Body != body {
				t.Fatalf("upstream response changed: status = %d, body = %s", response.StatusCode, response.Body)
			}
			after, _ := h.authManager.GetByID(auth.ID)
			if !tc.wantReset {
				if after.Generation != before.Generation || !reflect.DeepEqual(after.Quota, before.Quota) || !reflect.DeepEqual(after.ModelStates, before.ModelStates) {
					t.Fatal("unconfirmed recovery changed cooldown state")
				}
				return
			}
			if after.Status != coreauth.StatusActive || after.Unavailable || after.Quota.Exceeded || !after.NextRetryAfter.IsZero() {
				t.Fatalf("confirmed recovery remains blocked: status = %s, unavailable = %v, quota = %+v", after.Status, after.Unavailable, after.Quota)
			}
			if cooldowns := coreauth.CooldownSnapshotForAuth(after, after.UpdatedAt); len(cooldowns) != 0 {
				t.Fatalf("cooldowns after recovery = %+v, want none", cooldowns)
			}
			if after.Success != before.Success || after.Failed != before.Failed {
				t.Fatal("quota query changed inference counters")
			}
			for _, model := range []string{"gpt-6-astra", "gpt-6-luna", "gpt-6-sol"} {
				if count := registry.GetGlobalRegistry().GetModelCount(model); count != 1 {
					t.Fatalf("registry model %s count after recovery = %d, want 1", model, count)
				}
			}
			if records, errLoad := store.Load(t.Context()); errLoad != nil || len(records) != 0 {
				t.Fatalf("saved cooldowns after recovery = %+v, error = %v", records, errLoad)
			}
		})
	}
}

func TestAPICallLegacyPreservesCodexQuota(t *testing.T) {
	h, auth, _ := newQuotaRecoveryHandler(t, "codex")
	useQuotaTestUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(recoveredCodexUsage))
	})
	recorder, ctx := newQuotaTestAPICall(t, auth, "https://chatgpt.com/backend-api/wham/usage")
	ctx.Request.URL.Path = "/v0/management/api-call"
	before, _ := h.authManager.GetByID(auth.ID)
	h.APICall(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("API status = %d, want 200", recorder.Code)
	}
	after, _ := h.authManager.GetByID(auth.ID)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("legacy API call changed cooldown state")
	}
}

func TestAPICallCodexQuotaRecoveryPreservesNewerFailure(t *testing.T) {
	h, auth, _ := newQuotaRecoveryHandler(t, "codex")
	started, release := make(chan struct{}), make(chan struct{})
	useQuotaTestUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(recoveredCodexUsage))
	})
	recorder, ctx := newQuotaTestAPICall(t, auth, "https://chatgpt.com/backend-api/wham/usage")
	done := make(chan struct{})
	go func() {
		h.APICall(ctx)
		close(done)
	}()
	select {
	case <-started:
	case <-done:
		t.Fatalf("API call did not reach upstream: %s", recorder.Body.String())
	}
	h.authManager.MarkResult(t.Context(), coreauth.Result{
		AuthID: auth.ID, Provider: "codex", Model: "gpt-6-astra",
		Error: &coreauth.Error{HTTPStatus: http.StatusUnauthorized, Message: "new unauthorized failure"},
	})
	before, _ := h.authManager.GetByID(auth.ID)
	close(release)
	<-done
	if recorder.Code != http.StatusOK {
		t.Fatalf("API status = %d, want 200", recorder.Code)
	}
	after, _ := h.authManager.GetByID(auth.ID)
	if after.Generation != before.Generation || !reflect.DeepEqual(after.ModelStates, before.ModelStates) || !reflect.DeepEqual(after.Quota, before.Quota) {
		t.Fatal("older quota response cleared a newer failure")
	}
}
