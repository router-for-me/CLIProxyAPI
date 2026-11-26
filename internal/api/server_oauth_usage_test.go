package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func registerOAuthUsageAuth(t *testing.T, server *Server, a *auth.Auth) {
	t.Helper()
	a.Attributes = map[string]string{"runtime_only": "true"}
	a.Metadata = map[string]any{"type": a.Provider}
	if a.Status == "" {
		a.Status = auth.StatusActive
	}
	if _, errRegister := server.handlers.AuthManager.Register(auth.WithSkipPersist(context.Background()), a); errRegister != nil {
		t.Fatalf("Register(%s): %v", a.ID, errRegister)
	}
}

func claudeUsageSignals(fiveHourReset, weeklyReset time.Time, fiveHourUsed, weeklyUsed string) map[string]string {
	return map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(fiveHourReset.Unix(), 10),
		"Anthropic-Ratelimit-Unified-5h-Utilization": fiveHourUsed,
		"Anthropic-Ratelimit-Unified-5h-Status":      "allowed",
		"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(weeklyReset.Unix(), 10),
		"Anthropic-Ratelimit-Unified-7d-Utilization": weeklyUsed,
		"Anthropic-Ratelimit-Unified-7d-Status":      "allowed",
	}
}

func getOAuthUsage(t *testing.T, server *Server, apiKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/usage", nil)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)
	return rec
}

func TestOAuthUsageRequiresClientAPIKey(t *testing.T) {
	server := newTestServer(t)
	for _, key := range []string{"", "wrong-key"} {
		if rec := getOAuthUsage(t, server, key); rec.Code != http.StatusUnauthorized {
			t.Fatalf("key %q: status = %d, want 401; body=%s", key, rec.Code, rec.Body.String())
		}
	}
}

func TestOAuthUsageWithoutQuotaDataReturnsNullWindows(t *testing.T) {
	server := newTestServer(t)
	// A Claude credential that has not served a request yet and a non-Claude
	// credential with quota data must both be ignored.
	registerOAuthUsageAuth(t, server, &auth.Auth{ID: "claude-fresh", Provider: "claude"})
	now := time.Now()
	registerOAuthUsageAuth(t, server, &auth.Auth{ID: "codex-a", Provider: "codex", Quota: auth.QuotaState{
		ObservedAt: now,
		Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "50", "X-Codex-Primary-Window-Minutes": "300"},
	}})

	rec := getOAuthUsage(t, server, "test-key")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	for _, key := range []string{"five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet"} {
		value := ojson.GetBytesNoCopy(body, key)
		if !value.Exists() || value.Type != ojson.Null {
			t.Fatalf("%s = %s, want null; body=%s", key, value.Raw, body)
		}
	}
}

