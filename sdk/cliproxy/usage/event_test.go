package usage

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAccountingEventRedactionAndCorrelation(t *testing.T) {
	record := Record{RequestID: "execution", TraceID: "parent", Provider: "openai", Model: "alias-model", ExecutedModel: "upstream", Alias: "alias", ResponseModel: "served", ServiceTier: "auto", ResponseServiceTier: "priority", Stream: true, RequestedAt: time.Unix(100, 0), Latency: 2 * time.Second, AuthID: "private-account", APIKey: "private-key", Source: "private-source", AccessTokenSHA256: "private-token", BaseURL: "https://private-url", Fail: Failure{StatusCode: 429, Body: "private-body"}, Failed: true, ResponseHeaders: http.Header{"Secret": {"private-header"}}, Detail: Detail{UsagePresent: true, InputTokens: 10, OutputTokens: 6, ReasoningTokens: 5, CacheReadTokens: 4, TotalTokens: 16, TokenEvidence: &TokenEvidence{Fields: map[string]int64{"input_tokens": 10, "private-prompt": 42}}}}
	event := NewAccountingEvent(context.Background(), record)
	if event.ExecutionID != record.RequestID || event.TraceID != record.TraceID || event.ExecutedModel != "upstream" || event.Status != "failed" || event.StatusCode != 429 || !event.CompletedAt.Equal(record.RequestedAt.Add(record.Latency)) {
		t.Fatalf("event = %+v", event)
	}
	if event.Tokens.Breakdown.TotalTokens != 16 || event.Tokens.Breakdown.Input.UncachedTokens != 6 || event.Tokens.Breakdown.Output.NonReasoningTokens != 1 {
		t.Fatalf("tokens = %+v", event.Tokens)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-") {
		t.Fatalf("unsafe event: %s", data)
	}
	if len(event.AccountID) != 64 || len(event.ClientKeyID) != 64 {
		t.Fatal("missing safe identifiers")
	}
}

func TestAccountingUsagePresence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		detail  Detail
		present bool
	}{
		{"missing", Detail{}, false}, {"explicit zero", Detail{UsagePresent: true}, true}, {"legacy", Detail{TotalTokens: 3}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := NewAccountingEvent(context.Background(), Record{Detail: tc.detail})
			if (event.Tokens != nil) != tc.present {
				t.Fatalf("tokens = %+v", event.Tokens)
			}
		})
	}
}

type testAccountingSink struct{ events chan AccountingEvent }

func (s *testAccountingSink) HandleAccountingEvent(event AccountingEvent) { s.events <- event }

func TestAccountingSinkIndependentDelivery(t *testing.T) {
	sink := &testAccountingSink{events: make(chan AccountingEvent, 2)}
	manager := NewManager(2)
	defer manager.Stop()
	manager.Register(&accountingPlugin{sink: sink})
	manager.Publish(context.Background(), Record{RequestID: "attempt", TraceID: "trace", Interrupted: true})
	manager.Publish(context.Background(), Record{RequestID: "tool", AdditionalModel: true})
	manager.Publish(context.Background(), Record{RequestID: "next"})
	for _, id := range []string{"attempt", "next"} {
		select {
		case event := <-sink.events:
			if event.ExecutionID != id {
				t.Fatalf("event = %+v", event)
			}
			if id == "attempt" && event.Status != "interrupted" {
				t.Fatal(event.Status)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("accounting delivery blocked")
		}
	}
}
