package cachestats

import (
	"context"
	"strings"
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
		APIKey:          "test-api-key",
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

func TestUsagePluginSkipsUnusableRecords(t *testing.T) {
	store := newTestStore(t, 10, 10, time.Hour)
	plugin := usagePlugin{store: store}
	ctx := context.Background()

	// Failed request: no usage to attribute.
	failed := claudeRecord("s1", usage.Detail{})
	failed.Failed = true
	plugin.HandleUsage(ctx, failed)

	// No session id, no API key and no auth id: nothing to key on.
	anonymous := claudeRecord("", usage.Detail{CacheReadTokens: 10})
	anonymous.AuthID = ""
	anonymous.APIKey = ""
	plugin.HandleUsage(ctx, anonymous)

	// No session id and no model: nothing to key on either.
	unnamed := claudeRecord("", usage.Detail{CacheReadTokens: 10})
	unnamed.Model = ""
	plugin.HandleUsage(ctx, unnamed)

	if got := store.Global().Requests; got != 0 {
		t.Fatalf("recorded %d requests, want 0", got)
	}
}

func TestUsagePluginKeysNonClaudeCallersByAPIKeyAndModel(t *testing.T) {
	store := newTestStore(t, 10, 10, time.Hour)
	plugin := usagePlugin{store: store}
	ctx := context.Background()

	record := usage.Record{
		Provider:          "openai-compatibility",
		ExecutorType:      "OpenAICompatExecutor",
		Model:             "gpt-x",
		AuthID:            "auth-9",
		APIKey:            "sk-secret-value",
		ClientFingerprint: "curl/8.7.1",
		RequestedAt:       time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC),
		Detail: usage.Detail{
			InputTokens:     1000,
			OutputTokens:    10,
			CacheReadTokens: 800,
			TokenBreakdown:  usage.NewSubsetTokenBreakdown(1000, 800, 0, 10, 0, 1010),
		},
	}
	plugin.HandleUsage(ctx, record)

	sessions := store.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	summary := sessions[0]
	if summary.KeyedBy != KeyedByAPIKeyModel {
		t.Errorf("keyed_by = %q, want apikey-model", summary.KeyedBy)
	}
	if summary.Signal != SignalRead {
		t.Errorf("signal = %q, want read", summary.Signal)
	}
	if summary.Provider != "openai-compatibility" {
		t.Errorf("provider = %q", summary.Provider)
	}
	if strings.Contains(summary.ID, "sk-secret-value") {
		t.Fatalf("the raw API key leaked into the session id: %q", summary.ID)
	}
	if !strings.Contains(summary.ID, "gpt-x") || !strings.Contains(summary.ID, "curl/8.7.1") {
		t.Errorf("session id = %q, want the model and client fingerprint in the key", summary.ID)
	}
	// prompt_tokens already includes cached_tokens for a subset provider.
	if summary.PromptTokens != 1000 {
		t.Errorf("prompt tokens = %d, want 1000", summary.PromptTokens)
	}
	if got := summary.CachedShare; got < 0.799 || got > 0.801 {
		t.Errorf("cached share = %v, want 0.8", got)
	}

	// The same caller, model and client must land on the same session row.
	plugin.HandleUsage(ctx, record)
	if got := len(store.Sessions()); got != 1 {
		t.Errorf("sessions after a second identical caller = %d, want 1", got)
	}
	// A different model must not.
	other := record
	other.Model = "gpt-y"
	plugin.HandleUsage(ctx, other)
	if got := len(store.Sessions()); got != 2 {
		t.Errorf("sessions after a different model = %d, want 2", got)
	}
}

func TestUsagePluginRecordsGeminiAsReadOnly(t *testing.T) {
	store := newTestStore(t, 10, 10, time.Hour)
	plugin := usagePlugin{store: store}

	plugin.HandleUsage(context.Background(), usage.Record{
		Provider:     "gemini",
		ExecutorType: "GeminiExecutor",
		Model:        "gemini-3-pro",
		AuthID:       "auth-2",
		RequestedAt:  time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC),
		Detail: usage.Detail{
			InputTokens:     900,
			OutputTokens:    20,
			CachedTokens:    400,
			CacheReadTokens: 400,
			TokenBreakdown:  usage.NewSeparateReasoningTokenBreakdown(900, 400, 0, 20, 0, 920),
		},
	})

	sessions := store.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	if sessions[0].Signal != SignalRead {
		t.Errorf("signal = %q, want read", sessions[0].Signal)
	}
	if sessions[0].Classified != 1 || sessions[0].Hits != 1 {
		t.Errorf("gemini request was not classified: %+v", sessions[0].Aggregate)
	}
}

func TestUsagePluginMarksProvidersWithoutCacheAccounting(t *testing.T) {
	store := newTestStore(t, 10, 10, time.Hour)
	plugin := usagePlugin{store: store}

	plugin.HandleUsage(context.Background(), usage.Record{
		Provider:     "some-unknown-provider",
		ExecutorType: "MysteryExecutor",
		Model:        "mystery-1",
		AuthID:       "auth-3",
		RequestedAt:  time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC),
		Detail:       usage.Detail{InputTokens: 100, OutputTokens: 10},
	})

	sessions := store.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	summary := sessions[0]
	if summary.Signal != SignalNone {
		t.Errorf("signal = %q, want none", summary.Signal)
	}
	if summary.Requests != 1 {
		t.Errorf("requests = %d, want 1", summary.Requests)
	}
	if summary.Classified != 0 || summary.HitRate != 0 {
		t.Errorf("a provider with no cache signal must not report a hit rate: %+v", summary.Aggregate)
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
