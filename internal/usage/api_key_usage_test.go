package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestAPIKeyUsagePlugin_AggregatesByProviderAndKey(t *testing.T) {
	statisticsEnabled.Store(true)
	p := NewAPIKeyUsagePlugin()

	now := time.Now().UTC().Truncate(apiKeyUsageBucketWindow)

	p.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "claude",
		APIKey:      "sk-a",
		BaseURL:     "https://api.anthropic.com",
		RequestedAt: now,
	})
	p.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "Claude", // case-insensitive: should land in same bucket
		APIKey:      "sk-a",
		BaseURL:     "https://api.anthropic.com",
		RequestedAt: now.Add(2 * time.Minute),
	})
	p.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "claude",
		APIKey:      "sk-a",
		BaseURL:     "https://api.anthropic.com",
		RequestedAt: now.Add(time.Second),
		Failed:      true,
	})

	snap := p.Snapshot()
	prov, ok := snap["claude"]
	if !ok {
		t.Fatalf("expected 'claude' provider in snapshot, got: %#v", snap)
	}
	entry, ok := prov["https://api.anthropic.com|sk-a"]
	if !ok {
		t.Fatalf("expected key 'https://api.anthropic.com|sk-a', got: %#v", prov)
	}
	if entry.Success != 2 || entry.Failed != 1 {
		t.Fatalf("unexpected counts success=%d failed=%d", entry.Success, entry.Failed)
	}
	if len(entry.RecentRequests) != 1 {
		t.Fatalf("expected 1 bucket within same 10min window, got %d", len(entry.RecentRequests))
	}
	if entry.RecentRequests[0].Success != 2 || entry.RecentRequests[0].Failed != 1 {
		t.Fatalf("bucket counts wrong: %+v", entry.RecentRequests[0])
	}
}

func TestAPIKeyUsagePlugin_SkipsWhenProviderOrAPIKeyMissing(t *testing.T) {
	statisticsEnabled.Store(true)
	p := NewAPIKeyUsagePlugin()

	p.HandleUsage(context.Background(), coreusage.Record{Provider: "claude", APIKey: ""})
	p.HandleUsage(context.Background(), coreusage.Record{Provider: "", APIKey: "sk-a"})

	if got := p.Snapshot(); len(got) != 0 {
		t.Fatalf("expected empty snapshot, got: %#v", got)
	}
}

func TestAPIKeyUsagePlugin_BucketRingCap(t *testing.T) {
	statisticsEnabled.Store(true)
	p := NewAPIKeyUsagePlugin()

	base := time.Now().UTC().Truncate(apiKeyUsageBucketWindow)
	// Emit one request per 10-minute slot, more than the ring capacity.
	for i := 0; i < apiKeyUsageBucketCount+5; i++ {
		p.HandleUsage(context.Background(), coreusage.Record{
			Provider:    "gemini",
			APIKey:      "key1",
			RequestedAt: base.Add(time.Duration(i) * apiKeyUsageBucketWindow),
		})
	}

	snap := p.Snapshot()
	entry := snap["gemini"]["|key1"]
	if len(entry.RecentRequests) != apiKeyUsageBucketCount {
		t.Fatalf("expected ring to be capped at %d, got %d", apiKeyUsageBucketCount, len(entry.RecentRequests))
	}
	if entry.Success != int64(apiKeyUsageBucketCount+5) {
		t.Fatalf("running total should not be capped: got %d", entry.Success)
	}
}

func TestAPIKeyUsagePlugin_DisabledNoOp(t *testing.T) {
	statisticsEnabled.Store(false)
	defer statisticsEnabled.Store(true)

	p := NewAPIKeyUsagePlugin()
	p.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "claude",
		APIKey:      "sk-a",
		RequestedAt: time.Now(),
	})
	if got := p.Snapshot(); len(got) != 0 {
		t.Fatalf("expected empty snapshot when disabled, got %#v", got)
	}
}
