package auth

import (
	"fmt"
	"testing"
	"time"
)

// Regression tests for issue #7432: large weighted-round-robin pools (>1024 active
// credentials) collapsed onto the first Auth.ID-sorted credentials because transient
// exclusions (retry tried-sets, cooldowns) either reset accumulated credits or minted
// fresh pool state, and config reloads discarded the per-shard weighted state.

func TestSmoothWeightedStatePrepare_LargePoolKeepsCreditsForTransientExclusions(t *testing.T) {
	t.Parallel()

	const poolSize = 2000
	full := make(map[string]int64, poolSize)
	for index := 0; index < poolSize; index++ {
		full[fmt.Sprintf("cred-%04d", index)] = 1
	}

	state := &smoothWeightedState{}
	state.prepare(full)
	// Simulate a pick that left the first credential with a large negative credit.
	state.current["cred-0000"] = -int64(poolSize - 1)

	// The credential is transiently excluded (retry tried-set / cooldown) from the next
	// candidate set. Its credit must survive: the scaled bound covers the full pool.
	subset := make(map[string]int64, poolSize-1)
	for authID, weight := range full {
		if authID == "cred-0000" {
			continue
		}
		subset[authID] = weight
	}
	state.prepare(subset)
	if state.current["cred-0000"] != -int64(poolSize-1) {
		t.Fatalf("credit for transiently excluded credential = %d, want %d", state.current["cred-0000"], -(poolSize - 1))
	}

	// Rejoining the full vector must not discard the credit either.
	state.prepare(full)
	if state.current["cred-0000"] != -int64(poolSize-1) {
		t.Fatalf("credit after rejoining full pool = %d, want %d", state.current["cred-0000"], -(poolSize - 1))
	}
}

func TestPickWeightedPoolLocked_VaryingExclusionsKeepPoolIdentityAndCredits(t *testing.T) {
	t.Parallel()

	mkAuth := func(id string) *Auth {
		return &Auth{ID: id, Provider: "provider-a", Attributes: map[string]string{AttributeWeight: "1"}}
	}
	authA, authB, authC := mkAuth("auth-a"), mkAuth("auth-b"), mkAuth("auth-c")
	entries := []*scheduledAuth{
		{auth: authA, meta: &scheduledAuthMeta{auth: authA, weight: 1}},
		{auth: authB, meta: &scheduledAuthMeta{auth: authB, weight: 1}},
		{auth: authC, meta: &scheduledAuthMeta{auth: authC, weight: 1}},
	}

	scheduler := newAuthScheduler(&WeightedRoundRobinSelector{})
	exclude := func(id string) func(*scheduledAuth) bool {
		return func(entry *scheduledAuth) bool { return entry.auth.ID != id }
	}
	exclusions := []func(*scheduledAuth) bool{nil, exclude("auth-a"), exclude("auth-b"), exclude("auth-c")}

	// Cycle the transient exclusion (retry tried-set / cooldown) between picks. With a
	// subset-keyed pool state every new exclusion combination minted a fresh accumulator
	// and the first Auth.ID-sorted member of the subset won every time; with a stable
	// structural pool identity the credits keep rotating across all three credentials.
	counts := make(map[string]int)
	for index := 0; index < 60; index++ {
		picked, errPick := scheduler.pickWeightedPoolLocked("route", 0, "", entries, exclusions[index%len(exclusions)], "", false)
		if errPick != nil {
			t.Fatalf("pick %d error = %v", index, errPick)
		}
		counts[picked.ID]++
	}
	for _, authID := range []string{"auth-a", "auth-b", "auth-c"} {
		if counts[authID] < 10 {
			t.Fatalf("credential %q picks = %d (%#v), want fair rotation across all three credentials", authID, counts[authID], counts)
		}
	}
}

