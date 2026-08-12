package usage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func testUsageConfig(currency, rate string) *config.Config {
	return &config.Config{
		UsageStatisticsEnabled:       true,
		UsageStatisticsRetentionDays: 90,
		UsagePricing: config.UsagePricingConfig{
			Currency: currency,
			Version:  "test-rates",
			Rules: []config.UsagePricingRule{{
				Provider: "*", Model: "*", ServiceTier: "*",
				InputPerMillion: rate, OutputPerMillion: rate,
				CacheReadPerMillion: rate, CacheWritePerMillion: rate,
			}},
		},
	}
}

func testUsageRecord(at time.Time, id, alias, rawKey, model string, failed bool) coreusage.Record {
	return coreusage.Record{
		Provider:       "openai",
		Model:          model,
		ClientKeyID:    id,
		ClientKeyAlias: alias,
		APIKey:         rawKey,
		RequestedAt:    at,
		Latency:        12 * time.Millisecond,
		TTFT:           3 * time.Millisecond,
		Failed:         failed,
		Detail: coreusage.Detail{
			InputTokens: 10, OutputTokens: 2, TotalTokens: 12,
		},
	}
}

func TestCollectorAggregatesKeysAttemptsAndDoesNotPersistRawKey(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{Now: func() time.Time { return now }})
	collector.HandleUsage(context.Background(), testUsageRecord(now, "team-a", "Team A", "sk-secret-team-a", "gpt-5", false))
	collector.HandleUsage(context.Background(), testUsageRecord(now.Add(time.Minute), "team-b", "Team B", "sk-secret-team-b", "gpt-5", true))

	report := collector.Report(Filter{})
	if report.Summary.Attempts != 2 || report.Summary.Success != 1 || report.Summary.Failed != 1 {
		t.Fatalf("summary attempts = %#v", report.Summary.Metrics)
	}
	if len(report.Keys) != 2 || report.Keys[0].KeyID != "team-a" || report.Keys[1].KeyID != "team-b" {
		t.Fatalf("keys = %#v", report.Keys)
	}
	if report.Summary.AverageLatencyMs != 12 || report.Summary.AverageTTFTMs != 3 {
		t.Fatalf("averages = latency %d ttft %d", report.Summary.AverageLatencyMs, report.Summary.AverageTTFTMs)
	}

	data, errMarshal := json.Marshal(collector.ExportSnapshot())
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	if strings.Contains(string(data), "sk-secret-team-a") || strings.Contains(string(data), "sk-secret-team-b") {
		t.Fatalf("snapshot leaked a raw API key: %s", data)
	}
}

func TestCollectorSeparatesFinalFailuresFromUpstreamAttempts(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{Now: func() time.Time { return now }})
	collector.HandleUsage(context.Background(), coreusage.Record{
		Provider: "openai", Model: "gpt-5", ClientKeyID: "team-a", OutcomeKnown: true, UpstreamAttempt: true,
		Failed: true, Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 2},
	})
	collector.HandleUsage(context.Background(), coreusage.Record{
		Provider: "openai", Model: "gpt-5", ClientKeyID: "team-a", OutcomeKnown: true, UpstreamAttempt: true,
		Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 2},
	})
	collector.HandleUsage(context.Background(), coreusage.Record{
		Provider: "openai", Model: "gpt-5", ClientKeyID: "team-a", ExternalRequest: true, OutcomeKnown: true,
	})
	collector.HandleUsage(context.Background(), coreusage.Record{
		Provider: "openai", Model: "gpt-5", ClientKeyID: "team-a", OutcomeKnown: true, Supplemental: true,
		Detail: coreusage.Detail{OutputTokens: 4},
	})

	metrics := collector.Report(Filter{}).Summary.Metrics
	if metrics.Attempts != 1 || metrics.Success != 1 || metrics.Failed != 0 {
		t.Fatalf("final request metrics = %#v", metrics)
	}
	if metrics.UpstreamAttempts != 2 || metrics.UpstreamFailedAttempts != 1 {
		t.Fatalf("upstream attempt metrics = %#v", metrics)
	}
	if metrics.Tokens.OutputTokens != 8 {
		t.Fatalf("token totals = %#v, want supplemental usage included", metrics.Tokens)
	}
}

