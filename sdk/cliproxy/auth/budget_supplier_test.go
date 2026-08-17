package auth

import (
	"testing"
	"time"
)

func TestApplyBudgetObservations_WindowExhaustedUntilRecoverAt(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	recoverAt := now.Add(2 * time.Hour)
	zero := 0.0
	auth := &Auth{ID: "a1"}
	ApplyBudgetObservations([]*Auth{auth}, []BudgetObservation{{
		AuthID: "a1", Kind: BudgetKindWindow, Remaining: &zero, RecoverAt: recoverAt,
	}}, BudgetApplyConfig{Now: func() time.Time { return now }})
	if !auth.Unavailable || !auth.Quota.Exceeded {
		t.Fatalf("window remaining=0 must park: %+v", auth.Quota)
	}
	if !auth.Quota.NextRecoverAt.Equal(recoverAt) {
		t.Fatalf("recover = %v, want %v", auth.Quota.NextRecoverAt, recoverAt)
	}
	if auth.Quota.Reason != "credential_quota" {
		t.Fatalf("reason = %q", auth.Quota.Reason)
	}
	blocked, _, until := isAuthBlockedForModel(auth, "", now)
	if !blocked || !until.Equal(recoverAt) {
		t.Fatalf("conductor must treat the park as credential-wide: blocked=%v until=%v", blocked, until)
	}
}

func TestApplyBudgetObservations_BalanceZeroUsesRecheck(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	auth := &Auth{ID: "ds"}
	ApplyBudgetObservations([]*Auth{auth}, []BudgetObservation{{
		AuthID: "ds", Kind: BudgetKindBalance, Amount: "0", Currency: "CNY",
	}}, BudgetApplyConfig{Now: func() time.Time { return now }, Recheck: 5 * time.Minute})
	if !auth.Quota.Exceeded || auth.Quota.Reason != "credential_quota" {
		t.Fatalf("quota = %+v", auth.Quota)
	}
	if !auth.Quota.NextRecoverAt.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("recheck until = %v", auth.Quota.NextRecoverAt)
	}
}

func TestApplyBudgetObservations_UnparseableAndAbsenceFailOpen(t *testing.T) {
	auth := &Auth{ID: "a1"}
	ApplyBudgetObservations([]*Auth{auth}, []BudgetObservation{{
		AuthID: "a1", Kind: BudgetKindBalance, Amount: "n/a",
	}}, BudgetApplyConfig{})
	if auth.Quota.Exceeded || auth.Unavailable {
		t.Fatalf("unparseable amount must not park: %+v", auth.Quota)
	}
	ApplyBudgetObservations([]*Auth{auth}, []BudgetObservation{{
		AuthID: "missing", Kind: BudgetKindWindow,
	}}, BudgetApplyConfig{})
	if auth.Quota.Exceeded {
		t.Fatal("unknown auth id must be ignored")
	}
}

func TestApplyBudgetObservations_PositiveRemainingStaysEligible(t *testing.T) {
	half := 0.5
	auth := &Auth{ID: "a1"}
	ApplyBudgetObservations([]*Auth{auth}, []BudgetObservation{{
		AuthID: "a1", Kind: BudgetKindWindow, Remaining: &half,
	}}, BudgetApplyConfig{})
	if auth.Quota.Exceeded {
		t.Fatal("remaining 0.5 must stay eligible")
	}
}

func TestApplyBudgetObservations_ClearsWhenRecovered(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	auth := &Auth{ID: "a1", Unavailable: true, Quota: QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Hour)}}
	half := 0.5
	ApplyBudgetObservations([]*Auth{auth}, []BudgetObservation{{
		AuthID: "a1", Kind: BudgetKindWindow, Remaining: &half,
	}}, BudgetApplyConfig{Now: func() time.Time { return now }})
	if auth.Quota.Exceeded || auth.Unavailable {
		t.Fatalf("recovered observation must unpark: %+v", auth.Quota)
	}
}

func TestApplyBudgetObservations_PastRecoverAtFailsOpen(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	zero := 0.0
	auth := &Auth{ID: "a1"}
	ApplyBudgetObservations([]*Auth{auth}, []BudgetObservation{{
		AuthID: "a1", Kind: BudgetKindWindow, Remaining: &zero, RecoverAt: now.Add(-time.Minute),
	}}, BudgetApplyConfig{Now: func() time.Time { return now }})
	if auth.Quota.Exceeded {
		t.Fatal("stale window remaining after reset must not park")
	}
}

func TestApplyBudgetObservations_UnavailableBalanceParks(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	avail := false
	auth := &Auth{ID: "ds"}
	ApplyBudgetObservations([]*Auth{auth}, []BudgetObservation{{
		AuthID: "ds", Kind: BudgetKindBalance, Amount: "12", Available: &avail,
	}}, BudgetApplyConfig{Now: func() time.Time { return now }, Recheck: time.Minute})
	if !auth.Quota.Exceeded {
		t.Fatal("is_available=false must park")
	}
}
