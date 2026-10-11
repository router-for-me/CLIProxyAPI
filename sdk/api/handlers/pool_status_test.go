package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestSanitizePoolAuthID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
		noAt bool
	}{
		{name: "codex file email", raw: "auths/codex-7541c7ee-vothienduy221183@gmail.com-team.json", want: "codex-7541***-team", noAt: true},
		{name: "absolute path", raw: "/home/user/auths/plain.json", want: "plain"},
		{name: "cds suffix", raw: "auths/cool-account.cds", want: "cool-account"},
		{name: "plain label", raw: "team-a-key", want: "team-a-key"},
		{name: "short email local part", raw: "ab@gmail.com.json", want: "ab***", noAt: true},
		{name: "non-ascii email local part", raw: "user名字@gmail.com", want: "user***", noAt: true},
		{name: "non-ascii email domain", raw: "user@例え.jp", want: "user***", noAt: true},
		{name: "at token without domain", raw: "key@internal", want: "key***", noAt: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizePoolAuthID(tc.raw)
			if got != tc.want {
				t.Fatalf("sanitizePoolAuthID(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if tc.noAt && strings.Contains(got, "@") {
				t.Fatalf("sanitizePoolAuthID(%q) = %q leaks an email", tc.raw, got)
			}
		})
	}
}