func TestCollectorUsesConfiguredLocalCalendarDay(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, time.August, 5, 17, 5, 0, 0, time.UTC)
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{
		Now:         func() time.Time { return now },
		DayLocation: location,
	})
	collector.HandleUsage(context.Background(), testUsageRecord(now, "team-a", "Team A", "", "gpt-5", false))

	report := collector.Report(Filter{})
	if len(report.Daily) != 1 || report.Daily[0].Day != "2026-08-06" {
		t.Fatalf("daily usage = %#v, want local day 2026-08-06", report.Daily)
	}
	localDay := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	filtered := collector.Report(Filter{From: localDay, To: localDay})
	if filtered.Summary.Attempts != 1 || len(filtered.Recent) != 1 {
		t.Fatalf("local-day filtered report = %#v", filtered)
	}
	previousDay := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	if got := collector.Report(Filter{From: previousDay, To: previousDay}).Summary.Attempts; got != 0 {
		t.Fatalf("previous-day attempts = %d, want 0", got)
	}
}

func TestCollectorMigratesUnambiguousSnapshotBucketsToLocalDay(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, time.August, 5, 18, 0, 0, 0, time.UTC)
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{
		Now:         func() time.Time { return now },
		DayLocation: location,
	})
	collector.mu.Lock()
	collector.restoreLocked(Snapshot{Version: SnapshotVersion, Buckets: []Bucket{
		{
			Day: "2026-08-05", ClientKeyID: "migrated", Provider: "openai", Model: "gpt-5", ServiceTier: "default",
			FirstUsedAt: time.Date(2026, time.August, 5, 16, 10, 0, 0, time.UTC),
			LastUsedAt:  time.Date(2026, time.August, 5, 17, 0, 0, 0, time.UTC),
			Metrics:     Metrics{Attempts: 1, Success: 1},
		},
		{
			Day: "2026-08-05", ClientKeyID: "cross-midnight", Provider: "openai", Model: "gpt-5", ServiceTier: "default",
			FirstUsedAt: time.Date(2026, time.August, 5, 15, 30, 0, 0, time.UTC),
			LastUsedAt:  time.Date(2026, time.August, 5, 16, 30, 0, 0, time.UTC),
			Metrics:     Metrics{Attempts: 1, Success: 1},
		},
	}})
	dirty := collector.dirty
	collector.mu.Unlock()

	if !dirty {
		t.Fatal("snapshot migration did not mark the collector dirty")
	}
	report := collector.Report(Filter{})
	if len(report.Daily) != 2 || report.Daily[0].Day != "2026-08-05" || report.Daily[1].Day != "2026-08-06" {
		t.Fatalf("migrated daily usage = %#v", report.Daily)
	}
	if report.Daily[0].Summary.Attempts != 1 || report.Daily[1].Summary.Attempts != 1 {
		t.Fatalf("migrated daily attempts = %#v", report.Daily)
	}
}

func TestCollectorReportsRecentTenMinuteBuckets(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 5, 0, 0, time.UTC)
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{Now: func() time.Time { return now }})
	collector.HandleUsage(context.Background(), testUsageRecord(now, "team-a", "Team A", "", "gpt-5", false))
	collector.HandleUsage(context.Background(), testUsageRecord(now.Add(-10*time.Minute), "team-a", "Team A", "", "gpt-5", true))
	collector.HandleUsage(context.Background(), testUsageRecord(now.Add(-191*time.Minute), "team-b", "Team B", "", "gpt-5", false))
	collector.HandleUsage(context.Background(), testUsageRecord(now.Add(-201*time.Minute), "expired", "Expired", "", "gpt-5", false))

	report := collector.Report(Filter{})
	if report.RecentWindowMins != 200 {
		t.Fatalf("recent window = %d, want 200", report.RecentWindowMins)
	}
	if len(report.Recent) != 3 {
		t.Fatalf("recent buckets = %#v, want 3", report.Recent)
	}
	if report.Recent[0].StartAt.Format(time.RFC3339) != "2026-08-05T08:50:00Z" {
		t.Fatalf("first bucket start = %s", report.Recent[0].StartAt)
	}
	if report.Recent[2].Summary.Attempts != 1 || report.Recent[2].Summary.Success != 1 || report.Recent[2].Summary.Failed != 0 {
		t.Fatalf("current bucket = %#v", report.Recent[2].Summary.Metrics)
	}

	keyReport := collector.Report(Filter{ClientKeyID: "team-a"})
	if len(keyReport.Recent) != 2 || keyReport.Recent[1].Summary.Failed != 0 || keyReport.Recent[0].Summary.Failed != 1 {
		t.Fatalf("key-filtered recent buckets = %#v", keyReport.Recent)
	}
}

