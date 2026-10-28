package management

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	devinauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
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

	devin := coreauth.QuotaUsageWindows(coreauth.QuotaState{Signals: map[string]string{
		"daily_quota_remaining_percent": "75%", "weekly_quota_remaining_percent": "40%", "weekly_quota_reset_at": "2100-01-01T00:00:00Z"}})
	if len(devin) != 2 || devin[0].UsedPercent != 25 || devin[1].UsedPercent != 60 || devin[1].ResetAt.IsZero() {
		t.Fatalf("devin = %+v", devin)
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
		{"devin", devinUsage(&devinauth.DevinUserStatus{Plan: "Max", DailyQuotaRemainingPercent: 75, DailyQuotaResetAt: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), WeeklyQuotaRemainingPercent: 0}),
			"Max", []coreauth.UsageWindow{{UsedPercent: 25, WindowMinutes: 1440, ResetAt: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}}},
		{"muse", parseMetaUsage(gjson.Parse(`{"subs_tier_name":"Muse Max","subs_usage":{
			"window":{"used_percent":96,"window_duration_mins":300,"resets_at":4102444800},"weekly":{"used_percent":40}}}`)),
			"Muse Max", []coreauth.UsageWindow{{UsedPercent: 96, WindowMinutes: 300, ResetAt: time.Unix(4102444800, 0).UTC()}, {UsedPercent: 40, WindowMinutes: 10080}}},
	} {
		if tc.result.plan != tc.plan || fmt.Sprint(tc.result.windows) != fmt.Sprint(tc.want) {
			t.Errorf("%s = %q %+v, want %q %+v", tc.name, tc.result.plan, tc.result.windows, tc.plan, tc.want)
		}
	}
}

// API keys from the config file are polled by the service their base URL names;
// a Claude or Codex API key is never sent to a login's usage endpoint.
func TestUsageKindMatchesAPIKeysByBaseURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.deepseek.com/v1":            "deepseek",
		"https://api.commandcode.ai/provider/v1": "commandcode",
		"https://api.z.ai/api/coding/paas/v4":    "zai",
		"https://opencode.ai/zen/go/v1":          "opencode-go",
		"https://opencode.ai/zen/v1":             "",
		"http://automancer-vps:8100/v1":          "",
		"":                                       "",
	} {
		auth := &coreauth.Auth{Provider: "openai-compatible-x", Attributes: map[string]string{"source": "config:x[1]", "base_url": base}}
		if got := usageKind(auth); got != want {
			t.Errorf("usageKind(%q) = %q, want %q", base, got, want)
		}
	}
	claudeKey := &coreauth.Auth{Provider: "claude", Attributes: map[string]string{"source": "config:claude[1]"}}
	if got := usageKind(claudeKey); got != "" {
		t.Errorf("claude API key kind = %q, want none", got)
	}
}

// Banked resets only arrive with a poll, so the Codex account that is always
// serving traffic (the one spent next) must still be polled hourly.
func TestUsageRefreshPollsBusyCodexHourly(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now()
	expires := now.Add(10 * 24 * time.Hour).Truncate(time.Second)
	calls := 0
	original := usageFetchers["codex"]
	t.Cleanup(func() { usageFetchers["codex"] = original })
	usageFetchers["codex"] = func(context.Context, *http.Client, *coreauth.Auth, string) (usageResult, error) {
		calls++
		credits := gjson.Parse(fmt.Sprintf(`{"credits":[{"status":"available","expires_at":%q},{"status":"redeemed","expires_at":%q}],"available_count":1}`,
			expires.Format(time.RFC3339), expires.Format(time.RFC3339)))
		return usageResult{banked: parseCodexResetCredits(credits, now), bankedRead: true}, nil
	}
	cfg := &config.Config{AuthDir: t.TempDir()}
	cfg.Routing.UsageRefresh.Interval = "10m"
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "codex-a.json", Provider: "codex", Metadata: map[string]any{"access_token": "access"},
	}); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	serve := func(at time.Time) {
		manager.ObserveQuota("codex-a.json", http.Header{"X-Codex-Primary-Used-Percent": {"10"}}, at)
	}

	serve(now)
	h.refreshUsage(context.Background(), now)
	serve(now.Add(19 * time.Minute))
	h.refreshUsage(context.Background(), now.Add(20*time.Minute))
	if calls != 1 {
		t.Fatalf("polls within the hour = %d, want 1", calls)
	}
	serve(now.Add(60 * time.Minute))
	h.refreshUsage(context.Background(), now.Add(61*time.Minute))
	if calls != 2 {
		t.Fatalf("polls after an hour of traffic = %d, want 2", calls)
	}
	auth, _ := manager.GetByID("codex-a.json")
	banked, _ := h.usageEntry(auth)["banked_resets"].([]usageCredit)
	if len(banked) != 1 || !banked[0].ExpiresAt.Equal(expires) {
		t.Fatalf("banked resets = %+v, want the one available credit", banked)
	}
}

