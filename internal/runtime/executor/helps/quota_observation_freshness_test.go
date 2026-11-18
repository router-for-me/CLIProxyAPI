package helps

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestQuotaResetReceiptFreshnessOutlivesCompletionOrder(t *testing.T) {
	for _, newer := range []string{"request", "usage"} {
		t.Run(newer, func(t *testing.T) {
			ctx := auth.WithSkipPersist(context.Background())
			m := auth.NewManager(nil, &auth.GreedySelector{}, nil)
			a, err := m.Register(ctx, &auth.Auth{ID: "a", Provider: "codex", Status: auth.StatusActive})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			oldReceipt := now.Add(-10 * time.Minute)
			newReceipt := now.Add(-5 * time.Minute)
			freshReset := now.Add(24 * time.Hour)
			oldCtx := internallogging.WithResponseHeadersHolder(ctx)
			recordAPIResponseMetadataAt(oldCtx, nil, 200, http.Header{"X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-After-Seconds": {"180"}}, oldReceipt)
			if newer == "request" {
				newCtx := internallogging.WithResponseHeadersHolder(ctx)
				recordAPIResponseMetadataAt(newCtx, nil, 200, http.Header{"X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-At": {strconv.FormatInt(freshReset.Unix(), 10)}}, newReceipt)
				m.MarkResult(newCtx, auth.Result{AuthID: a.ID, Provider: "codex", Success: true})
			} else if m.RecordQuotaResetScheduleIfUnchanged(ctx, a, auth.QuotaResetSchedule{WeeklyResetAt: freshReset, ObservedAt: newReceipt}) == nil {
				t.Fatal("usage setup rejected")
			}
			before, _ := m.GetByID(a.ID)
			m.MarkResult(oldCtx, auth.Result{AuthID: a.ID, Provider: "codex", Success: true})
			after, _ := m.GetByID(a.ID)
			if got := auth.EffectiveQuotaResetSchedule(after).WeeklyResetAt; !got.Equal(freshReset) {
				t.Errorf("old completion replaced newer reset: got=%v want=%v", got, freshReset)
			}
			if after.Success != before.Success+1 || after.Failed != before.Failed || after.Unavailable != before.Unavailable || after.Quota.Exceeded != before.Quota.Exceeded {
				t.Fatal("observation freshness changed result accounting or availability")
			}
			other := &auth.Auth{ID: "b", Provider: "codex", Status: auth.StatusActive, QuotaResetSchedule: auth.QuotaResetSchedule{WeeklyResetAt: now.Add(48 * time.Hour), ObservedAt: newReceipt}}
			winner, err := (&auth.GreedySelector{}).Pick(ctx, "codex", "", cliproxyexecutor.Options{}, []*auth.Auth{after, other})
			if err != nil {
				t.Fatal(err)
			}
			if winner.ID != a.ID {
				t.Errorf("old stream changed actual greedy winner: got=%s want=%s", winner.ID, a.ID)
			}
			if newer == "request" && !after.Quota.ObservedAt.Equal(before.Quota.ObservedAt) {
				t.Error("old response replaced newer passive observation")
			}
		})
	}
}

func TestQuotaResetPartialEventKeepsEachWindowReceiptAge(t *testing.T) {
	for _, updateWeekly := range []bool{false, true} {
		name := "new-five-hour"
		if updateWeekly {
			name = "new-weekly"
		}
		t.Run(name, func(t *testing.T) {
			ctx := auth.WithSkipPersist(context.Background())
			m := auth.NewManager(nil, &auth.GreedySelector{}, nil)
			a, err := m.Register(ctx, &auth.Auth{ID: "a", Provider: "codex", Status: auth.StatusActive})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			first := now.Add(-10 * time.Minute)
			usage := now.Add(-5 * time.Minute)
			partial := now.Add(-time.Minute)
			stream := internallogging.WithResponseHeadersHolder(ctx)
			recordAPIResponseMetadataAt(stream, nil, 200, http.Header{"X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-After-Seconds": {"180"}, "X-Codex-Secondary-Window-Minutes": {"300"}, "X-Codex-Secondary-Reset-After-Seconds": {"240"}}, first)
			weekly := now.Add(24 * time.Hour)
			five := now.Add(2 * time.Hour)
			if m.RecordQuotaResetScheduleIfUnchanged(ctx, a, auth.QuotaResetSchedule{WeeklyResetAt: weekly, FiveHourResetAt: five, ObservedAt: usage}) == nil {
				t.Fatal("usage setup rejected")
			}
			event := []byte(`{"type":"codex.rate_limits","rate_limits":{"secondary":{"used_percent":30,"window_minutes":300,"reset_after_seconds":10800}}}`)
			if updateWeekly {
				event = []byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":20,"window_minutes":10080,"reset_after_seconds":21600}}}`)
				weekly = partial.Add(6 * time.Hour)
			} else {
				five = partial.Add(3 * time.Hour)
			}
			appendCodexAPIWebsocketResponseAt(stream, nil, event, partial)
			m.MarkResult(stream, auth.Result{AuthID: a.ID, Provider: "codex", Success: true})
			check := func(label string) {
				t.Helper()
				got, _ := m.GetByID(a.ID)
				schedule := auth.EffectiveQuotaResetSchedule(got)
				if !schedule.WeeklyResetAt.Equal(weekly) || !schedule.FiveHourResetAt.Equal(five) {
					t.Errorf("%s rebased another window's receipt age: got=%+v want weekly=%v five=%v", label, schedule, weekly, five)
				}
			}
			check("partial event")
			// This response arrived after usage but before the partial event. Its other
			// window must still advance without overwriting the newer partial window.
			middle := usage.Add(time.Minute)
			delayed := internallogging.WithResponseHeadersHolder(ctx)
			h := http.Header{}
			if updateWeekly {
				five = now.Add(4 * time.Hour)
				h.Set("X-Codex-Secondary-Window-Minutes", "300")
				h.Set("X-Codex-Secondary-Reset-At", strconv.FormatInt(five.Unix(), 10))
			} else {
				weekly = now.Add(12 * time.Hour)
				h.Set("X-Codex-Primary-Window-Minutes", "10080")
				h.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(weekly.Unix(), 10))
			}
			recordAPIResponseMetadataAt(delayed, nil, 200, h, middle)
			m.MarkResult(delayed, auth.Result{AuthID: a.ID, Provider: "codex", Success: true})
			check("delayed other window")
		})
	}
}