func TestCollectorResetAllClientKeyUsageClearsAndPersistsAggregates(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 5, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "usage.json")
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{
		Store: NewFileStore(path), Now: func() time.Time { return now },
	})
	collector.HandleUsage(context.Background(), testUsageRecord(now, "team-a", "Team A", "", "gpt-5", false))
	collector.HandleUsage(context.Background(), testUsageRecord(now, "team-b", "Team B", "", "gpt-5", true))

	resetCount, errReset := collector.ResetAllClientKeyUsage(context.Background())
	if errReset != nil {
		t.Fatalf("ResetAllClientKeyUsage() error = %v", errReset)
	}
	if resetCount != 2 {
		t.Fatalf("reset count = %d, want 2", resetCount)
	}
	report := collector.Report(Filter{})
	if report.Summary.Attempts != 0 || len(report.Keys) != 0 || len(report.Daily) != 0 || len(report.Models) != 0 || len(report.Recent) != 0 {
		t.Fatalf("report after reset = %#v", report)
	}

	reloaded := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{
		Store: NewFileStore(path), Now: func() time.Time { return now },
	})
	if errLoad := reloaded.Load(context.Background()); errLoad != nil {
		t.Fatalf("reloaded.Load() error = %v", errLoad)
	}
	if got := reloaded.Report(Filter{}).Summary.Attempts; got != 0 {
		t.Fatalf("persisted attempts after reset = %d, want 0", got)
	}

	collector.HandleUsage(context.Background(), testUsageRecord(now.Add(time.Minute), "team-a", "Team A", "", "gpt-5", false))
	if got := collector.Report(Filter{}).Summary.Attempts; got != 1 {
		t.Fatalf("attempts after collecting again = %d, want 1", got)
	}
}

func TestCollectorRefreshesAliasFromConfigAndAfterSnapshotLoad(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "usage.json")
	rawKey := "secret-client-key-1234"
	oldCfg := testUsageConfig("USD", "1")
	oldCfg.APIKeys = []string{rawKey}
	oldCfg.APIKeyMetadata = map[string]config.ClientAPIKeyMetadata{rawKey: {ID: "tenant", Alias: "Old Alias"}}
	collector := NewCollectorWithOptions(oldCfg, CollectorOptions{Store: NewFileStore(path), Now: func() time.Time { return now }})
	if errLoad := collector.Load(context.Background()); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	collector.HandleUsage(context.Background(), testUsageRecord(now, "tenant", "Old Alias", rawKey, "gpt-5", false))
	if errFlush := collector.Flush(context.Background()); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}

	newCfg := testUsageConfig("USD", "1")
	newCfg.APIKeys = []string{rawKey}
	newCfg.APIKeyMetadata = map[string]config.ClientAPIKeyMetadata{rawKey: {ID: "tenant", Alias: "新别名"}}
	collector.ApplyConfig(newCfg)
	if got := collector.Report(Filter{}).Keys[0].Alias; got != "新别名" {
		t.Fatalf("alias after ApplyConfig = %q, want 新别名", got)
	}
	collector.HandleUsage(context.Background(), testUsageRecord(now.Add(time.Minute), "tenant", "Old Alias", rawKey, "gpt-5", false))
	if got := collector.Report(Filter{}).Keys[0].Alias; got != "新别名" {
		t.Fatalf("alias after stale in-flight record = %q, want 新别名", got)
	}

	reloaded := NewCollectorWithOptions(newCfg, CollectorOptions{Store: NewFileStore(path), Now: func() time.Time { return now }})
	if errLoad := reloaded.Load(context.Background()); errLoad != nil {
		t.Fatalf("reloaded.Load() error = %v", errLoad)
	}
	key := reloaded.Report(Filter{}).Keys[0]
	if key.Alias != "新别名" {
		t.Fatalf("alias after snapshot load = %q, want 新别名", key.Alias)
	}
	if key.MaskedKey == "" || strings.Contains(key.MaskedKey, rawKey) {
		t.Fatalf("masked key = %q", key.MaskedKey)
	}
	if errFlush := collector.Flush(context.Background()); errFlush != nil {
		t.Fatalf("second Flush() error = %v", errFlush)
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	if strings.Contains(string(data), rawKey) {
		t.Fatalf("persisted snapshot leaked raw key: %s", data)
	}
}

