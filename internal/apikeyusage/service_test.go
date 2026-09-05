package apikeyusage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const testManagedKey = "sk-cpa-0123456789012345678901234567890123456789012"
const testManagedKeyTwo = "sk-cpa-abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"

func TestReserveEnforcesWeeklyRequestLimit(t *testing.T) {
	service := newTestService(t, config.APIKeyProfile{
		ID: "alice", Name: "Alice", APIKey: testManagedKey,
		Weekly: config.APIKeyLimit{Requests: 1},
	})
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)

	first, err := service.Reserve(context.Background(), testManagedKey, "gpt-5.4", now)
	if err != nil {
		t.Fatalf("Reserve(first) error = %v", err)
	}
	if !first.Allowed {
		t.Fatalf("Reserve(first) allowed = false, code = %q", first.Code)
	}
	second, err := service.Reserve(context.Background(), testManagedKey, "gpt-5.4", now)
	if err != nil {
		t.Fatalf("Reserve(second) error = %v", err)
	}
	if second.Allowed || second.Code != "week_request_limit" {
		t.Fatalf("Reserve(second) = allowed %v code %q", second.Allowed, second.Code)
	}
}

func TestReserveEnforcesModelAllowlist(t *testing.T) {
	service := newTestService(t, config.APIKeyProfile{
		ID: "alice", Name: "Alice", APIKey: testManagedKey,
		AllowedModels: []string{"gpt-*", "gemini-3.*"},
	})

	allowed, err := service.Reserve(context.Background(), testManagedKey, "GPT-5.4", time.Now())
	if err != nil || !allowed.Allowed {
		t.Fatalf("allowed model decision = %#v, err = %v", allowed, err)
	}
	denied, err := service.Reserve(context.Background(), testManagedKey, "claude-opus-4-6", time.Now())
	if err != nil {
		t.Fatalf("denied model error = %v", err)
	}
	if denied.Allowed || denied.Code != "model_not_allowed" {
		t.Fatalf("denied model decision = %#v", denied)
	}
}

func TestHandleUsagePersistsTokensAndSummary(t *testing.T) {
	service := newTestService(t, config.APIKeyProfile{
		ID: "alice", Name: "Alice", APIKey: testManagedKey,
		Monthly: config.APIKeyLimit{Requests: 10, Tokens: 1000},
	})
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	decision, err := service.Reserve(context.Background(), testManagedKey, "gpt-5.4", now)
	if err != nil || !decision.Allowed {
		t.Fatalf("Reserve() = %#v, %v", decision, err)
	}
	if err = service.Complete(context.Background(), decision.Reservation, 200); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	service.HandleUsage(context.Background(), coreusage.Record{
		Provider: "codex", Model: "gpt-5.4", Alias: "gpt-5.4", APIKey: testManagedKey,
		RequestedAt: now,
		Detail:      coreusage.Detail{InputTokens: 120, OutputTokens: 30, TotalTokens: 150},
	})

	summary, err := service.SummaryForPeriod(context.Background(), "month", now)
	if err != nil {
		t.Fatalf("SummaryForPeriod() error = %v", err)
	}
	if len(summary.Profiles) != 1 {
		t.Fatalf("profiles length = %d, want 1", len(summary.Profiles))
	}
	usage := summary.Profiles[0]
	if usage.Usage.Requests != 1 || usage.Usage.Successes != 1 || usage.Usage.TotalTokens != 150 {
		t.Fatalf("usage = %#v", usage.Usage)
	}
	if usage.RemainingRequests != 9 || usage.RemainingTokens != 850 {
		t.Fatalf("remaining requests/tokens = %d/%d", usage.RemainingRequests, usage.RemainingTokens)
	}
	if len(summary.Models) != 1 || summary.Models[0].Model != "gpt-5.4" {
		t.Fatalf("models = %#v", summary.Models)
	}
}

func TestHandleUsageUsesCanonicalNonOverlappingTokens(t *testing.T) {
	service := newTestService(t, config.APIKeyProfile{
		ID: "alice", Name: "Alice", APIKey: testManagedKey,
	})
	now := time.Now().UTC()
	service.HandleUsage(nil, coreusage.Record{
		Provider: "claude", Model: "claude-sonnet-4-5", APIKey: testManagedKey,
		RequestedAt: now,
		Detail: coreusage.Detail{
			TokenBreakdown: coreusage.NewSubsetTokenBreakdown(100, 40, 0, 20, 5, 120),
		},
	})

	summary, err := service.SummaryForPeriod(context.Background(), "month", now)
	if err != nil {
		t.Fatalf("SummaryForPeriod() error = %v", err)
	}
	if len(summary.Profiles) != 1 {
		t.Fatalf("profiles length = %d, want 1", len(summary.Profiles))
	}
	usage := summary.Profiles[0].Usage
	if usage.InputTokens != 100 || usage.OutputTokens != 20 || usage.CachedTokens != 40 || usage.ReasoningTokens != 5 || usage.TotalTokens != 120 {
		t.Fatalf("canonical usage = %#v", usage)
	}
}

