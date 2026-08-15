package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// Regression tests mirrored from CLIProxyAPI PR #4881 follow-up
// (codex pullrequestreview-4943660625, findings 1 and 2). Finding 3 (ignored
// CompareAndReplaceGroup result) is CPA-specific: the CPAPlus affinity path
// uses CompareAndReplaceAliases and already checks its result, serving the
// selected auth statelessly when the CAS loses.

// TestCooldownErrorCountsOnlyEligibleAuths is a regression guard for the
// first finding: cooldownCount used to be compared against len(auths),
// including request-excluded entries, so a pool where every pickable auth was
// cooling reported the non-retryable auth_unavailable instead of
// model_cooldown with Retry-After.
func TestCooldownErrorCountsOnlyEligibleAuths(t *testing.T) {
	t.Parallel()

	model := "test-model"
	now := time.Now()
	next := now.Add(60 * time.Second)
	cooled := &Auth{
		ID: "auth-cooled",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusActive,
				Unavailable:    true,
				NextRetryAfter: next,
				Quota: QuotaState{
					Exceeded:      true,
					NextRecoverAt: next,
				},
			},
		},
	}
	excluded := &Auth{
		ID: "auth-excluded",
		ModelStates: map[string]*ModelState{
			model: {Status: StatusActive},
		},
	}

	_, err := getAvailableAuths([]*Auth{cooled, excluded}, "gemini", model, now, map[string]struct{}{"auth-excluded": {}})
	if err == nil {
		t.Fatal("getAvailableAuths() error = nil")
	}
	var mce *modelCooldownError
	if !errors.As(err, &mce) {
		t.Fatalf("getAvailableAuths() error = %T (%v), want *modelCooldownError: excluded auths must not count toward the cooldown decision", err, err)
	}
	if mce.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want %d", mce.StatusCode(), http.StatusTooManyRequests)
	}
	if got := mce.Headers().Get("Retry-After"); got == "" {
		t.Fatal("Headers().Get(Retry-After) = empty, want a value")
	}
}