func TestCollectorRepricesExistingUsageAfterPricingChange(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{Now: func() time.Time { return now }})
	collector.HandleUsage(context.Background(), testUsageRecord(now, "tenant", "", "", "gpt-5", false))
	collector.ApplyConfig(testUsageConfig("CNY", "2"))
	collector.HandleUsage(context.Background(), testUsageRecord(now.Add(time.Minute), "tenant", "", "", "gpt-5", false))

	report := collector.Report(Filter{})
	if report.Currency != "CNY" || len(report.Currencies) != 1 || report.Currencies[0] != "CNY" {
		t.Fatalf("currency metadata = currency %q currencies %#v", report.Currency, report.Currencies)
	}
	if report.Summary.EstimatedCostMicros != 48 {
		t.Fatalf("repriced cost = %d, want 48", report.Summary.EstimatedCostMicros)
	}
	if got := report.Summary.EstimatedCostMicrosByCurrency["CNY"]; got != 48 {
		t.Fatalf("CNY cost = %d, want 48", got)
	}
	if len(report.Summary.PricingVersions) != 1 {
		t.Fatalf("pricing versions = %#v, want only the current version", report.Summary.PricingVersions)
	}
	for _, attempts := range report.Summary.PricingVersions {
		if attempts != 2 {
			t.Fatalf("repriced version attempts = %d, want 2", attempts)
		}
	}
}

