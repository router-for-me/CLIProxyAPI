package usageapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type fakeAuthLister struct {
	auths []*coreauth.Auth
}

func (f *fakeAuthLister) List() []*coreauth.Auth {
	return f.auths
}

type fakeQuotaFetcher struct {
	mu       sync.Mutex
	calls    int
	response pluginapi.QuotaFetchResponse
	handled  bool
	err      error
}

func (f *fakeQuotaFetcher) FetchQuota(context.Context, pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.response, f.handled, f.err
}

func (f *fakeQuotaFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.UnixMilli(1_760_000_000_000)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func usageConfig(enabled bool, providers, credentials []string) *config.UsageAPIConfig {
	return &config.UsageAPIConfig{
		Enabled:      enabled,
		Providers:    providers,
		Credentials:  credentials,
		CacheSeconds: 60,
	}
}

func newTestService(t *testing.T, cfg *config.UsageAPIConfig, auths []*coreauth.Auth, quotas QuotaFetcher) (*Service, *testClock) {
	t.Helper()
	clock := newTestClock()
	service := New(func() *config.UsageAPIConfig { return cfg }, &fakeAuthLister{auths: auths}, quotas)
	service.now = clock.Now
	return service, clock
}

func observedAuth(provider, id string, signals map[string]string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		FileName: id,
		Provider: provider,
		Status:   coreauth.StatusActive,
		Quota: coreauth.QuotaState{
			ObservedAt: time.UnixMilli(1_759_999_000_000),
			Signals:    signals,
		},
	}
}

