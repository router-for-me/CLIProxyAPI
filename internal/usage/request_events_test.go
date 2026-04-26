package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestEventHubSubscribeBacklogAndOverflowReset(t *testing.T) {
	hub := NewRequestEventHub(2)
	first := hub.Publish(coreusage.Record{Model: "gpt-4.1", RequestedAt: time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)})
	second := hub.Publish(coreusage.Record{Model: "gpt-4.1-mini", RequestedAt: time.Date(2026, 4, 25, 10, 1, 0, 0, time.UTC)})

	sub, backlog, resetRequired := hub.Subscribe(first.EventID)
	if resetRequired {
		t.Fatalf("resetRequired = true, want false")
	}
	if sub == nil {
		t.Fatalf("subscription should not be nil")
	}
	defer hub.Unsubscribe(sub)

	if len(backlog) != 1 || backlog[0].EventID != second.EventID {
		t.Fatalf("backlog = %#v, want second event only", backlog)
	}

	hub.Publish(coreusage.Record{Model: "gpt-4.1-nano", RequestedAt: time.Date(2026, 4, 25, 10, 2, 0, 0, time.UTC)})
	_, _, resetRequired = hub.Subscribe(first.EventID)
	if !resetRequired {
		t.Fatalf("resetRequired = false, want true when cursor is older than ring buffer")
	}
}

func TestBuildRequestEventPageFiltersAndSorts(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		Model:       "old-model",
		Source:      "openai-old",
		AuthIndex:   "old-auth",
		RequestedAt: time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC),
	})
	stats.Record(context.Background(), coreusage.Record{
		Model:       "new-model",
		Source:      "openai-new",
		AuthID:      "auth-new",
		AuthIndex:   "new-auth",
		RequestID:   "req-new",
		RequestedAt: time.Date(2026, 4, 25, 11, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  12,
			OutputTokens: 34,
			TotalTokens:  46,
		},
	})

	page := BuildRequestEventPage(stats, nil, RequestEventQuery{TimeRange: "24h", Limit: 10}, time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC))
	if len(page.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(page.Items))
	}
	if page.Items[0].Model != "new-model" {
		t.Fatalf("model = %q, want %q", page.Items[0].Model, "new-model")
	}
	if page.Items[0].RequestID != "req-new" {
		t.Fatalf("request_id = %q, want %q", page.Items[0].RequestID, "req-new")
	}
	if page.Items[0].AuthID != "auth-new" {
		t.Fatalf("auth_id = %q, want %q", page.Items[0].AuthID, "auth-new")
	}
	if page.Items[0].Tokens.TotalTokens != 46 {
		t.Fatalf("total_tokens = %d, want 46", page.Items[0].Tokens.TotalTokens)
	}
}