func TestOAuthUsageReportsPreferredClaudeCredential(t *testing.T) {
	server := newTestServer(t)
	now := time.Now()
	soonWeekly := now.Add(30 * time.Hour).Truncate(time.Second)
	fiveHour := now.Add(2 * time.Hour).Truncate(time.Second)
	registerOAuthUsageAuth(t, server, &auth.Auth{ID: "claude-late", Provider: "claude", Quota: auth.QuotaState{
		ObservedAt: now, Signals: claudeUsageSignals(now.Add(time.Hour), now.Add(5*24*time.Hour), "0.9", "0.9"),
	}})
	registerOAuthUsageAuth(t, server, &auth.Auth{ID: "claude-soon", Provider: "claude", Quota: auth.QuotaState{
		ObservedAt: now.Add(-time.Minute), Signals: claudeUsageSignals(fiveHour, soonWeekly, "0.08", "0.4125"),
	}})
	// Disabled credentials never count, even with the soonest reset.
	registerOAuthUsageAuth(t, server, &auth.Auth{ID: "claude-disabled", Provider: "claude", Disabled: true, Status: auth.StatusDisabled, Quota: auth.QuotaState{
		ObservedAt: now, Signals: claudeUsageSignals(now.Add(time.Hour), now.Add(2*time.Hour), "0.5", "0.5"),
	}})

	rec := getOAuthUsage(t, server, "test-key")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if got := ojson.GetBytesNoCopy(body, "five_hour.utilization").Float(); got != 8 {
		t.Fatalf("five_hour.utilization = %v, want 8; body=%s", got, body)
	}
	if got := ojson.GetBytesNoCopy(body, "five_hour.resets_at").String(); got != fiveHour.UTC().Format(time.RFC3339) {
		t.Fatalf("five_hour.resets_at = %q, want %q", got, fiveHour.UTC().Format(time.RFC3339))
	}
	if got := ojson.GetBytesNoCopy(body, "seven_day.utilization").Float(); got != 41.25 {
		t.Fatalf("seven_day.utilization = %v, want 41.25; body=%s", got, body)
	}
	if got := ojson.GetBytesNoCopy(body, "seven_day.resets_at").String(); got != soonWeekly.UTC().Format(time.RFC3339) {
		t.Fatalf("seven_day.resets_at = %q, want %q", got, soonWeekly.UTC().Format(time.RFC3339))
	}
	if value := ojson.GetBytesNoCopy(body, "seven_day_opus"); value.Type != ojson.Null {
		t.Fatalf("seven_day_opus = %s, want null", value.Raw)
	}
}

func TestOAuthUsageAuthFallsBackToNewestSnapshot(t *testing.T) {
	now := time.Now()
	// Weekly resets already passed, so no credential is preferred by
	// soonest-reset; the newest snapshot wins and the elapsed windows read 0.
	older := &auth.Auth{ID: "claude-a", Provider: "claude", Status: auth.StatusActive, Quota: auth.QuotaState{
		ObservedAt: now.Add(-2 * time.Hour), Signals: claudeUsageSignals(now.Add(-time.Hour), now.Add(-time.Minute), "0.7", "0.7"),
	}}
	newer := &auth.Auth{ID: "claude-b", Provider: "claude", Status: auth.StatusActive, Quota: auth.QuotaState{
		ObservedAt: now.Add(-time.Hour), Signals: claudeUsageSignals(now.Add(-time.Minute), now.Add(-time.Minute), "0.6", "0.3"),
	}}
	got := oauthUsageAuth([]*auth.Auth{older, newer}, now)
	if got == nil || got.ID != "claude-b" {
		t.Fatalf("oauthUsageAuth = %v, want claude-b", got)
	}
	payload := oauthUsagePayload([]*auth.Auth{older, newer}, now)
	window, ok := payload["five_hour"].(gin.H)
	if !ok {
		t.Fatalf("five_hour = %#v", payload["five_hour"])
	}
	if window["utilization"] != 0.0 || window["resets_at"] != nil {
		t.Fatalf("elapsed five_hour = %#v, want utilization 0 and no reset", window)
	}
}

func TestOAuthUsageAuthIgnoresStatusOnlySnapshot(t *testing.T) {
	now := time.Now()
	// No weekly reset is known, so the fallback decides; a newer snapshot that
	// only carries status headers has nothing to show and must not win.
	withData := &auth.Auth{ID: "claude-a", Provider: "claude", Status: auth.StatusActive, Quota: auth.QuotaState{
		ObservedAt: now.Add(-time.Hour), Signals: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.2"},
	}}
	statusOnly := &auth.Auth{ID: "claude-b", Provider: "claude", Status: auth.StatusActive, Quota: auth.QuotaState{
		ObservedAt: now, Signals: map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "allowed"},
	}}
	if got := oauthUsageAuth([]*auth.Auth{withData, statusOnly}, now); got == nil || got.ID != "claude-a" {
		t.Fatalf("oauthUsageAuth = %v, want claude-a", got)
	}
	if window, ok := oauthUsagePayload([]*auth.Auth{statusOnly}, now)["five_hour"]; !ok || window != nil {
		t.Fatalf("status-only five_hour = %#v, want nil", window)
	}
}