func TestCollectorRepricesSnapshotOnLoad(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "usage.json")
	snapshot := Snapshot{
		Version: SnapshotVersion,
		Buckets: []Bucket{{
			Day: "2026-08-05", ClientKeyID: "tenant", Provider: "openai", Model: "gpt-5", ServiceTier: "default",
			Metrics: Metrics{
				Attempts: 1, Success: 1,
				Tokens:                        TokenTotals{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
				EstimatedCostMicros:           12,
				EstimatedCostMicrosByCurrency: map[string]int64{"USD": 12},
				PricingVersions:               map[string]int64{"old-rates": 1},
			},
		}},
	}
	encoded, errMarshal := json.Marshal(snapshot)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	if errWrite := os.WriteFile(path, encoded, 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	collector := NewCollectorWithOptions(testUsageConfig("CNY", "2"), CollectorOptions{
		Store: NewFileStore(path), Now: func() time.Time { return now },
	})
	if errLoad := collector.Load(context.Background()); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	report := collector.Report(Filter{})
	if report.Currency != "CNY" || report.Summary.EstimatedCostMicros != 24 {
		t.Fatalf("repriced report = currency %q cost %d", report.Currency, report.Summary.EstimatedCostMicros)
	}
	if _, exists := report.Summary.EstimatedCostMicrosByCurrency["USD"]; exists {
		t.Fatalf("snapshot retained old USD cost: %#v", report.Summary.EstimatedCostMicrosByCurrency)
	}
	if report.Summary.UnpricedAttempts != 0 || report.Summary.UnpricedTokens != 0 {
		t.Fatalf("repriced usage remains unpriced: %#v", report.Summary.Metrics)
	}
}

func TestCollectorRetentionAndDayBoundedOverflow(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	cfg := testUsageConfig("USD", "1")
	cfg.UsageStatisticsRetentionDays = 2
	collector := NewCollectorWithOptions(cfg, CollectorOptions{
		Now: func() time.Time { return now }, MaxBuckets: 1,
	})
	collector.HandleUsage(context.Background(), testUsageRecord(now.AddDate(0, 0, -1), "a", "", "", "one", false))
	collector.HandleUsage(context.Background(), testUsageRecord(now.AddDate(0, 0, -1), "b", "", "", "two", false))
	collector.HandleUsage(context.Background(), testUsageRecord(now.AddDate(0, 0, -2), "expired", "", "", "old", false))
	report := collector.Report(Filter{})
	if report.Summary.Attempts != 2 || report.Overflow.Attempts != 1 || report.Overflow.OverflowAttempts != 1 {
		t.Fatalf("initial report = %#v", report)
	}
	rows := collector.ExportRows(Filter{})
	if len(rows) != 2 || !rows[1].Overflow || rows[1].Metrics.Attempts != 1 || rows[1].Metrics.OverflowAttempts != 1 {
		t.Fatalf("export rows omitted explicit overflow: %#v", rows)
	}
	if filteredRows := collector.ExportRows(Filter{ClientKeyID: "a"}); len(filteredRows) != 1 || filteredRows[0].Overflow {
		t.Fatalf("dimension-filtered export rows = %#v", filteredRows)
	}

	now = now.AddDate(0, 0, 2)
	collector.HandleUsage(context.Background(), testUsageRecord(now, "c", "", "", "three", false))
	report = collector.Report(Filter{})
	if report.Summary.Attempts != 1 || report.Overflow.Attempts != 0 || len(report.Daily) != 1 || report.Daily[0].Day != now.Format(time.DateOnly) {
		t.Fatalf("report after retention prune = %#v", report)
	}
}

func TestCollectorCorruptSnapshotBlocksOverwriteAndExposesError(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "usage.json")
	original := []byte("{not-json")
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{Store: NewFileStore(path), Now: func() time.Time { return now }})
	if errLoad := collector.Load(context.Background()); errLoad == nil {
		t.Fatal("Load() error = nil, want corrupt snapshot error")
	}
	collector.HandleUsage(context.Background(), testUsageRecord(now, "tenant", "", "", "gpt-5", false))
	if errClose := collector.Close(context.Background()); errClose == nil {
		t.Fatal("Close() error = nil, want persistence block error")
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	if string(data) != string(original) {
		t.Fatalf("corrupt snapshot was overwritten: %q", data)
	}
	if collector.Report(Filter{}).PersistenceError == "" {
		t.Fatal("management report omitted persistence error")
	}
}

func TestFileStoreRejectsUnsupportedSnapshotVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if errWrite := os.WriteFile(path, []byte(`{"version":2}`), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}
	_, errLoad := NewFileStore(path).Load(context.Background())
	if !errors.Is(errLoad, ErrUnsupportedSnapshotVersion) {
		t.Fatalf("Load() error = %v, want ErrUnsupportedSnapshotVersion", errLoad)
	}
}

func TestCollectorConcurrentHandleReportAndConfig(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{Now: func() time.Time { return now }})
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for index := 0; index < 100; index++ {
				collector.HandleUsage(context.Background(), testUsageRecord(now, "tenant", "", "", "gpt-5", false))
				if index%10 == 0 {
					_ = collector.Report(Filter{})
					currency := "USD"
					if worker%2 == 1 {
						currency = "CNY"
					}
					collector.ApplyConfig(testUsageConfig(currency, "1"))
				}
			}
		}(worker)
	}
	wait.Wait()
	if got := collector.Report(Filter{}).Summary.Attempts; got != 800 {
		t.Fatalf("attempts = %d, want 800", got)
	}
}

func TestCollectorSerializesConcurrentFlushes(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	store := newBlockingUsageStore()
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{
		Store: store, Now: func() time.Time { return now },
	})
	if errLoad := collector.Load(context.Background()); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	collector.HandleUsage(context.Background(), testUsageRecord(now, "tenant", "", "", "gpt-5", false))

	firstDone := make(chan error, 1)
	go func() { firstDone <- collector.Flush(context.Background()) }()
	<-store.firstStarted
	collector.HandleUsage(context.Background(), testUsageRecord(now.Add(time.Minute), "tenant", "", "", "gpt-5", false))
	secondDone := make(chan error, 1)
	go func() { secondDone <- collector.Flush(context.Background()) }()

	select {
	case <-store.secondStarted:
		// A collector without full-operation serialization reaches the store here.
	case <-time.After(50 * time.Millisecond):
	}
	close(store.releaseFirst)
	if errFlush := <-firstDone; errFlush != nil {
		t.Fatalf("first Flush() error = %v", errFlush)
	}
	if errFlush := <-secondDone; errFlush != nil {
		t.Fatalf("second Flush() error = %v", errFlush)
	}

	store.mu.Lock()
	saved := append([]Snapshot(nil), store.saved...)
	store.mu.Unlock()
	if len(saved) != 2 || len(saved[1].Buckets) != 1 || saved[1].Buckets[0].Metrics.Attempts != 2 {
		t.Fatalf("saved snapshots are stale or out of order: %#v", saved)
	}
}

