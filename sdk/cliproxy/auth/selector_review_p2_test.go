package auth

import (
	"context"
	"errors"
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
