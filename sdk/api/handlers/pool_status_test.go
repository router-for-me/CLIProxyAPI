package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