func TestCollectorBackgroundFlushUsesInjectedInterval(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	store := &notifyingUsageStore{saved: make(chan Snapshot, 1)}
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{
		Store: store, FlushInterval: 5 * time.Millisecond, Now: func() time.Time { return now },
	})
	if errStart := collector.Start(context.Background()); errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
	collector.HandleUsage(context.Background(), testUsageRecord(now, "tenant", "", "", "gpt-5", false))
	select {
	case snapshot := <-store.saved:
		if len(snapshot.Buckets) != 1 || snapshot.Buckets[0].Metrics.Attempts != 1 {
			t.Fatalf("background snapshot = %#v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("background flush did not run")
	}
	if errClose := collector.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
}

func TestCollectorReportsAndClearsTransientSaveErrors(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	store := &failOnceUsageStore{}
	collector := NewCollectorWithOptions(testUsageConfig("USD", "1"), CollectorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	collector.HandleUsage(context.Background(), testUsageRecord(now, "team-a", "Team A", "secret-a", "gpt-5", false))

	if errFlush := collector.Flush(context.Background()); errFlush == nil {
		t.Fatal("first Flush() error = nil, want transient save error")
	}
	if collector.Report(Filter{}).PersistenceError == "" {
		t.Fatal("report did not expose the transient save error")
	}
	if errFlush := collector.Flush(context.Background()); errFlush != nil {
		t.Fatalf("second Flush() error = %v", errFlush)
	}
	if got := collector.Report(Filter{}).PersistenceError; got != "" {
		t.Fatalf("persistence error remained after successful retry: %q", got)
	}
}

func TestMaskAPIKeyIsRuneSafe(t *testing.T) {
	if got, want := maskAPIKey("密钥甲乙丙丁戊己庚辛壬癸"), "密钥甲乙...庚辛壬癸"; got != want {
		t.Fatalf("maskAPIKey() = %q, want %q", got, want)
	}
}

func TestClientIdentityRejectsRawKeyMetadataAndControls(t *testing.T) {
	rawKey := "secret-client-key"
	id, alias, _ := clientIdentity(coreusage.Record{
		APIKey: rawKey, ClientKeyID: rawKey, ClientKeyAlias: "Team\r\n\x1b " + rawKey,
	})
	if id == rawKey || id == "" {
		t.Fatalf("client ID = %q, want non-secret fallback", id)
	}
	if alias != "" {
		t.Fatalf("alias = %q, want empty because it contains the raw key", alias)
	}
	if got := sanitizeDisplayValue("Team\r\n\x1bA", 128); got != "TeamA" {
		t.Fatalf("sanitizeDisplayValue() = %q, want TeamA", got)
	}
}

func TestCollectorScrubsRawKeyFromAllPersistedDimensions(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	rawKey := "secret-client-key"
	cfg := testUsageConfig("USD", "1")
	cfg.APIKeys = []string{rawKey}
	cfg.UsagePricing.Currency = rawKey
	cfg.UsagePricing.Version = rawKey
	collector := NewCollectorWithOptions(cfg, CollectorOptions{Now: func() time.Time { return now }})
	record := testUsageRecord(now, "prefix-"+rawKey, "Alias "+rawKey, rawKey, "model-"+rawKey, false)
	record.Provider = "provider-" + rawKey
	record.ServiceTier = "tier-" + rawKey
	collector.HandleUsage(context.Background(), record)
	data, errMarshal := json.Marshal(collector.ExportSnapshot())
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	if strings.Contains(string(data), rawKey) {
		t.Fatalf("snapshot leaked raw key from a dimension: %s", data)
	}
	snapshot := collector.ExportSnapshot()
	if len(snapshot.Buckets) != 1 || snapshot.Buckets[0].Provider != unknownDimension || snapshot.Buckets[0].Model != unknownDimension || snapshot.Buckets[0].ServiceTier != coreusage.DefaultServiceTier {
		t.Fatalf("scrubbed bucket = %#v", snapshot.Buckets)
	}
}

func TestConfiguredClientDisplaysRejectAnyConfiguredRawKey(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"raw-key-a", "raw-key-b"},
			APIKeyMetadata: map[string]config.ClientAPIKeyMetadata{
				"raw-key-a": {ID: "raw-key-b", Alias: "Team raw-key-b"},
			},
		},
	}
	displays, _ := configuredClientKeyDisplays(cfg)
	wantID := sdkFallbackIDForTest("raw-key-a")
	display, exists := displays[wantID]
	if !exists {
		t.Fatalf("safe fallback ID %q missing from %#v", wantID, displays)
	}
	if display.alias != "" {
		t.Fatalf("alias = %q, want empty because it contains another raw key", display.alias)
	}
}

