package test

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func chatBody(prompt string, stream bool) string {
	return fmt.Sprintf(`{"model":"grok-fixture-chat","stream":%t,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":%q}]}`, stream, prompt)
}

func waitFor(t *testing.T, what string, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func deliveryState(t *testing.T, g *gateway, id string) usage.Delivery {
	t.Helper()
	delivery, err := g.outbox.Delivery(id)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

// TestAccountingReconciliationScenario drives one accounting history through a failover,
// an interrupted stream, a process restart, a partial batch failure and a lost export
// response, then reconciles the outbox against the receiver's spend rows and counters.
func TestAccountingReconciliationScenario(t *testing.T) {
	chat := contracts[0]
	upstream := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, call upstreamCall) bool {
		switch {
		case call.Account == "acct-A":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"quota","type":"insufficient_quota","code":"insufficient_quota"}}`))
		case strings.Contains(call.Body, "interrupt me"):
			// The connection drops after partial output and before any usage chunk arrives.
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, strings.Join(strings.SplitAfter(chat.stream.body, "\n\n")[:2], ""))
			w.(http.Flusher).Flush()
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
		default:
			return false
		}
		return true
	})
	accounts := []upstreamAccount{
		{id: "rec-A", provider: chat.provider, apiKey: "acct-A", models: []string{chat.model}, priority: 2},
		{id: "rec-B", provider: chat.provider, apiKey: "acct-B", models: []string{chat.model}, priority: 1},
	}
	recorder := recordAccounting(t)

	// Phase 1: accounting accepts events locally while no exporter is running.
	first := newGateway(t, gatewayOptions{mode: accountingEnabled, upstreamURL: upstream.server.URL, accounts: accounts})
	dataPath := first.dataPath

	failover := post(t, context.Background(), first.URL+chat.path, chatBody("failover", false), nil)
	if failover.Status != http.StatusOK {
		t.Fatalf("failover must hide the first account's quota error: %d %s", failover.Status, failover.Body)
	}
	post(t, context.Background(), first.URL+chat.path, chatBody("plain one", false), nil)
	interrupted := post(t, context.Background(), first.URL+chat.path, chatBody("interrupt me", true), nil)
	t.Logf("interrupted stream reached the client as status %d with %d lines", interrupted.Status, interrupted.Chunks)
	post(t, context.Background(), first.URL+chat.path, chatBody("plain two", true), nil)

	events := first.durableEvents(t, recorder.waitEvents(t, 5))
	byTrace := map[string][]usage.AccountingEvent{}
	var successes, unexportable []usage.AccountingEvent
	for _, event := range events {
		byTrace[event.TraceID] = append(byTrace[event.TraceID], event)
		if event.Status == "succeeded" {
			successes = append(successes, event)
		} else {
			unexportable = append(unexportable, event)
		}
	}
	if len(events) != 5 || len(successes) != 3 || len(unexportable) != 2 {
		t.Fatalf("want 3 successes and 2 non-success attempts from 4 requests, got %d events: %+v", len(events), events)
	}
	var failoverTrace []usage.AccountingEvent
	for _, group := range byTrace {
		if len(group) == 2 {
			failoverTrace = group
		}
	}
	if len(failoverTrace) != 2 || failoverTrace[0].ExecutionID == failoverTrace[1].ExecutionID {
		t.Fatalf("failover must yield two attempts under one trace with distinct execution IDs: %+v", byTrace)
	}
	first.shutdown()

	// Phase 2: restart on the same data path with an exporter whose callbacks partly fail.
	receiver := newFakeLiteLLM(t, receiverPartialFailure)
	failingID := successes[1].ExecutionID
	receiver.failIDs[failingID] = true
	second := newGateway(t, gatewayOptions{mode: accountingLiteLLMFailing, upstreamURL: upstream.server.URL, accounts: accounts, dataPath: dataPath, receiver: receiver, exportClients: true})

	waitFor(t, "first export batch", 15*time.Second, func() bool { return receiver.processedCount() >= 2 })
	receiver.setBehavior(receiverLoseResponse)
	waitFor(t, "lost-response replay", 20*time.Second, func() bool { return receiver.processedCount() >= 3 })
	receiver.setBehavior(receiverAccept)
	waitFor(t, "every success acknowledged", 20*time.Second, func() bool {
		for _, event := range successes {
			if deliveryState(t, second, event.ExecutionID).State != usage.DeliveryAcknowledged {
				return false
			}
		}
		return true
	})

	// Expected delivery attempts: one for a batch accepted first time, three for the record
	// whose callback failed, whose response was then lost, and which finally succeeded.
	for _, event := range successes {
		want := uint64(1)
		if event.ExecutionID == failingID {
			want = 3
		}
		if got := deliveryState(t, second, event.ExecutionID); got.Attempts != want {
			t.Errorf("event %s delivery attempts = %d, want %d", event.ExecutionID, got.Attempts, want)
		}
	}
	// Failures are never exported. They are rejected locally with a single claim.
	for _, event := range unexportable {
		if got := deliveryState(t, second, event.ExecutionID); got.State != usage.DeliveryRejected || got.Attempts != 1 {
			t.Errorf("non-success attempt %s delivery = %+v, want rejected after one claim", event.ExecutionID, got)
		}
	}
	for _, batch := range receiver.batches {
		for _, id := range batch {
			for _, event := range unexportable {
				if id == event.ExecutionID {
					t.Errorf("non-success attempt %s was exported", id)
				}
			}
		}
	}
	if stats, err := second.outbox.Stats(); err != nil || stats.Backlog != 0 || stats.DroppedEvents != 0 {
		t.Errorf("outbox stats after export = %+v, %v", stats, err)
	}
	if leaked := receiver.leakedSecrets(); len(leaked) > 0 {
		t.Errorf("exported records contain credential material: %v", leaked)
	}

	// Reconcile priced tokens and spend: outbox totals against unique spend rows.
	var wantPrompt, wantCompletion int64
	var wantSpend float64
	for _, event := range successes {
		wantPrompt += event.Tokens.Breakdown.Input.TotalTokens
		wantCompletion += event.Tokens.Breakdown.Output.TotalTokens
		var amount float64
		if _, err := fmt.Sscan(*event.Estimate.Amount, &amount); err != nil {
			t.Fatal(err)
		}
		wantSpend += amount
	}
	ids := make([]string, 0, len(successes))
	for _, event := range successes {
		ids = append(ids, event.ExecutionID)
	}
	var rowPrompt, rowCompletion int64
	var rowSpend float64
	for _, receipt := range receiver.receipts(ids...) {
		if receipt.Rows != 1 {
			t.Fatalf("event %s has %d spend rows, want exactly one", receipt.ID, receipt.Rows)
		}
		rowPrompt += receipt.PromptTokens
		rowCompletion += receipt.CompletionTokens
		rowSpend += receipt.Spend
	}
	if rowPrompt != wantPrompt || rowCompletion != wantCompletion || math.Abs(rowSpend-wantSpend) > 1e-9 {
		t.Errorf("spend rows (%d/%d tokens, %.9f) disagree with the outbox (%d/%d tokens, %.9f)", rowPrompt, rowCompletion, rowSpend, wantPrompt, wantCompletion, wantSpend)
	}
	if rows := receiver.rowCount(); rows != len(successes) {
		t.Errorf("spend-log rows = %d, want %d unique rows despite the replay", rows, len(successes))
	}

	// Aggregates are where at-least-once delivery shows. The replayed record is counted
	// twice, so counters exceed the unique rows by exactly the replayed callbacks.
	identity := "key/" + fixtureKeyHash()
	aggregate := receiver.aggregateSnapshot()[identity]
	replays := receiver.processedCount() - receiver.rowCount()
	if replays != 1 || aggregate.Events != int64(len(successes)+replays) {
		t.Errorf("aggregate events = %d with %d replayed callbacks, want %d events", aggregate.Events, replays, len(successes)+1)
	}
	replayedTokens := aggregate.PromptTokens - rowPrompt
	if replayedTokens != 12 || math.Abs(aggregate.Spend-rowSpend-0.000032) > 1e-9 {
		t.Errorf("aggregate over-count = %d prompt tokens, %.9f spend, want exactly one replayed record (12 tokens, 0.000032)", replayedTokens, aggregate.Spend-rowSpend)
	}
	t.Logf("reconciled: %d unique rows, %d/%d tokens, spend %.9f; aggregates over-count by %d replayed callback (%d tokens, %.9f spend)",
		receiver.rowCount(), rowPrompt, rowCompletion, rowSpend, replays, replayedTokens, aggregate.Spend-rowSpend)
}
