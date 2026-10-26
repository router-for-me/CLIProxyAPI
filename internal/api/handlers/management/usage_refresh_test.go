package management

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// A polled Claude account must rank on its weekly reset, and keep that rank after
// a restart instead of falling back to "unknown" until it serves a request.
func TestUsageRefreshPollsAndSurvivesRestart(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now()
	weeklyReset := now.Add(72 * time.Hour).Truncate(time.Second)
	fixture := gjson.Parse(fmt.Sprintf(`{"five_hour":{"utilization":12.5,"resets_at":%q},"seven_day":{"utilization":40,"resets_at":%q},
		"limits":[{"kind":"weekly_scoped","group":"weekly","percent":5,"resets_at":%q,"scope":{"model":{"display_name":"Fable"}}}]}`,
		now.Add(2*time.Hour).Format(time.RFC3339), weeklyReset.Format(time.RFC3339), weeklyReset.Format(time.RFC3339)))

	calls := 0
	original := usageFetchers["claude"]
	t.Cleanup(func() { usageFetchers["claude"] = original })
	usageFetchers["claude"] = func(_ context.Context, _ *http.Client, _ *coreauth.Auth, token string) (usageResult, error) {
		calls++
		if token != "access" {
			t.Errorf("token = %q, want the credential's access token", token)
		}
		return parseClaudeUsage(fixture), nil
	}

	cfg := &config.Config{AuthDir: t.TempDir()}
	cfg.Routing.UsageRefresh.Interval = "10m"
	newHandler := func() (*Handler, *coreauth.Manager) {
		manager := coreauth.NewManager(nil, nil, nil)
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
			ID: "claude-a.json", Provider: "claude", Metadata: map[string]any{"access_token": "access"},
		}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		return NewHandlerWithoutConfigFilePath(cfg, manager), manager
	}
	assertStanding := func(manager *coreauth.Manager) {
		t.Helper()
		auth, _ := manager.GetByID("claude-a.json")
		resetAt, used, spent := coreauth.ConsumeFirstStanding(auth, now)
		if !resetAt.Equal(weeklyReset) || math.Abs(used-40) > 0.001 || spent {
			t.Fatalf("standing = (%v, %v, %v), want (%v, 40, false)", resetAt, used, spent, weeklyReset)
		}
	}

	h, manager := newHandler()
	h.refreshUsage(context.Background(), now)
	if calls != 1 {
		t.Fatalf("usage polls = %d, want 1", calls)
	}
	assertStanding(manager)
	auth, _ := manager.GetByID("claude-a.json")
	windows, _ := h.usageEntry(auth)["windows"].([]coreauth.UsageWindow)
	if len(windows) != 3 || windows[2].Label != "Fable only" {
		t.Fatalf("windows = %+v, want 5h, weekly and the Fable-only limit", windows)
	}

	// Restart: a new handler and manager read the snapshot without polling again.
	h, manager = newHandler()
	h.refreshUsage(context.Background(), now.Add(time.Minute))
	if calls != 1 {
		t.Fatalf("usage polls after restart = %d, want still 1", calls)
	}
	assertStanding(manager)
}

func TestUsageParsersReadProviderShapes(t *testing.T) {
	quota := coreauth.QuotaState{}
	codex := parseCodexUsage(gjson.Parse(`{"plan_type":"pro","rate_limit":{
		"primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_at":4102444800},
		"secondary_window":{"used_percent":30,"limit_window_seconds":604800,"reset_at":4102531200}}}`))
	quota.ObserveResponseHeadersForProvider("codex", codex.headers, time.Now())
	if got := coreauth.QuotaUsageWindows(quota); codex.plan != "pro" || len(got) != 2 || got[0].UsedPercent != 12 || got[1].WindowMinutes != 10080 {
		t.Fatalf("codex = %q %+v", codex.plan, got)
	}

	for _, tc := range []struct {
		name   string
		result usageResult
		plan   string
		want   []coreauth.UsageWindow
	}{
		{"kimi pools", parseKimiUsage(gjson.Parse(`{"user":{"membership":{"level":"LEVEL_INTERMEDIATE"}},
			"usages":{"limit_5h":{"used_ratio":0.25,"reset_time":"2100-01-01T00:00:00Z"},"limit_7d":{"used_ratio":0.5}}}`)),
			"Allegretto", []coreauth.UsageWindow{{UsedPercent: 25, WindowMinutes: 300, ResetAt: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}, {UsedPercent: 50, WindowMinutes: 10080}}},
		{"kimi counts", parseKimiUsage(gjson.Parse(`{"usage":{"limit":"100","used":"40"},
			"limits":[{"window":{"duration":5,"timeUnit":"TIME_UNIT_HOUR"},"detail":{"limit":200,"remaining":180}}]}`)),
			"", []coreauth.UsageWindow{{UsedPercent: 40, WindowMinutes: 10080}, {UsedPercent: 10, WindowMinutes: 300}}},
		{"grok", parseXAIUsage(gjson.Parse(`{"config":{"creditUsagePercent":55,"subscriptionTier":"Ultra",
			"currentPeriod":{"start":"2100-01-01T00:00:00Z","end":"2100-01-08T00:00:00Z"}}}`)),
			"Ultra", []coreauth.UsageWindow{{UsedPercent: 55, WindowMinutes: 10080, ResetAt: time.Date(2100, 1, 8, 0, 0, 0, 0, time.UTC)}}},
		{"antigravity", parseAntigravityUsage(gjson.Parse(`{"response":{"groups":[{"displayName":"Gemini","buckets":[
			{"bucketId":"gemini-5h","remainingFraction":0.75},{"window":"weekly","remaining":{"remainingFraction":1}},{"bucketId":"x","disabled":true,"remainingFraction":0}]}]}}`)),
			"", []coreauth.UsageWindow{{Label: "Gemini", UsedPercent: 25, WindowMinutes: 300}, {Label: "Gemini", WindowMinutes: 10080}}},
		{"muse", parseMetaUsage(gjson.Parse(`{"subs_tier_name":"Muse Max","subs_usage":{
			"window":{"used_percent":96,"window_duration_mins":300,"resets_at":4102444800},"weekly":{"used_percent":40}}}`)),
			"Muse Max", []coreauth.UsageWindow{{UsedPercent: 96, WindowMinutes: 300, ResetAt: time.Unix(4102444800, 0).UTC()}, {UsedPercent: 40, WindowMinutes: 10080}}},
	} {
		if tc.result.plan != tc.plan || fmt.Sprint(tc.result.windows) != fmt.Sprint(tc.want) {
			t.Errorf("%s = %q %+v, want %q %+v", tc.name, tc.result.plan, tc.result.windows, tc.plan, tc.want)
		}
	}
}
