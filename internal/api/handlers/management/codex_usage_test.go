package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestCodexUsagePollIntervalMatchesCodexCLI(t *testing.T) {
	if codexUsagePollInterval != 60*time.Second {
		t.Fatalf("expected poll interval 60s, got %s", codexUsagePollInterval)
	}
}

func TestCodexPlanWeight_FreeIsPointTwo(t *testing.T) {
	if got := codexPlanWeight("free"); got != 0.2 {
		t.Fatalf("expected free plan weight 0.2, got %v", got)
	}
	if got := codexPlanWeight("business"); got != 1.0 {
		t.Fatalf("expected business plan weight 1.0, got %v", got)
	}
}

func TestAggregateCodexUsage_AppliesFreeWeight(t *testing.T) {
	statuses := map[string]codexAuthUsageStatus{
		"free-auth": {
			Status: "ok",
			Usage: &codexUsagePayload{
				PlanType: "free",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        100,
						LimitWindowSeconds: 18000,
					},
				},
			},
		},
		"business-auth": {
			Status: "ok",
			Usage: &codexUsagePayload{
				PlanType: "business",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        0,
						LimitWindowSeconds: 18000,
					},
				},
			},
		},
	}

	compat, totals, withUsage := aggregateCodexUsage(statuses)
	if withUsage != 2 {
		t.Fatalf("expected withUsage=2, got %d", withUsage)
	}
	if compat.RateLimit == nil || compat.RateLimit.PrimaryWindow == nil {
		t.Fatalf("expected primary window in compat payload")
	}
	if compat.RateLimit.PrimaryWindow.UsedPercent != 17 {
		t.Fatalf("expected weighted used_percent=17, got %d", compat.RateLimit.PrimaryWindow.UsedPercent)
	}
	if totals.PrimaryWindow == nil {
		t.Fatalf("expected primary totals")
	}
	if totals.PrimaryWindow.ProgressPercent != 16.67 {
		t.Fatalf("expected weighted progress 16.67, got %.2f", totals.PrimaryWindow.ProgressPercent)
	}
}