func TestReserveEnforcesCompletedTokenLimit(t *testing.T) {
	service := newTestService(t, config.APIKeyProfile{
		ID: "alice", Name: "Alice", APIKey: testManagedKey,
		Weekly: config.APIKeyLimit{Tokens: 100},
	})
	now := time.Now().UTC()
	first, err := service.Reserve(context.Background(), testManagedKey, "gpt-5.4", now)
	if err != nil || !first.Allowed {
		t.Fatalf("Reserve(first) = %#v, %v", first, err)
	}
	service.HandleUsage(context.Background(), coreusage.Record{
		Provider: "codex", Model: "gpt-5.4", APIKey: testManagedKey, RequestedAt: now,
		Detail: coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(80, 0, 0, 20, 0, 100)},
	})

	second, err := service.Reserve(context.Background(), testManagedKey, "gpt-5.4", now)
	if err != nil {
		t.Fatalf("Reserve(second) error = %v", err)
	}
	if second.Allowed || second.Code != "week_token_limit" || second.Used != 100 {
		t.Fatalf("Reserve(second) = %#v", second)
	}
}

func TestUsageIsolatedByProfile(t *testing.T) {
	service := newTestService(t,
		config.APIKeyProfile{ID: "alice", Name: "Alice", APIKey: testManagedKey},
		config.APIKeyProfile{ID: "bob", Name: "Bob", APIKey: testManagedKeyTwo},
	)
	now := time.Now().UTC()
	for _, apiKey := range []string{testManagedKey, testManagedKey, testManagedKeyTwo} {
		decision, err := service.Reserve(context.Background(), apiKey, "gpt-5.4", now)
		if err != nil || !decision.Allowed {
			t.Fatalf("Reserve(%q) = %#v, %v", apiKey, decision, err)
		}
	}

	summary, err := service.SummaryForPeriod(context.Background(), "week", now)
	if err != nil {
		t.Fatalf("SummaryForPeriod() error = %v", err)
	}
	requestsByID := make(map[string]int64, len(summary.Profiles))
	for _, profile := range summary.Profiles {
		requestsByID[profile.ID] = profile.Usage.Requests
	}
	if requestsByID["alice"] != 2 || requestsByID["bob"] != 1 {
		t.Fatalf("requests by profile = %#v", requestsByID)
	}
}

func TestPruneExpiredRemovesOldEventsAndPeriods(t *testing.T) {
	service := newTestService(t, config.APIKeyProfile{
		ID: "alice", Name: "Alice", APIKey: testManagedKey,
	})
	now := time.Now().UTC().Truncate(time.Second)
	old := now.AddDate(0, 0, -60).Unix()
	recent := now.AddDate(0, 0, -10).Unix()

	for _, requestedAt := range []int64{old, recent} {
		if _, err := service.db.Exec(`INSERT INTO usage_events (key_hash, profile_id, provider, model, requested_at, failed, status_code, latency_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens) VALUES (?, ?, '', '', ?, 0, 200, 0, 0, 0, 0, 0, 0)`, hashAPIKey(testManagedKey), "alice", requestedAt); err != nil {
			t.Fatalf("insert usage event: %v", err)
		}
		if _, err := service.db.Exec(`INSERT INTO period_usage (key_hash, profile_id, profile_name, period_kind, period_start) VALUES (?, ?, ?, ?, ?)`, hashAPIKey(testManagedKey), fmt.Sprintf("alice-%d", requestedAt), "Alice", periodWeek, requestedAt); err != nil {
			t.Fatalf("insert period usage: %v", err)
		}
	}
	if err := pruneExpired(context.Background(), service.db, 30, now); err != nil {
		t.Fatalf("pruneExpired() error = %v", err)
	}

	var eventCount, periodCount int
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&eventCount); err != nil {
		t.Fatalf("count usage events: %v", err)
	}
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM period_usage`).Scan(&periodCount); err != nil {
		t.Fatalf("count period usage: %v", err)
	}
	if eventCount != 1 || periodCount != 1 {
		t.Fatalf("remaining events/periods = %d/%d, want 1/1", eventCount, periodCount)
	}
}

func TestUsageSurvivesServiceRestart(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := testConfig(filepath.Join(dir, "usage.db"), config.APIKeyProfile{ID: "alice", Name: "Alice", APIKey: testManagedKey})
	service, err := New(configPath, cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	decision, err := service.Reserve(context.Background(), testManagedKey, "gpt-5.4", now)
	if err != nil || !decision.Allowed {
		t.Fatalf("Reserve() = %#v, %v", decision, err)
	}
	if err = service.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := New(configPath, cfg)
	if err != nil {
		t.Fatalf("New(reopened) error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	summary, err := reopened.SummaryForPeriod(context.Background(), "week", now)
	if err != nil {
		t.Fatalf("SummaryForPeriod() error = %v", err)
	}
	if len(summary.Profiles) != 1 || summary.Profiles[0].Usage.Requests != 1 {
		t.Fatalf("persisted profiles = %#v", summary.Profiles)
	}
}

func newTestService(t *testing.T, profiles ...config.APIKeyProfile) *Service {
	t.Helper()
	dir := t.TempDir()
	service, err := New(filepath.Join(dir, "config.yaml"), testConfig(filepath.Join(dir, "usage.db"), profiles...))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func testConfig(databasePath string, profiles ...config.APIKeyProfile) *config.Config {
	return &config.Config{
		SDKConfig: config.SDKConfig{APIKeyProfiles: profiles},
		APIKeyUsage: config.APIKeyUsageConfig{
			Enabled: true, DatabasePath: databasePath, RetentionDays: 400, Timezone: "UTC",
		},
	}
}