func TestClearCooldownStateForAuth_PreservesActiveForceCooldowns(t *testing.T) {
	t.Parallel()

	now := time.Now()
	newForceCooledAuth := func() *Auth {
		return &Auth{
			ID:             "auth-a",
			Unavailable:    true,
			NextRetryAfter: now.Add(time.Minute),
			LastError:      &Error{Code: ErrorCodeForceCooldown, Message: "rate limited"},
			ModelStates: map[string]*ModelState{
				"model-a": {
					Unavailable:    true,
					NextRetryAfter: now.Add(time.Minute),
					LastError:      &Error{Code: ErrorCodeForceCooldown, Message: "rate limited"},
				},
			},
		}
	}

	// An active force cooldown must survive the clear (config reload with disable-cooling).
	auth := newForceCooledAuth()
	if clearCooldownStateForAuth(auth, now) {
		t.Fatalf("active force cooldown was cleared")
	}
	if !auth.Unavailable || auth.NextRetryAfter.IsZero() {
		t.Fatalf("credential force cooldown lost: unavailable=%v nextRetryAfter=%v", auth.Unavailable, auth.NextRetryAfter)
	}
	if state := auth.ModelStates["model-a"]; !state.Unavailable || state.NextRetryAfter.IsZero() {
		t.Fatalf("model force cooldown lost: unavailable=%v nextRetryAfter=%v", state.Unavailable, state.NextRetryAfter)
	}

	// Once the force cooldown expires the clear must proceed.
	auth = newForceCooledAuth()
	auth.NextRetryAfter = now.Add(-time.Minute)
	auth.ModelStates["model-a"].NextRetryAfter = now.Add(-time.Minute)
	if !clearCooldownStateForAuth(auth, now) {
		t.Fatalf("expired force cooldown was not cleared")
	}
	if auth.Unavailable || !auth.NextRetryAfter.IsZero() {
		t.Fatalf("expired force cooldown survived: unavailable=%v nextRetryAfter=%v", auth.Unavailable, auth.NextRetryAfter)
	}

	// Regular (non-force) cooldowns keep being cleared.
	auth = newForceCooledAuth()
	auth.LastError = &Error{Code: "rate_limit", Message: "rate limited"}
	auth.ModelStates["model-a"].LastError = &Error{Code: "rate_limit", Message: "rate limited"}
	if !clearCooldownStateForAuth(auth, now) {
		t.Fatalf("regular cooldown was not cleared")
	}
	if auth.Unavailable || !auth.NextRetryAfter.IsZero() {
		t.Fatalf("regular cooldown survived: unavailable=%v nextRetryAfter=%v", auth.Unavailable, auth.NextRetryAfter)
	}
}

func TestSchedulerRebuild_PreservesShardWeightedStateAcrossReload(t *testing.T) {
	t.Parallel()

	const poolSize = 40
	model := "weighted-model"
	auths := make([]*Auth, 0, poolSize)
	ids := make([]string, 0, poolSize)
	for index := 0; index < poolSize; index++ {
		id := fmt.Sprintf("cred-%03d", index)
		ids = append(ids, id)
		auths = append(auths, &Auth{ID: id, Provider: "provider-a", Attributes: map[string]string{AttributeWeight: "1"}})
	}
	registerSchedulerModels(t, "provider-a", model, ids...)

	scheduler := newSchedulerForTest(&WeightedRoundRobinSelector{}, auths...)
	modelKey := canonicalModelLookupKey(model)

	// Build the shard and generate weighted credits via a few weighted picks.
	shard := scheduler.providers["provider-a"].ensureModelLocked(modelKey, time.Now())
	if shard == nil || len(shard.readyByPriority) == 0 {
		t.Fatalf("shard was not built")
	}
	priority := shard.priorityOrder[0]
	for index := 0; index < 5; index++ {
		if picked := shard.pickReadyAtPriorityLocked(false, priority, schedulerStrategyWeightedRoundRobin, nil); picked == nil {
			t.Fatalf("weighted pick %d returned nil", index)
		}
	}
	before := make(map[string]int64, len(shard.readyByPriority[priority].all.weightedState.current))
	for authID, credit := range shard.readyByPriority[priority].all.weightedState.current {
		before[authID] = credit
	}
	if len(before) == 0 {
		t.Fatalf("weighted state is empty before rebuild")
	}

	// A config reload rebuilds the scheduler; the lazily rebuilt shard must restore the
	// accumulated credits instead of collapsing onto Auth.ID order.
	scheduler.rebuild(auths)
	rebuilt := scheduler.providers["provider-a"].ensureModelLocked(modelKey, time.Now())
	if rebuilt == nil || len(rebuilt.readyByPriority) == 0 {
		t.Fatalf("shard was not rebuilt")
	}
	after := rebuilt.readyByPriority[priority].all.weightedState.current
	if len(after) != len(before) {
		t.Fatalf("weighted state size after rebuild = %d, want %d", len(after), len(before))
	}
	for authID, credit := range before {
		if after[authID] != credit {
			t.Fatalf("credit for %q after rebuild = %d, want %d", authID, after[authID], credit)
		}
	}
}