func TestPollSelectedCodexUsageIfDue_PollsOnlySelectedAndPerAuthInterval(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var whamCalls int32
	whamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&whamCalls, 1)
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Fatalf("unexpected wham path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-wham" {
			t.Fatalf("unexpected auth header: %s", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acc-wham" {
			t.Fatalf("unexpected account id header: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(codexUsagePayload{
			PlanType: "plus",
			RateLimit: &codexUsageRateLimit{
				Allowed:      true,
				LimitReached: false,
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        20,
					LimitWindowSeconds: 18000,
					ResetAfterSeconds:  1200,
					ResetAt:            1900000000,
				},
				SecondaryWindow: &codexUsageWindow{
					UsedPercent:        40,
					LimitWindowSeconds: 604800,
					ResetAfterSeconds:  3600,
					ResetAt:            1900003600,
				},
			},
			AdditionalRateLimits: []codexUsageAdditionalRateLimit{
				{
					LimitName:      "messages",
					MeteredFeature: "cloud",
					RateLimit: &codexUsageRateLimit{
						Allowed:      true,
						LimitReached: false,
						PrimaryWindow: &codexUsageWindow{
							UsedPercent:        10,
							LimitWindowSeconds: 86400,
							ResetAfterSeconds:  600,
							ResetAt:            1900000600,
						},
					},
				},
			},
		})
	}))
	defer whamServer.Close()

	var apiCalls int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&apiCalls, 1)
		if r.URL.Path != "/api/codex/usage" {
			t.Fatalf("unexpected codex api path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-api" {
			t.Fatalf("unexpected auth header: %s", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acc-api" {
			t.Fatalf("unexpected account id header: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(codexUsagePayload{
			PlanType: "plus",
			RateLimit: &codexUsageRateLimit{
				Allowed:      false,
				LimitReached: true,
				PrimaryWindow: &codexUsageWindow{
					UsedPercent:        60,
					LimitWindowSeconds: 18000,
					ResetAfterSeconds:  1800,
					ResetAt:            1900001800,
				},
				SecondaryWindow: &codexUsageWindow{
					UsedPercent:        80,
					LimitWindowSeconds: 604800,
					ResetAfterSeconds:  7200,
					ResetAt:            1900007200,
				},
			},
			AdditionalRateLimits: []codexUsageAdditionalRateLimit{
				{
					LimitName:      "messages",
					MeteredFeature: "cloud",
					RateLimit: &codexUsageRateLimit{
						Allowed:      false,
						LimitReached: true,
						PrimaryWindow: &codexUsageWindow{
							UsedPercent:        50,
							LimitWindowSeconds: 86400,
							ResetAfterSeconds:  900,
							ResetAt:            1900000900,
						},
					},
				},
			},
		})
	}))
	defer apiServer.Close()

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	ctx := context.Background()
	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-wham",
		Provider: "codex",
		FileName: "codex-wham.json",
		Attributes: map[string]string{
			"base_url": whamServer.URL + "/backend-api",
		},
		Metadata: map[string]any{
			"access_token": "token-wham",
			"account_id":   "acc-wham",
			"email":        "wham@example.com",
		},
	})
	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-api",
		Provider: "codex",
		FileName: "codex-api.json",
		Attributes: map[string]string{
			"base_url": apiServer.URL,
		},
		Metadata: map[string]any{
			"access_token": "token-api",
			"account_id":   "acc-api",
			"email":        "api@example.com",
		},
	})
	_, _ = manager.Register(ctx, &coreauth.Auth{
		ID:       "codex-apikey",
		Provider: "codex",
		FileName: "codex-apikey.json",
		Attributes: map[string]string{
			"api_key": "sk-test",
		},
	})

	h := &Handler{
		cfg:              &config.Config{},
		authManager:      manager,
		configFilePath:   t.TempDir() + "/config.yaml",
		codexUsageByAuth: make(map[string]codexAuthUsageStatus),
		codexUsageCompat: defaultCodexUsagePayload(),
	}

	manager.SetSelectedAuthID("codex", "codex-wham")
	h.pollSelectedCodexUsageIfDue(context.Background())
	compat, summary, hasData := h.codexUsageSnapshot()

	if !hasData {
		t.Fatal("expected usage data after polling")
	}
	if atomic.LoadInt32(&whamCalls) != 1 {
		t.Fatalf("expected 1 wham call, got %d", whamCalls)
	}
	if atomic.LoadInt32(&apiCalls) != 0 {
		t.Fatalf("expected 0 api calls before selection switch, got %d", apiCalls)
	}
	if summary.SelectedAuthID != "codex-wham" {
		t.Fatalf("expected selected auth codex-wham, got %q", summary.SelectedAuthID)
	}
	if compat.PlanType != "plus" {
		t.Fatalf("expected plan_type plus, got %q", compat.PlanType)
	}
	if compat.RateLimit == nil || compat.RateLimit.PrimaryWindow == nil {
		t.Fatalf("expected aggregated primary window")
	}
	if compat.RateLimit.PrimaryWindow.UsedPercent != 20 {
		t.Fatalf("expected used_percent 20 for selected auth, got %d", compat.RateLimit.PrimaryWindow.UsedPercent)
	}
	if summary.AuthFilesTotal != 1 {
		t.Fatalf("expected only selected auth cached, got %d", summary.AuthFilesTotal)
	}
	if summary.AuthFilesWithUsage != 1 {
		t.Fatalf("expected 1 auth with usage, got %d", summary.AuthFilesWithUsage)
	}

	// Same selected auth within 60s should not poll again.
	h.pollSelectedCodexUsageIfDue(context.Background())
	if atomic.LoadInt32(&whamCalls) != 1 {
		t.Fatalf("expected still 1 wham call due to per-auth interval, got %d", whamCalls)
	}

	// Switch selected auth: first poll for this auth should run immediately.
	manager.SetSelectedAuthID("codex", "codex-api")
	h.pollSelectedCodexUsageIfDue(context.Background())
	if atomic.LoadInt32(&apiCalls) != 1 {
		t.Fatalf("expected 1 api call after selection switch, got %d", apiCalls)
	}

	// Switch back quickly: previous wham poll is still within 60s, should be skipped.
	manager.SetSelectedAuthID("codex", "codex-wham")
	h.pollSelectedCodexUsageIfDue(context.Background())
	if atomic.LoadInt32(&whamCalls) != 1 {
		t.Fatalf("expected wham call count unchanged within interval, got %d", whamCalls)
	}

	// Force wham auth interval to expire, then it should poll again.
	h.codexUsageMu.Lock()
	st := h.codexUsageByAuth["codex-wham"]
	st.LastPolledAt = time.Now().Add(-61 * time.Second).UTC()
	h.codexUsageByAuth["codex-wham"] = st
	h.codexUsageMu.Unlock()
	h.pollSelectedCodexUsageIfDue(context.Background())
	if atomic.LoadInt32(&whamCalls) != 2 {
		t.Fatalf("expected second wham poll after interval expiry, got %d", whamCalls)
	}
}

func TestCodexUsageStatePersistence(t *testing.T) {
	tmp := t.TempDir()
	configPath := tmp + "/config.yaml"
	h := &Handler{
		configFilePath:   configPath,
		codexUsageByAuth: make(map[string]codexAuthUsageStatus),
		codexUsageCompat: defaultCodexUsagePayload(),
	}
	now := time.Now().UTC()
	h.updateCodexUsageState(map[string]codexAuthUsageStatus{
		"codex-a": {
			AuthID:       "codex-a",
			Status:       "ok",
			LastPolledAt: now,
			HasUsage:     true,
			Usage: &codexUsagePayload{
				PlanType: "plus",
				RateLimit: &codexUsageRateLimit{
					PrimaryWindow: &codexUsageWindow{
						UsedPercent:        33,
						LimitWindowSeconds: 18000,
					},
				},
			},
		},
	}, "codex-a", now, true)

	h2 := &Handler{
		configFilePath:   configPath,
		codexUsageByAuth: make(map[string]codexAuthUsageStatus),
		codexUsageCompat: defaultCodexUsagePayload(),
	}
	h2.loadCodexUsageState()
	compat, summary, hasData := h2.codexUsageSnapshot()
	if !hasData {
		t.Fatal("expected persisted usage data after reload")
	}
	if summary.SelectedAuthID != "codex-a" {
		t.Fatalf("expected selected auth codex-a, got %q", summary.SelectedAuthID)
	}
	if compat.RateLimit == nil || compat.RateLimit.PrimaryWindow == nil || compat.RateLimit.PrimaryWindow.UsedPercent != 33 {
		t.Fatalf("unexpected persisted compat payload: %+v", compat)
	}
}

func TestGetCodexUsageCompatDefaultsToGuest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/codex/usage", nil)

	h.GetCodexUsageCompat(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload codexUsagePayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.PlanType != "guest" {
		t.Fatalf("expected plan_type guest, got %q", payload.PlanType)
	}
}