// TestGetAvailableAuthsSkipsNilCandidates is a regression guard for the
// second finding: a nil entry in the auth list used to panic on candidate.ID
// when consulting the exclusion map.
func TestGetAvailableAuthsSkipsNilCandidates(t *testing.T) {
	t.Parallel()

	model := "test-model"
	active := &Auth{
		ID: "auth-active",
		ModelStates: map[string]*ModelState{
			model: {Status: StatusActive},
		},
	}

	got, err := getAvailableAuths([]*Auth{nil, active}, "gemini", model, time.Now())
	if err != nil {
		t.Fatalf("getAvailableAuths() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != active {
		t.Fatalf("getAvailableAuths() = %v, want [auth-active]", got)
	}

	_, err = getAvailableAuths([]*Auth{nil}, "gemini", model, time.Now(), map[string]struct{}{"anything": {}})
	if err == nil {
		t.Fatal("getAvailableAuths() with only a nil candidate: error = nil, want auth_unavailable")
	}
	var mce *modelCooldownError
	if errors.As(err, &mce) {
		t.Fatalf("getAvailableAuths() with only a nil candidate: error = %v, must not be modelCooldownError", err)
	}
}

// TestPickRebindsSplitAffinityGroupsOnFailover mirrors the CPA regression
// guard for the codex P2 finding on PR #4881. The CPAPlus binding design has
// no splitConflict skip: on a miss it rebinds the observed stale group via
// CompareAndReplaceAliases and absorbs both session keys into it, which
// converges the split groups onto the selected auth. This test locks that
// convergence in.
func TestPickRebindsSplitAffinityGroupsOnFailover(t *testing.T) {
	t.Parallel()

	model := "test-model"
	provider := "gemini"
	primaryKey := provider + "::pck:pk1::" + model
	fallbackKey := provider + "::conv:c1::" + model

	cooled := func(id string) *Auth {
		return &Auth{
			ID: id,
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusActive,
					Unavailable:    true,
					NextRetryAfter: time.Now().Add(60 * time.Second),
					Quota: QuotaState{
						Exceeded:      true,
						NextRecoverAt: time.Now().Add(60 * time.Second),
					},
				},
			},
		}
	}
	authA := cooled("auth-a")
	authB := cooled("auth-b")
	authC := &Auth{
		ID: "auth-c",
		ModelStates: map[string]*ModelState{
			model: {Status: StatusActive},
		},
	}

	selector := NewSessionAffinitySelector(&FillFirstSelector{})
	selector.cache.SetAliases("auth-a", primaryKey)
	selector.cache.SetAliases("auth-b", fallbackKey)

	payload := []byte(`{"prompt_cache_key":"pk1","conversation":{"id":"c1"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload, Metadata: map[string]any{}}
	auth, err := selector.Pick(context.Background(), provider, model, opts, []*Auth{authA, authB, authC})
	if err != nil {
		t.Fatalf("Pick() error = %v, want nil", err)
	}
	if auth != authC {
		t.Fatalf("Pick() = %v, want auth-c (only available auth)", auth.ID)
	}

	gotPrimary, genP, aliasesPrimary, okPrimary := selector.cache.GetWithGeneration(primaryKey)
	if !okPrimary || gotPrimary != "auth-c" {
		t.Fatalf("primary group after failover = %q (ok=%v), want auth-c", gotPrimary, okPrimary)
	}
	gotFallback, genF, _, okFallback := selector.cache.GetWithGeneration(fallbackKey)
	if !okFallback || gotFallback != "auth-c" {
		t.Fatalf("fallback group after failover = %q (ok=%v), want auth-c", gotFallback, okFallback)
	}
	if genP == 0 || genP != genF {
		t.Fatalf("split groups not merged into one: primary gen=%d, fallback gen=%d", genP, genF)
	}
	if !slices.Contains(aliasesPrimary, fallbackKey) {
		t.Fatalf("primary group aliases %v missing fallback key %q", aliasesPrimary, fallbackKey)
	}
}

// TestCompareAndDeleteGroupRejectsStaleObservation covers the codex P2
// follow-up on PR #4881: when a concurrent request refreshes or extends the
// fallback group between the observation and the delete, a stale merge
// observation must not remove the newer group, otherwise the newly attached
// aliases lose their affinity binding.
//
// Mirror of CLIProxyAPI dd8c72a3.
func TestCompareAndDeleteGroupRejectsStaleObservation(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(time.Minute)
	key := "gemini::conv:c1::test-model"
	extended := "gemini::conv:c2::test-model"
	cache.SetAliases("auth-a", key)

	authID, gen, aliases, ok := cache.GetWithGeneration(key)
	if !ok || authID != "auth-a" {
		t.Fatalf("GetWithGeneration() = %q, ok=%v; want auth-a bound", authID, ok)
	}

	// A concurrent request extends the same group on the same auth.
	cache.SetAliases("auth-a", key, extended)

	if removed := cache.CompareAndDeleteGroup(key, "auth-a", gen, aliases); removed != nil {
		t.Fatalf("CompareAndDeleteGroup with stale observation removed %v; want nil (group must survive)", removed)
	}
	if got, _, gotAliases, ok := cache.GetWithGeneration(key); !ok || got != "auth-a" || !slices.Contains(gotAliases, extended) {
		t.Fatalf("group after stale delete = %q, aliases=%v, ok=%v; want intact auth-a group", got, gotAliases, ok)
	}

	// A fresh observation still deletes successfully.
	authID, gen, aliases, ok = cache.GetWithGeneration(key)
	if !ok {
		t.Fatal("GetWithGeneration() after extension lost the group")
	}
	if removed := cache.CompareAndDeleteGroup(key, authID, gen, aliases); removed == nil {
		t.Fatal("CompareAndDeleteGroup with fresh observation returned nil; want removed aliases")
	}
	if _, _, _, ok := cache.GetWithGeneration(key); ok {
		t.Fatal("group still present after fresh CompareAndDeleteGroup")
	}
}

// TestMergeSplitGroupsRetainsFallbackAliasesUnderCASContention covers the
// codex P2 follow-up on PR #4881: the fallback delete commits before the
// primary CAS, so a primary CAS that loses to a concurrent writer must not
// rebuild merged from cacheKey and fallbackKey alone — the fallback group's
// historical aliases have to survive via the retained observation.
//
// The contending writer uses CompareAndReplaceAliases itself, so its bump
// only lands while the primary group is still in its pre-merge state; once
// the merge commits, the contender's CAS refuses and cannot corrupt the
// result. Both interleavings are therefore valid and the assertions hold
// either way.
//
// Mirror of CLIProxyAPI e768fba9.
func TestMergeSplitGroupsRetainsFallbackAliasesUnderCASContention(t *testing.T) {
	t.Parallel()

	for i := 0; i < 2000; i++ {
		selector := NewSessionAffinitySelector(&FillFirstSelector{})
		cacheKey := fmt.Sprintf("gemini::pck:pk%d::test-model", i)
		fallbackKey := fmt.Sprintf("gemini::conv:c1-%d::test-model", i)
		historical := fmt.Sprintf("gemini::conv:c0-%d::test-model", i)
		scratch := fmt.Sprintf("gemini::scratch:%d::test-model", i)
		selector.cache.SetAliases("auth-a", cacheKey)
		selector.cache.SetAliases("auth-b", fallbackKey, historical)

		// A concurrent writer attaches a scratch alias to the primary group
		// right after the fallback group disappears — the interleaving that
		// makes the merge's first primary CAS lose.
		done := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			defer close(done)
			bumps := 0
			for {
				select {
				case <-finished:
					return
				default:
				}
				if _, _, _, ok := selector.cache.GetWithGeneration(fallbackKey); ok {
					continue
				}
				if bumps >= 2 {
					return
				}
				authP, genP, aliasesP, okP := selector.cache.GetWithGeneration(cacheKey)
				if okP {
					bumped := append(append([]string(nil), aliasesP...), scratch)
					if selector.cache.CompareAndReplaceAliases(authP, genP, aliasesP, authP, bumped...) {
						bumps++
					}
				}
			}
		}()

		merged := selector.mergeSplitAliasGroupsCAS(cacheKey, fallbackKey, "auth-c")
		close(finished)
		<-done
		if !merged {
			t.Fatalf("iteration %d: mergeSplitAliasGroupsCAS() = false under single-bump contention, want true", i)
		}
		got, _, aliases, ok := selector.cache.GetWithGeneration(cacheKey)
		if !ok || got != "auth-c" {
			t.Fatalf("iteration %d: merged group = %q, ok=%v; want auth-c", i, got, ok)
		}
		if !slices.Contains(aliases, historical) {
			t.Fatalf("iteration %d: merged aliases %v missing historical fallback alias %q", i, aliases, historical)
		}
	}
}

func TestSessionAffinitySelector_SplitGroupMergeExhaustionRestoresFallbackAliases(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	provider := "responses-split-exhaustion"
	model := "gpt-test"

	cacheKey := provider + "::pck:shared-prompt::" + model
	fallbackKey := provider + "::conv:conversation-session::" + model
	extraFallbackAlias := provider + "::extra:alias::" + model
	missingPrimaryAlias := provider + "::conv:missing::" + model

	// 1. Group 1 on cacheKey + missingPrimaryAlias bound to auth-a
	selector.cache.SetAliases("auth-a", cacheKey, missingPrimaryAlias)

	// Invalidate missingPrimaryAlias from entries table while leaving cacheKey's entry
	// expecting it, so CompareAndReplaceAliases on cacheKey fails on all CAS attempts.
	selector.cache.mu.Lock()
	delete(selector.cache.entries, missingPrimaryAlias)
	selector.cache.mu.Unlock()

	// 2. Group 2 on fallbackKey + extraFallbackAlias bound to auth-b
	selector.cache.SetAliases("auth-b", fallbackKey, extraFallbackAlias)

	// 3. mergeSplitAliasGroupsCAS attempts to merge cacheKey and fallbackKey into auth-c.
	// Since cacheKey expects missingPrimaryAlias which is missing from entries,
	// CompareAndReplaceAliases fails on all 3 attempts.
	merged := selector.mergeSplitAliasGroupsCAS(cacheKey, fallbackKey, "auth-c")
	if merged {
		t.Fatalf("mergeSplitAliasGroupsCAS must fail due to CAS exhaustion")
	}

	// Since mergeSplitAliasGroupsCAS exhausted retries after deleting fallbackKey,
	// the fallback group (fallbackKey and extraFallbackAlias) must be restored to auth-b.
	if got, ok := selector.cache.Get(fallbackKey); !ok || got != "auth-b" {
		t.Fatalf("fallbackKey must be restored to auth-b, got %q, %v", got, ok)
	}
	if got, ok := selector.cache.Get(extraFallbackAlias); !ok || got != "auth-b" {
		t.Fatalf("extraFallbackAlias must be restored to auth-b, got %q, %v", got, ok)
	}
}

func TestSessionAffinitySelector_SplitGroupMergeExhaustionDoesNotClobberConcurrentRebind(t *testing.T) {
	for i := 0; i < 500; i++ {
		selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
			Fallback: &RoundRobinSelector{},
			TTL:      time.Minute,
		})
		provider := fmt.Sprintf("responses-split-exhaustion-concurrent-%d", i)
		model := "gpt-test"

		cacheKey := provider + "::pck:shared-prompt::" + model
		fallbackKey := provider + "::conv:conversation-session::" + model
		extraFallbackAlias := provider + "::extra:alias::" + model
		missingPrimaryAlias := provider + "::conv:missing::" + model

		// 1. Group 1 on cacheKey + missingPrimaryAlias bound to auth-a
		selector.cache.SetAliases("auth-a", cacheKey, missingPrimaryAlias)

		// Invalidate missingPrimaryAlias from entries table while leaving cacheKey's entry
		// expecting it, so CompareAndReplaceAliases on cacheKey fails on all CAS attempts.
		selector.cache.mu.Lock()
		delete(selector.cache.entries, missingPrimaryAlias)
		selector.cache.mu.Unlock()

		// 2. Group 2 on fallbackKey + extraFallbackAlias bound to auth-b
		selector.cache.SetAliases("auth-b", fallbackKey, extraFallbackAlias)

		// Start a concurrent goroutine that observes when fallbackKey is deleted by attempt 0,
		// and immediately rebinds extraFallbackAlias to auth-x.
		done := make(chan struct{})
		finished := make(chan struct{})
		rebound := false
		go func() {
			defer close(done)
			for {
				select {
				case <-finished:
					return
				default:
				}
				if _, ok := selector.cache.Get(fallbackKey); ok {
					continue
				}
				selector.cache.SetAliases("auth-x", extraFallbackAlias)
				rebound = true
				return
			}
		}()

		// 3. mergeSplitAliasGroupsCAS attempts to merge cacheKey and fallbackKey into auth-c.
		// Since cacheKey expects missingPrimaryAlias which is missing from entries,
		// CompareAndReplaceAliases fails on all 3 attempts.
		merged := selector.mergeSplitAliasGroupsCAS(cacheKey, fallbackKey, "auth-c")
		close(finished)
		<-done
		if merged {
			t.Fatalf("iteration %d: mergeSplitAliasGroupsCAS must fail due to CAS exhaustion", i)
		}

		if rebound {
			if got, ok := selector.cache.Get(extraFallbackAlias); !ok || got != "auth-x" {
				t.Fatalf("iteration %d: concurrent rebind extraFallbackAlias clobbered; got %q, %v, want auth-x", i, got, ok)
			}
		}
		selector.Stop()
	}
}