func TestSnapshotDisabledByDefault(t *testing.T) {
	quota := &fakeQuotaFetcher{handled: true}
	service, _ := newTestService(t, usageConfig(false, []string{"claude"}, nil), []*coreauth.Auth{
		observedAuth("claude", "claude-a.json", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.5"}),
	}, quota)

	resp := service.Snapshot(context.Background())
	if len(resp.Reports) != 0 {
		t.Fatalf("reports = %#v, want empty", resp.Reports)
	}
	if quota.callCount() != 0 {
		t.Fatalf("quota calls = %d, want 0", quota.callCount())
	}
	if resp.Reports == nil {
		t.Fatal("reports must be an empty array, not null")
	}
}

func TestSnapshotRequiresProviderAllowList(t *testing.T) {
	quota := &fakeQuotaFetcher{handled: true}
	service, _ := newTestService(t, usageConfig(true, nil, nil), []*coreauth.Auth{
		observedAuth("claude", "claude-a.json", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.5"}),
	}, quota)

	if service.Enabled() {
		t.Fatal("Enabled() = true, want false without providers")
	}
	resp := service.Snapshot(context.Background())
	if len(resp.Reports) != 0 {
		t.Fatalf("reports = %#v, want empty", resp.Reports)
	}
}

func TestSnapshotFiltersProvidersAndCredentials(t *testing.T) {
	quota := &fakeQuotaFetcher{handled: false}
	auths := []*coreauth.Auth{
		observedAuth("claude", "claude-a.json", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.5"}),
		observedAuth("codex", "codex-a.json", map[string]string{"X-Codex-Primary-Used-Percent": "10"}),
		observedAuth("gemini-cli", "gemini-a.json", nil),
	}
	service, _ := newTestService(t, usageConfig(true, []string{"claude", "codex"}, []string{"codex-a.json"}), auths, quota)

	resp := service.Snapshot(context.Background())
	if len(resp.Reports) != 1 {
		t.Fatalf("reports = %#v, want one", resp.Reports)
	}
	if resp.Reports[0].Provider != "codex" {
		t.Fatalf("provider = %q, want codex", resp.Reports[0].Provider)
	}
}

func TestSnapshotMatchesCompatibilityProviderName(t *testing.T) {
	quota := &fakeQuotaFetcher{
		handled: true,
		response: pluginapi.QuotaFetchResponse{Groups: []pluginapi.QuotaGroup{{
			DisplayName: "Credits",
			Buckets:     []pluginapi.QuotaBucket{{Window: "daily", RemainingFraction: 0.5}},
		}}},
	}
	auth := observedAuth("openai-compatibility", "compat-a.json", nil)
	auth.Attributes = map[string]string{"compat_name": "openrouter"}
	service, _ := newTestService(t, usageConfig(true, []string{"openrouter"}, nil), []*coreauth.Auth{auth}, quota)

	resp := service.Snapshot(context.Background())
	if len(resp.Reports) != 1 {
		t.Fatalf("reports = %#v, want one", resp.Reports)
	}
	if resp.Reports[0].Provider != "openrouter" {
		t.Fatalf("provider = %q, want openrouter", resp.Reports[0].Provider)
	}
}

func TestSnapshotPrefersQuotaProvider(t *testing.T) {
	reset := "2026-01-02T03:04:05Z"
	quota := &fakeQuotaFetcher{
		handled: true,
		response: pluginapi.QuotaFetchResponse{
			Subscription: &pluginapi.QuotaSubscription{Plan: "Max", TierName: "tier-1"},
			Groups: []pluginapi.QuotaGroup{{
				DisplayName: "Rolling Limits",
				Buckets: []pluginapi.QuotaBucket{
					{Window: "5h", RemainingFraction: 0.05, ResetTime: reset, Description: "user@example.com remaining"},
					{Window: "7d", RemainingFraction: 0.9},
				},
			}},
		},
	}
	auth := observedAuth("claude", "claude-a.json", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.99"})
	auth.Metadata = map[string]any{"access_token": "super-secret-token", "email": "user@example.com"}
	service, _ := newTestService(t, usageConfig(true, []string{"claude"}, nil), []*coreauth.Auth{auth}, quota)

	resp := service.Snapshot(context.Background())
	if len(resp.Reports) != 1 {
		t.Fatalf("reports = %#v, want one", resp.Reports)
	}
	report := resp.Reports[0]
	if report.Provider != "claude" || report.FetchedAt != 1_760_000_000_000 {
		t.Fatalf("report header = %#v", report)
	}
	if len(report.Limits) != 2 {
		t.Fatalf("limits = %#v, want two", report.Limits)
	}
	first := report.Limits[0]
	if first.ID != "claude:rolling-limits:5h" || first.Label != "Rolling Limits (5 Hour)" {
		t.Fatalf("first limit = %#v", first)
	}
	if first.Amount.Used == nil || *first.Amount.Used != 95 || first.Amount.Remaining == nil || *first.Amount.Remaining != 5 {
		t.Fatalf("first amount = %#v", first.Amount)
	}
	if first.Status != "warning" {
		t.Fatalf("first status = %q, want warning", first.Status)
	}
	if first.Window == nil || first.Window.ID != "5h" || first.Window.Label != "5 Hour" {
		t.Fatalf("first window = %#v", first.Window)
	}
	if first.Window.ResetsAt != time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli() {
		t.Fatalf("resetsAt = %d", first.Window.ResetsAt)
	}
	if report.Metadata["planType"] != "Max" || report.Metadata["tier"] != "tier-1" {
		t.Fatalf("metadata = %#v", report.Metadata)
	}
	if report.Metadata["source"] != "quota-provider" {
		t.Fatalf("source = %v", report.Metadata["source"])
	}

	raw, errMarshal := json.Marshal(resp)
	if errMarshal != nil {
		t.Fatalf("marshal response: %v", errMarshal)
	}
	for _, secret := range []string{"super-secret-token", "user@example.com"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("response leaked %q: %s", secret, raw)
		}
	}
}

func TestSnapshotFallsBackToObservedSignals(t *testing.T) {
	quota := &fakeQuotaFetcher{handled: false}
	auth := observedAuth("codex", "codex-a.json", map[string]string{
		"X-Codex-Primary-Used-Percent":     "42",
		"X-Codex-Primary-Window-Minutes":   "300",
		"X-Codex-Primary-Reset-At":         "1760000000",
		"X-Codex-Secondary-Used-Percent":   "100",
		"X-Codex-Secondary-Window-Minutes": "10080",
		"X-Codex-Plan-Type":                "plus",
		"X-Codex-Allowed":                  "true",
		"X-Codex-Limit-Reached":            "false",
	})
	service, _ := newTestService(t, usageConfig(true, []string{"codex"}, nil), []*coreauth.Auth{auth}, quota)

	resp := service.Snapshot(context.Background())
	if len(resp.Reports) != 1 {
		t.Fatalf("reports = %#v, want one", resp.Reports)
	}
	report := resp.Reports[0]
	if len(report.Limits) != 2 {
		t.Fatalf("limits = %#v, want two", report.Limits)
	}
	primary := report.Limits[0]
	if primary.ID != "codex:primary" || primary.Label != "5 Hour" {
		t.Fatalf("primary = %#v", primary)
	}
	if primary.Status != "ok" || primary.Window == nil || primary.Window.DurationMs == nil || *primary.Window.DurationMs != int64(5*time.Hour/time.Millisecond) {
		t.Fatalf("primary = %#v", primary)
	}
	secondary := report.Limits[1]
	if secondary.Status != "warning" {
		t.Fatalf("secondary status = %q, want warning", secondary.Status)
	}
	if primary.Window.ResetsAt != 1_760_000_000_000 {
		t.Fatalf("primary resetsAt = %d", primary.Window.ResetsAt)
	}
	if report.Metadata["planType"] != "plus" || report.Metadata["source"] != "observed-signals" {
		t.Fatalf("metadata = %#v", report.Metadata)
	}
}

func TestSnapshotObservedClaudeRejectedWindow(t *testing.T) {
	quota := &fakeQuotaFetcher{handled: false}
	auth := observedAuth("claude", "claude-a.json", map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization": "0.95",
		"Anthropic-Ratelimit-Unified-5h-Status":      "allowed",
		"Anthropic-Ratelimit-Unified-7d-Utilization": "1",
		"Anthropic-Ratelimit-Unified-7d-Status":      "rejected",
	})
	service, _ := newTestService(t, usageConfig(true, []string{"claude"}, nil), []*coreauth.Auth{auth}, quota)

	resp := service.Snapshot(context.Background())
	if len(resp.Reports) != 1 || len(resp.Reports[0].Limits) != 2 {
		t.Fatalf("reports = %#v", resp.Reports)
	}
	if got := resp.Reports[0].Limits[0].Status; got != "warning" {
		t.Fatalf("5h status = %q, want warning", got)
	}
	if got := resp.Reports[0].Limits[1].Status; got != "exhausted" {
		t.Fatalf("7d status = %q, want exhausted", got)
	}
}

func TestSnapshotObservedDevin(t *testing.T) {
	quota := &fakeQuotaFetcher{handled: false}
	auth := observedAuth("devin", "devin-a.json", map[string]string{
		"daily_quota_remaining_percent": "5%",
		"daily_quota_reset_at":          "2026-01-02T03:04:05Z",
		"plan":                          "Team",
	})
	service, _ := newTestService(t, usageConfig(true, []string{"devin"}, nil), []*coreauth.Auth{auth}, quota)

	resp := service.Snapshot(context.Background())
	if len(resp.Reports) != 1 || len(resp.Reports[0].Limits) != 1 {
		t.Fatalf("reports = %#v", resp.Reports)
	}
	limit := resp.Reports[0].Limits[0]
	if limit.ID != "devin:daily" || limit.Status != "warning" {
		t.Fatalf("limit = %#v", limit)
	}
	if limit.Amount.Used == nil || *limit.Amount.Used != 95 {
		t.Fatalf("amount = %#v", limit.Amount)
	}
	if resp.Reports[0].Metadata["planType"] != "Team" {
		t.Fatalf("metadata = %#v", resp.Reports[0].Metadata)
	}
}

func TestSnapshotCachesQuotaReports(t *testing.T) {
	quota := &fakeQuotaFetcher{
		handled: true,
		response: pluginapi.QuotaFetchResponse{Groups: []pluginapi.QuotaGroup{{
			Buckets: []pluginapi.QuotaBucket{{Window: "5h", RemainingFraction: 0.5}},
		}}},
	}
	service, clock := newTestService(t, usageConfig(true, []string{"claude"}, nil), []*coreauth.Auth{
		observedAuth("claude", "claude-a.json", nil),
	}, quota)

	first := service.Snapshot(context.Background())
	second := service.Snapshot(context.Background())
	if quota.callCount() != 1 {
		t.Fatalf("quota calls = %d, want 1", quota.callCount())
	}
	if first.Reports[0].FetchedAt != second.Reports[0].FetchedAt {
		t.Fatal("cached report was refetched")
	}
	clock.Advance(61 * time.Second)
	service.Snapshot(context.Background())
	if quota.callCount() != 2 {
		t.Fatalf("quota calls after expiry = %d, want 2", quota.callCount())
	}
}

func TestSnapshotNegativeCacheFallsBackToSignals(t *testing.T) {
	quota := &fakeQuotaFetcher{handled: true, err: errors.New("upstream unavailable")}
	auth := observedAuth("claude", "claude-a.json", map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization": "0.4",
	})
	service, clock := newTestService(t, usageConfig(true, []string{"claude"}, nil), []*coreauth.Auth{auth}, quota)

	for i := range 2 {
		resp := service.Snapshot(context.Background())
		if len(resp.Reports) != 1 || resp.Reports[0].Metadata["source"] != "observed-signals" {
			t.Fatalf("snapshot %d = %#v", i, resp.Reports)
		}
	}
	if quota.callCount() != 1 {
		t.Fatalf("quota calls = %d, want 1", quota.callCount())
	}
	clock.Advance(failedFetchCacheTTL + time.Second)
	service.Snapshot(context.Background())
	if quota.callCount() != 2 {
		t.Fatalf("quota calls after negative cache = %d, want 2", quota.callCount())
	}
}

func TestQuotaReportSkipsInvalidBucketsAndBoundsLimits(t *testing.T) {
	groups := make([]pluginapi.QuotaGroup, 2)
	buckets := make([]pluginapi.QuotaBucket, 0, maxLimitsPerReport+5)
	for range maxLimitsPerReport + 5 {
		buckets = append(buckets, pluginapi.QuotaBucket{Window: "5h", RemainingFraction: 0.5})
	}
	groups[0] = pluginapi.QuotaGroup{DisplayName: "First", Buckets: buckets}
	groups[1] = pluginapi.QuotaGroup{DisplayName: "Invalid", Buckets: []pluginapi.QuotaBucket{
		{Window: "5h", RemainingFraction: math.Inf(1)},
	}}
	report := reportFromQuota("claude", pluginapi.QuotaFetchResponse{Groups: groups}, 1_760_000_000_000)
	if report == nil {
		t.Fatal("report = nil")
	}
	if len(report.Limits) != maxLimitsPerReport {
		t.Fatalf("limits = %d, want %d", len(report.Limits), maxLimitsPerReport)
	}
}