func TestCollectorCrossKeySecretIDFallsBackToAuthenticatedKey(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	cfg := testUsageConfig("USD", "1")
	cfg.APIKeys = []string{"raw-key-a", "raw-key-b"}
	cfg.APIKeyMetadata = map[string]config.ClientAPIKeyMetadata{
		"raw-key-a": {ID: "raw-key-b", Alias: "Alias raw-key-b"},
	}
	collector := NewCollectorWithOptions(cfg, CollectorOptions{Now: func() time.Time { return now }})
	collector.HandleUsage(context.Background(), testUsageRecord(now, "raw-key-b", "Alias raw-key-b", "raw-key-a", "gpt-5", false))
	snapshot := collector.ExportSnapshot()
	if len(snapshot.Buckets) != 1 || snapshot.Buckets[0].ClientKeyID != sdkaccess.FallbackClientKeyID("raw-key-a") {
		t.Fatalf("bucket identity = %#v, want authenticated key fallback", snapshot.Buckets)
	}
	data, errMarshal := json.Marshal(snapshot)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	if strings.Contains(string(data), "raw-key-a") || strings.Contains(string(data), "raw-key-b") {
		t.Fatalf("snapshot leaked configured raw key: %s", data)
	}
}

func sdkFallbackIDForTest(rawKey string) string { return sdkaccess.FallbackClientKeyID(rawKey) }

type blockingUsageStore struct {
	mu            sync.Mutex
	calls         int
	saved         []Snapshot
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  chan struct{}
}

func newBlockingUsageStore() *blockingUsageStore {
	return &blockingUsageStore{
		firstStarted: make(chan struct{}), secondStarted: make(chan struct{}), releaseFirst: make(chan struct{}),
	}
}

func (s *blockingUsageStore) Load(context.Context) (Snapshot, error) { return Snapshot{}, nil }

func (s *blockingUsageStore) Save(ctx context.Context, snapshot Snapshot) error {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		close(s.firstStarted)
		select {
		case <-s.releaseFirst:
		case <-ctx.Done():
			return ctx.Err()
		}
	} else if call == 2 {
		close(s.secondStarted)
	}
	s.mu.Lock()
	s.saved = append(s.saved, snapshot)
	s.mu.Unlock()
	return nil
}

type notifyingUsageStore struct {
	saved chan Snapshot
}

type failOnceUsageStore struct {
	mu    sync.Mutex
	calls int
}

func (s *failOnceUsageStore) Load(context.Context) (Snapshot, error) { return Snapshot{}, nil }

func (s *failOnceUsageStore) Save(context.Context, Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls == 1 {
		return errors.New("temporary usage snapshot failure")
	}
	return nil
}

func (s *notifyingUsageStore) Load(context.Context) (Snapshot, error) { return Snapshot{}, nil }

func (s *notifyingUsageStore) Save(_ context.Context, snapshot Snapshot) error {
	s.saved <- snapshot
	return nil
}
