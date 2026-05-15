package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// TestNextTransientCooldown verifies the 5xx exponential ladder starts short
// and caps at the configured maximum without overflow.
func TestNextTransientCooldown(t *testing.T) {
	t.Parallel()

	cases := []struct {
		level int
		want  time.Duration
	}{
		{0, 5 * time.Second},
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{4, 80 * time.Second},
		{5, 160 * time.Second},
		{6, transientBackoffMax}, // capped
		{50, transientBackoffMax},
	}
	for _, tc := range cases {
		got, _ := nextTransientCooldown(tc.level, false)
		if got != tc.want {
			t.Fatalf("nextTransientCooldown(%d) = %v, want %v", tc.level, got, tc.want)
		}
	}

	if d, _ := nextTransientCooldown(2, true); d != 0 {
		t.Fatalf("disableCooling should yield 0 cooldown, got %v", d)
	}
}

// TestMarkResult_5xxUsesExpBackoff verifies repeated 503s grow the per-model
// transient backoff level rather than parking on a fixed 1-minute cooldown.
func TestMarkResult_5xxUsesExpBackoff(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "tx-503", Provider: "codex"}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	model := "gpt-5.3-codex-spark"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	mark := func() *ModelState {
		t.Helper()
		m.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: "codex",
			Model:    model,
			Success:  false,
			Error:    &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream 503"},
		})
		updated, ok := m.GetByID(auth.ID)
		if !ok || updated == nil {
			t.Fatalf("auth missing after MarkResult")
		}
		st := updated.ModelStates[model]
		if st == nil {
			t.Fatalf("model state missing")
		}
		return st
	}

	want := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
	}
	wantLevels := []int{1, 2, 3}
	for i, expected := range want {
		st := mark()
		got := time.Until(st.NextRetryAfter)
		// Allow a small tolerance because NextRetryAfter is computed from now.Add(...)
		// inside MarkResult; the assertion runs slightly after that point.
		if got > expected || got < expected-2*time.Second {
			t.Fatalf("attempt %d: NextRetryAfter delta = %v, want ~%v", i, got, expected)
		}
		if st.TransientBackoffLevel != wantLevels[i] {
			t.Fatalf("attempt %d: TransientBackoffLevel = %d, want %d", i, st.TransientBackoffLevel, wantLevels[i])
		}
	}

	// A success clears both the quota state and the transient backoff level.
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    model,
		Success:  true,
	})
	updated, _ := m.GetByID(auth.ID)
	if st := updated.ModelStates[model]; st != nil && (st.TransientBackoffLevel != 0 || !st.BlockedSince.IsZero()) {
		t.Fatalf("success should reset transient backoff and blocked-since, got level=%d blockedSince=%v", st.TransientBackoffLevel, st.BlockedSince)
	}
}

// TestMarkResult_BlockedSinceStableAcrossRetries verifies that BlockedSince is
// set on first transition into a blocked state and *not* bumped on subsequent
// failures — so the 10s probe window measures real elapsed unavailability.
func TestMarkResult_BlockedSinceStableAcrossRetries(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "blocked-since", Provider: "codex"}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	model := "m"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "codex", Model: model,
		Error: &Error{HTTPStatus: 503, Message: "x"},
	})
	updated, _ := m.GetByID(auth.ID)
	first := updated.ModelStates[model].BlockedSince
	if first.IsZero() {
		t.Fatalf("BlockedSince must be set on first failure")
	}

	time.Sleep(20 * time.Millisecond)

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "codex", Model: model,
		Error: &Error{HTTPStatus: 503, Message: "x"},
	})
	updated, _ = m.GetByID(auth.ID)
	second := updated.ModelStates[model].BlockedSince
	if !second.Equal(first) {
		t.Fatalf("BlockedSince must remain stable across failures, was %v then %v", first, second)
	}
}

// TestGetAvailableAuths_MixedReasonsReturnsModelCooldown verifies the legacy
// selector path returns a cooldown-style error (with retry hint) when all
// candidates are blocked, regardless of the underlying reason mix.
func TestGetAvailableAuths_MixedReasonsReturnsModelCooldown(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "m"
	auths := []*Auth{
		{
			ID: "quota", ModelStates: map[string]*ModelState{model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(2 * time.Minute),
				BlockedSince:   now.Add(-1 * time.Second),
				Quota:          QuotaState{Exceeded: true, NextRecoverAt: now.Add(2 * time.Minute)},
			}},
		},
		{
			ID: "transient", ModelStates: map[string]*ModelState{model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Second),
				BlockedSince:   now.Add(-1 * time.Second),
			}},
		},
	}

	_, err := getAvailableAuths(auths, "codex", model, now)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var cooldown *modelCooldownError
	if !errors.As(err, &cooldown) {
		t.Fatalf("expected *modelCooldownError, got %T (%v)", err, err)
	}
	// earliest is the 30s transient cooldown.
	if cooldown.resetIn > 35*time.Second || cooldown.resetIn < 25*time.Second {
		t.Fatalf("resetIn = %v, want ~30s", cooldown.resetIn)
	}
	if !strings.Contains(err.Error(), "cooling down") {
		t.Fatalf("error message should describe cooldown, got %q", err.Error())
	}
}

