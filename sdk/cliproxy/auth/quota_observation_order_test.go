package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestQuotaObservationRejectsOlderAndDuplicateSamples(t *testing.T) {
	at := time.Unix(100, 0)
	headers := func(used string) http.Header {
		return http.Header{"X-Codex-Primary-Used-Percent": {used}, "X-Codex-Primary-Reset-After-Seconds": {"3600"}}
	}
	q := QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: at.Add(time.Hour), BackoffLevel: 2}
	cooldown := cooldownFieldsOf(q)
	if !q.ObserveResponseHeadersForProvider("codex", headers("1"), at) {
		t.Fatal("initial observation rejected")
	}
	for _, stale := range []time.Time{at.Add(-time.Second), at} {
		if q.ObserveResponseHeadersForProvider("codex", headers("0"), stale) {
			t.Fatal("stale or duplicate observation accepted")
		}
		if q.Signals["X-Codex-Primary-Used-Percent"] != "1" || !q.ObservedAt.Equal(at) {
			t.Fatal("stale observation changed quota")
		}
	}
	// A genuinely newer refill must remain visible, even with the same reset.
	if !q.ObserveResponseHeadersForProvider("codex", headers("0"), at.Add(time.Second)) || q.Signals["X-Codex-Primary-Used-Percent"] != "0" {
		t.Fatal("new refill was suppressed")
	}
	if !cooldownQuotaEqual(q, cooldown) {
		t.Fatal("observation changed cooldown state")
	}
}

func TestMarkResultQuotaUsesReceiptOrder(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "success"
		if failed {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			auth, err := manager.Register(context.Background(), &Auth{ID: "receipt-order", Provider: "codex"})
			if err != nil {
				t.Fatal(err)
			}
			oldCtx := internallogging.WithFreshResponseHeadersHolder(context.Background())
			newCtx := internallogging.WithFreshResponseHeadersHolder(context.Background())
			internallogging.SetResponseHeaders(oldCtx, http.Header{"X-Codex-Primary-Used-Percent": {"0"}})
			internallogging.SetResponseHeaders(newCtx, http.Header{"X-Codex-Primary-Used-Percent": {"1"}})
			_, newAt := internallogging.GetResponseHeadersSnapshot(newCtx)
			result := Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Success: true}
			manager.MarkResult(newCtx, result)
			if failed {
				result.Success = false
				result.Error = &Error{HTTPStatus: 500, Message: "late failure"}
			}
			// The older request finishes after the newer one. Both account and
			// model snapshots must keep the newer 1%, not report a reset to 0%.
			manager.MarkResult(oldCtx, result)
			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated.ModelStates["gpt-5"] == nil {
				t.Fatal("missing account/model state")
			}
			for _, q := range []QuotaState{updated.Quota, updated.ModelStates["gpt-5"].Quota} {
				if q.Signals["X-Codex-Primary-Used-Percent"] != "1" || !q.ObservedAt.Equal(newAt) {
					t.Fatalf("completion reordered quota observation: %#v; receipt %v", q, newAt)
				}
			}
			// Request outcome bookkeeping still uses completion, including a
			// failed old request; rejecting its quota does not skip MarkResult.
			if failed && updated.Failed != 1 || !failed && updated.Success != 2 {
				t.Fatal("quota ordering suppressed request outcome bookkeeping")
			}
		})
	}
}

func TestMarkResultQuotaUsesWebsocketEventReceipt(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth, err := manager.Register(context.Background(), &Auth{ID: "websocket-receipt", Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := internallogging.WithFreshResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{"X-Request-Id": {"handshake"}})
	internallogging.MergeResponseHeaders(ctx, http.Header{"X-Codex-Primary-Used-Percent": {"7"}})
	_, eventAt := internallogging.GetResponseHeadersSnapshot(ctx)
	manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Success: true})
	updated, _ := manager.GetByID(auth.ID)
	if updated.Quota.Signals["X-Codex-Primary-Used-Percent"] != "7" || !updated.Quota.ObservedAt.Equal(eventAt) {
		t.Fatal("quota did not use websocket event receipt time")
	}
}
