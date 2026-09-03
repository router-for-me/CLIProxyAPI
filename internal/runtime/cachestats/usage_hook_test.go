package cachestats

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func claudeRecord(sessionID string, detail usage.Detail) usage.Record {
	return usage.Record{
		Provider:        "claude",
		ExecutorType:    "ClaudeExecutor",
		Model:           "claude-sonnet-5",
		AuthID:          "auth-1",
		ClaudeSessionID: sessionID,
		RequestedAt:     time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC),
		Detail:          detail,
	}
}

func TestUsagePluginRecordsClaudeSessions(t *testing.T) {
	store := newTestStore(t, 10, 10, time.Hour)
	plugin := usagePlugin{store: store}

	plugin.HandleUsage(context.Background(), claudeRecord("s1", usage.Detail{
		InputTokens:           2,
		OutputTokens:          7,
		CacheReadTokens:       161937,
		CacheCreationTokens:   35314,
		CacheCreation1hTokens: 35314,
		CacheMissReason:       "messages_changed",
		CacheMissedTokens:     25202,
	}))

	detail, ok := store.Session("s1")
	if !ok {
		t.Fatal("session not recorded")
	}
	if len(detail.Requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(detail.Requests))
	}
	request := detail.Requests[0]
	if request.CacheReadTokens != 161937 || request.CacheCreation1hTokens != 35314 {
		t.Errorf("cache tokens = %d read / %d 1h", request.CacheReadTokens, request.CacheCreation1hTokens)
	}
	if request.MissReason != "messages_changed" || request.MissedTokens != 25202 {
		t.Errorf("miss reason = %q/%d", request.MissReason, request.MissedTokens)
	}
	if request.Tier != TierHit {
		t.Errorf("tier = %q, want hit", request.Tier)
	}
}

func TestUsagePluginSkipsIrrelevantRecords(t *testing.T) {
	store := newTestStore(t, 10, 10, time.Hour)
	plugin := usagePlugin{store: store}
	ctx := context.Background()

	// No session id.
	plugin.HandleUsage(ctx, claudeRecord("", usage.Detail{CacheReadTokens: 10}))
	// Failed request.
	failed := claudeRecord("s1", usage.Detail{})
	failed.Failed = true
	plugin.HandleUsage(ctx, failed)
	// Non-Claude provider.
	other := claudeRecord("s2", usage.Detail{CacheReadTokens: 10})
	other.Provider = "gemini"
	other.ExecutorType = "GeminiExecutor"
	plugin.HandleUsage(ctx, other)

	if got := store.Global().Requests; got != 0 {
		t.Fatalf("recorded %d requests, want 0", got)
	}
}

func TestUsagePluginFlagsKeepaliveProbes(t *testing.T) {
	store := newTestStore(t, 10, 10, time.Hour)
	plugin := usagePlugin{store: store}

	record := claudeRecord("s1", usage.Detail{CacheReadTokens: 1000})
	record.ProbeOrigin = usage.KeepaliveProbeOrigin
	record.RequestMaxTokens = 1
	plugin.HandleUsage(context.Background(), record)

	detail, ok := store.Session("s1")
	if !ok {
		t.Fatal("session not recorded")
	}
	if !detail.Requests[0].IsProbe {
		t.Error("keepalive probe was not flagged")
	}
	if detail.Requests[0].MaxTokens != 1 {
		t.Errorf("max_tokens = %d, want 1", detail.Requests[0].MaxTokens)
	}
}

func TestUsagePluginPrefersRecordMissReason(t *testing.T) {
	store := newTestStore(t, 10, 10, time.Hour)
	plugin := usagePlugin{store: store}

	record := claudeRecord("s1", usage.Detail{CacheReadTokens: 5})
	record.CacheMissReason = "tools_changed"
	record.CacheMissedTokens = 90151
	plugin.HandleUsage(context.Background(), record)

	detail, _ := store.Session("s1")
	if detail.Requests[0].MissReason != "tools_changed" || detail.Requests[0].MissedTokens != 90151 {
		t.Fatalf("miss reason = %q/%d", detail.Requests[0].MissReason, detail.Requests[0].MissedTokens)
	}
}
