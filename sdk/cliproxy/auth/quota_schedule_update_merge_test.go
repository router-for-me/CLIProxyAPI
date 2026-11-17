package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestQuotaScheduleUpdateMergesWindowReceipts(t *testing.T) {
	for _, mode := range []string{"update", "refresh", "prepare"} {
		for _, window := range []string{"weekly", "five_hour"} {
			for _, shape := range []string{"full", "sparse", "newer_incoming", "token_rotation", "account_replacement"} {
				if mode != "update" && (shape == "newer_incoming" || shape == "token_rotation" || shape == "account_replacement") {
					continue
				}
				t.Run(mode+"/"+window+"/"+shape, func(t *testing.T) {
					ctx := WithSkipPersist(context.Background())
					m := NewManager(nil, nil, nil)
					a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"account_uuid": "account-a", "organization_uuid": "org-a", "access_token": "token-a"}})
					if err != nil {
						t.Fatal(err)
					}
					at := time.Now().UTC().Truncate(time.Second)
					oldAt, partialAt, maxAt, newAt := at.Add(-4*time.Minute), at.Add(-3*time.Minute), at.Add(-2*time.Minute), at.Add(-time.Minute)
					oldReset, partialReset, otherReset, newReset := at.Add(24*time.Hour), at.Add(48*time.Hour), at.Add(72*time.Hour), at.Add(96*time.Hour)
					targetHeader, otherHeader := "Anthropic-Ratelimit-Unified-7d-Reset", "Anthropic-Ratelimit-Unified-5h-Reset"
					if window == "five_hour" {
						targetHeader, otherHeader = otherHeader, targetHeader
					}
					header := func(name string, reset time.Time) http.Header {
						return http.Header{name: []string{strconv.FormatInt(reset.Unix(), 10)}}
					}
					initial := internallogging.WithResponseHeadersHolder(ctx)
					if shape != "sparse" {
						internallogging.SetResponseHeadersAt(initial, header(targetHeader, oldReset), oldAt)
					}
					internallogging.MergeResponseHeadersAt(initial, header(otherHeader, otherReset), maxAt)
					m.MarkResult(initial, Result{AuthID: a.ID, Provider: a.Provider, Success: true})
					snapshot, _ := m.GetByID(a.ID)
					incoming := snapshot.Clone()
					incoming.Metadata["note"] = "save unrelated edit"
					partial := internallogging.WithResponseHeadersHolder(ctx)
					internallogging.SetResponseHeadersAt(partial, header(targetHeader, partialReset), partialAt)
					m.MarkResult(partial, Result{AuthID: a.ID, Provider: a.Provider, Success: true})
					current, _ := m.GetByID(a.ID)
					if !current.QuotaResetSchedule.ObservedAt.Equal(maxAt) || !snapshot.QuotaResetSchedule.ObservedAt.Equal(maxAt) {
						t.Fatal("fixture must retain equal aggregate receipt ages")
					}
					target := func(s QuotaResetSchedule) time.Time {
						if window == "weekly" {
							return s.WeeklyResetAt
						}
						return s.FiveHourResetAt
					}
					if !target(current.QuotaResetSchedule).Equal(partialReset) {
						t.Fatal("partial receipt did not advance its own window")
					}
					want := current.QuotaResetSchedule
					if shape == "newer_incoming" {
						incoming.QuotaResetSchedule = QuotaResetSchedule{ObservedAt: newAt}
						if window == "weekly" {
							incoming.QuotaResetSchedule.FiveHourResetAt = newReset
							want.FiveHourResetAt = newReset
							want.fiveHourObservedAt = time.Time{}
							want.weeklyObservedAt = partialAt
						} else {
							incoming.QuotaResetSchedule.WeeklyResetAt = newReset
							want.WeeklyResetAt = newReset
							want.weeklyObservedAt = time.Time{}
							want.fiveHourObservedAt = partialAt
						}
						want.ObservedAt = newAt
					}
					if shape == "token_rotation" {
						incoming.Metadata["access_token"] = "token-b"
					}
					if shape == "account_replacement" {
						incoming.Metadata["account_uuid"] = "account-b"
					}
					var got *Auth
					switch mode {
					case "update":
						got, err = m.Update(ctx, incoming)
					case "refresh":
						got, err = m.UpdateRefreshedAuth(ctx, snapshot, incoming)
					case "prepare":
						got, err = m.UpdatePreparedAuth(ctx, snapshot, incoming)
					}
					if err != nil || got == nil {
						t.Fatalf("update failed: %v", err)
					}
					if shape == "account_replacement" {
						if !got.QuotaResetSchedule.WeeklyResetAt.IsZero() || !got.QuotaResetSchedule.FiveHourResetAt.IsZero() {
							t.Fatal("account replacement retained old windows")
						}
						return
					}
					if got.QuotaResetSchedule != want {
						t.Fatalf("per-window receipt lost: got=%+v want=%+v", got.QuotaResetSchedule, want)
					}
					if !target(EffectiveQuotaResetSchedule(got)).Equal(partialReset) {
						t.Fatal("effective schedule lost fresh partial window")
					}
					if got.Metadata["note"] != "save unrelated edit" {
						t.Fatal("schedule merge discarded ordinary update")
					}
				})
			}
		}
	}
}