func TestUsageParsersReadAPIKeyProviders(t *testing.T) {
	now := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	deepseek := parseDeepSeekBalance(gjson.Parse(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00"}]}`))
	if deepseek.balance == nil || *deepseek.balance != (usageBalance{Left: 110, Currency: "CNY"}) {
		t.Errorf("deepseek balance = %+v", deepseek.balance)
	}
	commandcode := parseCommandCodeUsage(gjson.Parse(`{"credits":{"monthlyCredits":28.05,"purchasedCredits":0},
		"windowLimits":{"fiveHour":{"cap":10,"used":2.5,"resetAt":"2100-01-01T05:00:00Z"},"weekly":{"cap":40,"used":10}}}`),
		gjson.Parse(`{"success":true,"data":{"planId":"individual-goat","status":"active","currentPeriodEnd":"2100-01-20T00:00:00Z"}}`))
	if commandcode.plan != "GOAT" || commandcode.balance == nil || *commandcode.balance != (usageBalance{Left: 28.05, Total: 70, Currency: "USD"}) ||
		len(commandcode.windows) != 3 || commandcode.windows[0].UsedPercent != 25 || math.Abs(commandcode.windows[2].UsedPercent-59.93) > 0.01 ||
		!commandcode.windows[2].ResetAt.Equal(time.Date(2100, 1, 20, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("commandcode = %q %+v %+v", commandcode.plan, commandcode.balance, commandcode.windows)
	}
	for _, tc := range []struct {
		name   string
		result usageResult
		plan   string
		want   []coreauth.UsageWindow
	}{
		{"glm", parseZaiUsage(gjson.Parse(`{"code":200,"success":true,"data":{"planName":"Pro","limits":[
			{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":25,"nextResetTime":4102462800000},
			{"type":"TOKENS_LIMIT","unit":6,"number":1,"percentage":9},
			{"type":"TIME_LIMIT","unit":5,"number":1,"usage":1000,"currentValue":224,"percentage":22}]}}`)),
			"Pro", []coreauth.UsageWindow{{UsedPercent: 25, WindowMinutes: 300, ResetAt: time.Date(2100, 1, 1, 5, 0, 0, 0, time.UTC)},
				{UsedPercent: 9, WindowMinutes: 10080}, {Label: "MCP", UsedPercent: 22, WindowMinutes: usageMonthMinutes}}},
		{"opencode go", parseOpenCodeGoUsage(gjson.Parse(`{"usage":{"rolling":{"percent":3,"resetInSec":18000},
			"weekly":{"percent":1,"resetInSec":266500},"monthly":{"percent":0}}}`), now),
			"", []coreauth.UsageWindow{{UsedPercent: 3, WindowMinutes: 300, ResetAt: now.Add(5 * time.Hour)},
				{UsedPercent: 1, WindowMinutes: 10080, ResetAt: now.Add(266500 * time.Second)}, {WindowMinutes: usageMonthMinutes}}},
	} {
		if tc.result.plan != tc.plan || fmt.Sprint(tc.result.windows) != fmt.Sprint(tc.want) {
			t.Errorf("%s = %q %+v, want %q %+v", tc.name, tc.result.plan, tc.result.windows, tc.plan, tc.want)
		}
	}
}