func TestPoolStatusHandler(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	now := time.Now()

	manager := coreauth.NewManager(nil, nil, nil)
	ctx := context.Background()
	auths := []*coreauth.Auth{
		{ID: "codex-7541c7ee-vothienduy221183@gmail.com-team", Provider: "codex"},
		{
			ID: "codex-cool-quotauser9000@gmail.com", Provider: "codex",
			Unavailable: true, NextRetryAfter: now.Add(10 * time.Minute),
			LastError: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests},
		},
		{ID: "codex-disabled-user@gmail.com", Provider: "codex", Disabled: true},
	}
	for _, a := range auths {
		if _, err := manager.Register(ctx, a); err != nil {
			t.Fatalf("Register(%s): %v", a.ID, err)
		}
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/pool/status", nil)
	handler.PoolStatus(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("PoolStatus status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "@gmail.com") || strings.Contains(body, "vothienduy221183") {
		t.Fatalf("pool status leaks credential identity: %s", body)
	}
	if !strings.Contains(body, "***") {
		t.Fatalf("pool status missing masked identifier: %s", body)
	}

	var payload struct {
		Timestamp string `json:"timestamp"`
		Version   string `json:"version"`
		Providers map[string]struct {
			Total            int     `json:"total"`
			PrimaryTotal     int     `json:"primary_total"`
			PrimaryOpen      int     `json:"primary_open"`
			PrimaryCooling   int     `json:"primary_cooling"`
			LastResortOpen   int     `json:"last_resort_open"`
			CoolingRatio     float64 `json:"cooling_ratio"`
			Status           string  `json:"status"`
			EarliestRecovery *string `json:"earliest_recovery"`
			Accounts         []struct {
				AuthID   string  `json:"auth_id"`
				Status   string  `json:"status"`
				Reason   *string `json:"reason"`
				ResetsAt *string `json:"resets_at"`
			} `json:"accounts"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode pool status: %v", err)
	}
	if payload.Timestamp == "" || payload.Version == "" {
		t.Fatal("pool status missing timestamp or version")
	}
	codex, ok := payload.Providers["codex"]
	if !ok {
		t.Fatalf("providers missing codex entry: %s", body)
	}
	if codex.Total != 3 || codex.PrimaryTotal != 3 {
		t.Fatalf("codex totals = %d/%d, want 3/3", codex.Total, codex.PrimaryTotal)
	}
	if codex.PrimaryOpen != 1 || codex.PrimaryCooling != 1 || codex.LastResortOpen != 0 {
		t.Fatalf("codex open/cooling = %d/%d/%d, want 1/1/0", codex.PrimaryOpen, codex.PrimaryCooling, codex.LastResortOpen)
	}
	if codex.Status != "degraded" {
		t.Fatalf("codex status = %q, want degraded", codex.Status)
	}
	if codex.EarliestRecovery == nil {
		t.Fatal("codex earliest_recovery should be set while a credential cools")
	}
	statuses := make(map[string]int)
	for _, account := range codex.Accounts {
		statuses[account.Status]++
		if strings.Contains(account.AuthID, "@") {
			t.Fatalf("account auth_id leaks email: %q", account.AuthID)
		}
	}
	if statuses["open"] != 1 || statuses["cooling"] != 1 || statuses["disabled"] != 1 {
		t.Fatalf("account statuses = %v, want one each of open/cooling/disabled", statuses)
	}
}

func TestPoolStatusDegradedWithTerminallyUnavailable(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	ctx := context.Background()
	auths := []*coreauth.Auth{
		{ID: "open-1", Provider: "codex"},
		{ID: "dead-1", Provider: "codex", Unavailable: true},
	}
	for _, a := range auths {
		if _, err := manager.Register(ctx, a); err != nil {
			t.Fatalf("Register %s: %v", a.ID, err)
		}
	}

	handler := &BaseAPIHandler{AuthManager: manager}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/pool/status", nil)
	handler.PoolStatus(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var resp map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	providers, _ := resp["providers"].(map[string]any)
	codex, _ := providers["codex"].(map[string]any)
	if got := codex["status"]; got != "degraded" {
		t.Fatalf("provider status = %v, want degraded (1 open + 1 terminally unavailable)", got)
	}
}

func TestBuildPoolStatusAccountUntimedBlocks(t *testing.T) {
	t.Parallel()

	now := time.Now()
	cases := []struct {
		name        string
		auth        *coreauth.Auth
		wantStatus  string
		wantReason  string
		wantBlocked []string
	}{
		{
			name: "terminal unauthorized",
			auth: &coreauth.Auth{
				ID: "u1", Unavailable: true, Status: coreauth.StatusError,
				LastError: &coreauth.Error{HTTPStatus: http.StatusUnauthorized},
			},
			wantStatus: "unavailable",
			wantReason: "unauthorized",
		},
		{
			name: "expired access token",
			auth: &coreauth.Auth{
				ID:       "u2",
				Metadata: map[string]any{"access_token": "opaque-token", "expired": now.Add(-time.Hour).UTC().Format(time.RFC3339)},
			},
			wantStatus: "unavailable",
			wantReason: "expired_token",
		},
		{
			name:       "permanent unavailable",
			auth:       &coreauth.Auth{ID: "u3", Unavailable: true},
			wantStatus: "unavailable",
			wantReason: "unavailable",
		},
		{
			name:       "timed cooldown stays cooling",
			auth:       &coreauth.Auth{ID: "u4", Unavailable: true, NextRetryAfter: now.Add(time.Hour)},
			wantStatus: "cooling",
		},
		{
			name:       "healthy stays open",
			auth:       &coreauth.Auth{ID: "u5"},
			wantStatus: "open",
		},
		{
			name: "all models untimed blocked",
			auth: &coreauth.Auth{ID: "u6", ModelStates: map[string]*coreauth.ModelState{
				"gpt-5": {Unavailable: true},
				"gpt-4": {Unavailable: true},
			}},
			wantStatus:  "unavailable",
			wantReason:  "unavailable",
			wantBlocked: []string{"gpt-4", "gpt-5"},
		},
		{
			name: "partial model untimed blocks stay open",
			auth: &coreauth.Auth{ID: "u7", ModelStates: map[string]*coreauth.ModelState{
				"gpt-5": {Unavailable: true},
				"gpt-4": {},
			}},
			wantStatus:  "open",
			wantBlocked: []string{"gpt-5"},
		},
		{
			name: "timed model cooldown stays cooling",
			auth: &coreauth.Auth{ID: "u8", ModelStates: map[string]*coreauth.ModelState{
				"gpt-5": {Unavailable: true, NextRetryAfter: now.Add(time.Hour)},
			}},
			wantStatus: "cooling",
		},
		{
			name: "partial model timed cooldown stays open",
			auth: &coreauth.Auth{ID: "u9", ModelStates: map[string]*coreauth.ModelState{
				"gpt-5": {Unavailable: true, NextRetryAfter: now.Add(time.Hour)},
				"gpt-4": {},
			}},
			wantStatus: "open",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := buildPoolStatusAccount(tc.auth, now, nil)
			if account.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", account.Status, tc.wantStatus)
			}
			if tc.wantReason != "" && (account.Reason == nil || *account.Reason != tc.wantReason) {
				got := ""
				if account.Reason != nil {
					got = *account.Reason
				}
				t.Fatalf("reason = %q, want %q", got, tc.wantReason)
			}
			if len(tc.wantBlocked) > 0 && !slices.Equal(account.ModelsBlocked, tc.wantBlocked) {
				t.Fatalf("models_blocked = %v, want %v", account.ModelsBlocked, tc.wantBlocked)
			}
			if len(tc.wantBlocked) == 0 && len(account.ModelsBlocked) > 0 {
				t.Fatalf("models_blocked = %v, want none", account.ModelsBlocked)
			}
		})
	}
}

func TestPoolStatusDisambiguatesSharedLabels(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	ctx := context.Background()
	auths := []*coreauth.Auth{
		{ID: "gemini:apikey:aaa111bbb222", Provider: "gemini", Label: "gemini-apikey"},
		{ID: "gemini:apikey:ccc333ddd444", Provider: "gemini", Label: "gemini-apikey"},
		{ID: "store/dir1/key.json", Provider: "gemini", Label: "shared"},
		{ID: "store/dir2/key.json", Provider: "gemini", Label: "shared"},
		{ID: "claude:apikey:eee555fff666", Provider: "claude", Label: "claude-apikey"},
	}
	for _, a := range auths {
		if _, err := manager.Register(ctx, a); err != nil {
			t.Fatalf("Register(%s): %v", a.ID, err)
		}
	}

	handler := &BaseAPIHandler{AuthManager: manager}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/pool/status", nil)
	handler.PoolStatus(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var resp struct {
		Providers map[string]struct {
			Accounts []struct {
				AuthID string `json:"auth_id"`
			} `json:"accounts"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	gemini, ok := resp.Providers["gemini"]
	if !ok || len(gemini.Accounts) != 4 {
		t.Fatalf("gemini accounts = %d, want 4", len(gemini.Accounts))
	}
	seen := make(map[string]int, len(gemini.Accounts))
	var apikeyIDs, hashSuffixIDs int
	for _, account := range gemini.Accounts {
		seen[account.AuthID]++
		if strings.HasPrefix(account.AuthID, "gemini:apikey:") {
			apikeyIDs++
		}
		if strings.Contains(account.AuthID, "#") {
			hashSuffixIDs++
		}
	}
	if len(seen) != len(gemini.Accounts) {
		t.Fatalf("gemini auth_ids not unique: %v", seen)
	}
	if apikeyIDs != 2 {
		t.Fatalf("gemini apikey auth_ids = %d, want 2 unique auth IDs: %v", apikeyIDs, seen)
	}
	if hashSuffixIDs != 2 {
		t.Fatalf("same-basename auth_ids = %d, want 2 hash-suffixed: %v", hashSuffixIDs, seen)
	}
	claude, ok := resp.Providers["claude"]
	if !ok || len(claude.Accounts) != 1 {
		t.Fatalf("claude accounts = %d, want 1", len(claude.Accounts))
	}
	if claude.Accounts[0].AuthID != "claude-apikey" {
		t.Fatalf("claude auth_id = %q, want claude-apikey (unique label kept)", claude.Accounts[0].AuthID)
	}
}

func TestPoolStatusHandlerNoManager(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/pool/status", nil)
	handler.PoolStatus(c)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("PoolStatus without manager = %d, want 503", recorder.Code)
	}
}

func TestPoolStatusDegradedWithPartialModelCooling(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	now := time.Now()
	manager := coreauth.NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &coreauth.Auth{
		ID:       "codex-cred-1",
		Provider: "codex",
		ModelStates: map[string]*coreauth.ModelState{
			"gpt-5": {Unavailable: true, NextRetryAfter: now.Add(time.Hour)},
			"gpt-4": {},
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	handler := &BaseAPIHandler{AuthManager: manager}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/pool/status", nil)
	handler.PoolStatus(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var resp struct {
		Providers map[string]struct {
			Status           string  `json:"status"`
			PrimaryOpen      int     `json:"primary_open"`
			PrimaryCooling   int     `json:"primary_cooling"`
			EarliestRecovery *string `json:"earliest_recovery"`
			Accounts         []struct {
				AuthID        string            `json:"auth_id"`
				Status        string            `json:"status"`
				ModelsCooling map[string]string `json:"models_cooling"`
			} `json:"accounts"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	codex, ok := resp.Providers["codex"]
	if !ok {
		t.Fatal("missing codex provider")
	}
	if codex.Status != "degraded" {
		t.Fatalf("provider status = %q, want degraded", codex.Status)
	}
	if codex.PrimaryOpen != 1 || codex.PrimaryCooling != 0 {
		t.Fatalf("open/cooling = %d/%d, want 1/0", codex.PrimaryOpen, codex.PrimaryCooling)
	}
	if codex.EarliestRecovery == nil {
		t.Fatal("earliest_recovery should be set for partial model cooldown")
	}
	if len(codex.Accounts) != 1 || codex.Accounts[0].Status != "open" {
		t.Fatalf("account status = %v, want open", codex.Accounts)
	}
	if _, ok := codex.Accounts[0].ModelsCooling["gpt-5"]; !ok {
		t.Fatalf("account missing gpt-5 in models_cooling: %v", codex.Accounts[0].ModelsCooling)
	}
}

func TestPoolStatusPreservesCrossProviderLabels(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	ctx := context.Background()
	auths := []*coreauth.Auth{
		{ID: "openai-key-1", Provider: "openai", Label: "prod"},
		{ID: "claude-key-1", Provider: "claude", Label: "prod"},
	}
	for _, a := range auths {
		if _, err := manager.Register(ctx, a); err != nil {
			t.Fatalf("Register(%s): %v", a.ID, err)
		}
	}

	handler := &BaseAPIHandler{AuthManager: manager}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/pool/status", nil)
	handler.PoolStatus(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var resp struct {
		Providers map[string]struct {
			Accounts []struct {
				AuthID string `json:"auth_id"`
			} `json:"accounts"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	openai, ok := resp.Providers["openai"]
	if !ok || len(openai.Accounts) != 1 {
		t.Fatalf("openai accounts = %v", resp.Providers["openai"])
	}
	if openai.Accounts[0].AuthID != "prod" {
		t.Fatalf("openai auth_id = %q, want prod", openai.Accounts[0].AuthID)
	}
	claude, ok := resp.Providers["claude"]
	if !ok || len(claude.Accounts) != 1 {
		t.Fatalf("claude accounts = %v", resp.Providers["claude"])
	}
	if claude.Accounts[0].AuthID != "prod" {
		t.Fatalf("claude auth_id = %q, want prod", claude.Accounts[0].AuthID)
	}
}