// TestGetAvailableAuths_AllBlockedProbeAfterWindow verifies that once every
// candidate has been blocked at least allBlockedProbeWindow, a probe is
// returned instead of a cooldown error so the next request can retry.
func TestGetAvailableAuths_AllBlockedProbeAfterWindow(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "m"
	old := now.Add(-2 * allBlockedProbeWindow)
	auths := []*Auth{
		{ID: "a", ModelStates: map[string]*ModelState{model: {
			Status: StatusError, Unavailable: true,
			NextRetryAfter: now.Add(time.Minute),
			BlockedSince:   old,
		}}},
		{ID: "b", ModelStates: map[string]*ModelState{model: {
			Status: StatusError, Unavailable: true,
			NextRetryAfter: now.Add(time.Minute),
			BlockedSince:   old.Add(time.Second), // slightly newer
		}}},
	}

	probes, err := getAvailableAuths(auths, "codex", model, now)
	if err != nil {
		t.Fatalf("expected probe candidates, got error: %v", err)
	}
	if len(probes) == 0 {
		t.Fatalf("expected at least one probe candidate")
	}
	if probes[0].ID != "a" {
		t.Fatalf("probe order should put oldest blocked first, got %q", probes[0].ID)
	}
}

// TestGetAvailableAuths_AllBlockedNoProbeBeforeWindow verifies that if every
// candidate just got blocked, the picker still surfaces a cooldown error
// rather than probing prematurely.
func TestGetAvailableAuths_AllBlockedNoProbeBeforeWindow(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "m"
	auths := []*Auth{
		{ID: "a", ModelStates: map[string]*ModelState{model: {
			Status: StatusError, Unavailable: true,
			NextRetryAfter: now.Add(30 * time.Second),
			BlockedSince:   now.Add(-1 * time.Second),
		}}},
	}

	_, err := getAvailableAuths(auths, "codex", model, now)
	if err == nil {
		t.Fatalf("expected error before probe window elapses")
	}
	var cooldown *modelCooldownError
	if !errors.As(err, &cooldown) {
		t.Fatalf("expected cooldown error, got %T (%v)", err, err)
	}
}

// TestModelSchedulerProbeAndAvailability ensures the scheduler's broadened
// availabilitySummaryLocked counts blocked entries and pickProbeLocked picks
// the longest-blocked entry.
func TestModelSchedulerProbeAndAvailability(t *testing.T) {
	t.Parallel()

	now := time.Now()
	old := now.Add(-2 * allBlockedProbeWindow)
	mid := now.Add(-allBlockedProbeWindow - time.Second)

	authA := &Auth{ID: "a", Provider: "codex", ModelStates: map[string]*ModelState{"m": {
		Status: StatusError, Unavailable: true,
		NextRetryAfter: now.Add(time.Minute),
		BlockedSince:   old,
	}}}
	authB := &Auth{ID: "b", Provider: "codex", ModelStates: map[string]*ModelState{"m": {
		Status: StatusError, Unavailable: true,
		NextRetryAfter: now.Add(time.Minute),
		BlockedSince:   mid,
	}}}

	shard := &modelScheduler{
		modelKey:        "m",
		entries:         make(map[string]*scheduledAuth),
		readyByPriority: make(map[int]*readyBucket),
	}
	shard.upsertEntryLocked(buildScheduledAuthMeta(authA), now)
	shard.upsertEntryLocked(buildScheduledAuthMeta(authB), now)

	total, cooldown, earliest := shard.availabilitySummaryLocked(nil)
	if total != 2 || cooldown != 2 {
		t.Fatalf("availabilitySummary total=%d cooldown=%d, want 2/2", total, cooldown)
	}
	if earliest.IsZero() {
		t.Fatalf("expected non-zero earliest retry time")
	}

	picked := shard.pickProbeLocked(nil, now)
	if picked == nil {
		t.Fatalf("expected probe pick when entries are aged past the window")
	}
	if picked.ID != "a" {
		t.Fatalf("probe should pick longest-blocked auth, got %q", picked.ID)
	}

	// A predicate that excludes "a" must skip it and return the next-oldest.
	picked = shard.pickProbeLocked(func(e *scheduledAuth) bool {
		return e != nil && e.auth != nil && e.auth.ID != "a"
	}, now)
	if picked == nil || picked.ID != "b" {
		t.Fatalf("predicate-excluding-a should yield b, got %v", picked)
	}

	// If nothing is aged past the window, no probe is offered.
	freshAuth := &Auth{ID: "c", Provider: "codex", ModelStates: map[string]*ModelState{"m": {
		Status: StatusError, Unavailable: true,
		NextRetryAfter: now.Add(time.Minute),
		BlockedSince:   now.Add(-1 * time.Second),
	}}}
	freshShard := &modelScheduler{
		modelKey:        "m",
		entries:         make(map[string]*scheduledAuth),
		readyByPriority: make(map[int]*readyBucket),
	}
	freshShard.upsertEntryLocked(buildScheduledAuthMeta(freshAuth), now)
	if got := freshShard.pickProbeLocked(nil, now); got != nil {
		t.Fatalf("expected no probe before window, got %q", got.ID)
	}
}
