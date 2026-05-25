package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetAvailableAuths_PrefersHealthierAuthWithinSamePriority(t *testing.T) {
	t.Parallel()

	now := time.Now()
	authA := &Auth{
		ID:         "a",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
	}
	authB := &Auth{
		ID:         "b",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
	}

	applyHealthFailure(&authA.Health, now, 429)

	available, err := getAvailableAuths([]*Auth{authA, authB}, "claude", "claude-sonnet-4-6", now)
	if err != nil {
		t.Fatalf("getAvailableAuths() error = %v", err)
	}
	if len(available) != 1 {
		t.Fatalf("getAvailableAuths() len = %d, want 1 healthy auth in the top tier", len(available))
	}
	if available[0].ID != "b" {
		t.Fatalf("getAvailableAuths() auth.ID = %q, want %q", available[0].ID, "b")
	}
}

func TestGetAvailableAuths_HealthScoreRecoversOverTime(t *testing.T) {
	t.Parallel()

	now := time.Now()
	authA := &Auth{
		ID:         "a",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
	}
	authB := &Auth{
		ID:         "b",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
	}

	applyHealthFailure(&authA.Health, now, 429)

	availableNow, err := getAvailableAuths([]*Auth{authA, authB}, "claude", "claude-sonnet-4-6", now)
	if err != nil {
		t.Fatalf("getAvailableAuths(now) error = %v", err)
	}
	if len(availableNow) != 1 || availableNow[0].ID != "b" {
		t.Fatalf("getAvailableAuths(now) = %+v, want only auth b in the top tier", availableNow)
	}

	recoveredAt := now.Add(20 * time.Minute)
	availableRecovered, err := getAvailableAuths([]*Auth{authA, authB}, "claude", "claude-sonnet-4-6", recoveredAt)
	if err != nil {
		t.Fatalf("getAvailableAuths(recovered) error = %v", err)
	}
	if len(availableRecovered) != 2 {
		t.Fatalf("getAvailableAuths(recovered) len = %d, want 2 after recovery", len(availableRecovered))
	}
	if availableRecovered[0].ID != "a" || availableRecovered[1].ID != "b" {
		t.Fatalf("getAvailableAuths(recovered) ids = [%s %s], want [a b]", availableRecovered[0].ID, availableRecovered[1].ID)
	}
}

func TestGetAvailableAuths_ModelHealthIsScopedPerModel(t *testing.T) {
	t.Parallel()

	now := time.Now()
	authA := &Auth{
		ID:         "a",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
		ModelStates: map[string]*ModelState{
			"gpt-5.4": {
				Health: HealthState{
					Observed:      true,
					Score:         20,
					LastUpdatedAt: now,
					LastFailureAt: now,
				},
			},
		},
	}
	authB := &Auth{
		ID:         "b",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
	}

	availableBadModel, err := getAvailableAuths([]*Auth{authA, authB}, "codex", "gpt-5.4", now)
	if err != nil {
		t.Fatalf("getAvailableAuths(gpt-5.4) error = %v", err)
	}
	if len(availableBadModel) != 1 || availableBadModel[0].ID != "b" {
		t.Fatalf("getAvailableAuths(gpt-5.4) = %+v, want only auth b in top tier", availableBadModel)
	}

	availableOtherModel, err := getAvailableAuths([]*Auth{authA, authB}, "codex", "gpt-5.3", now)
	if err != nil {
		t.Fatalf("getAvailableAuths(gpt-5.3) error = %v", err)
	}
	if len(availableOtherModel) != 2 {
		t.Fatalf("getAvailableAuths(gpt-5.3) len = %d, want 2", len(availableOtherModel))
	}
	if availableOtherModel[0].ID != "a" || availableOtherModel[1].ID != "b" {
		t.Fatalf("getAvailableAuths(gpt-5.3) ids = [%s %s], want [a b]", availableOtherModel[0].ID, availableOtherModel[1].ID)
	}
}

func TestApplyHealthFailure_FatalStatusOpensCircuitImmediately(t *testing.T) {
	t.Parallel()

	now := time.Now()
	var health HealthState
	applyHealthFailure(&health, now, 401)

	if health.BreakerState != HealthBreakerOpen {
		t.Fatalf("BreakerState = %q, want %q", health.BreakerState, HealthBreakerOpen)
	}
	if health.OpenUntil.IsZero() || !health.OpenUntil.After(now) {
		t.Fatalf("OpenUntil = %v, want future time", health.OpenUntil)
	}
}

func TestApplyHealthFailure_Repeated429OpensCircuit(t *testing.T) {
	t.Parallel()

	now := time.Now()
	var health HealthState

	for attempt := 1; attempt < health429OpenFailures; attempt++ {
		applyHealthFailure(&health, now.Add(time.Duration(attempt-1)*30*time.Second), 429)
		if health.BreakerState != HealthBreakerClosed {
			t.Fatalf("after %d 429s BreakerState = %q, want %q", attempt, health.BreakerState, HealthBreakerClosed)
		}
	}

	applyHealthFailure(&health, now.Add(time.Duration(health429OpenFailures-1)*30*time.Second), 429)
	if health.BreakerState != HealthBreakerOpen {
		t.Fatalf("after %d 429s BreakerState = %q, want %q", health429OpenFailures, health.BreakerState, HealthBreakerOpen)
	}
	if health.OpenUntil.IsZero() || !health.OpenUntil.After(now) {
		t.Fatalf("OpenUntil = %v, want future time", health.OpenUntil)
	}
}

func TestManagerAvailableAuthsForRouteModel_HealthOpenBlocksSelection(t *testing.T) {
	t.Parallel()

	now := time.Now()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	authA := &Auth{
		ID:         "a",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
		Health: HealthState{
			Observed:     true,
			Score:        30,
			BreakerState: HealthBreakerOpen,
			OpenUntil:    now.Add(2 * time.Minute),
		},
	}
	authB := &Auth{
		ID:         "b",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
	}

	available, err := manager.availableAuthsForRouteModel([]*Auth{authA, authB}, "claude", "claude-sonnet-4-6", now)
	if err != nil {
		t.Fatalf("availableAuthsForRouteModel() error = %v", err)
	}
	if len(available) != 1 || available[0].ID != "b" {
		t.Fatalf("availableAuthsForRouteModel() = %+v, want only auth b", available)
	}
}

func TestManagerAvailableAuthsForRouteModel_SpreadKeepsLowerPriorityCandidates(t *testing.T) {
	t.Parallel()

	now := time.Now()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			ProviderStrategies: map[string]string{
				"claude": "spread",
			},
		},
	})
	authA := &Auth{
		ID:         "a",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "20"},
	}
	authB := &Auth{
		ID:         "b",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "5"},
	}

	available, err := manager.availableAuthsForRouteModel([]*Auth{authA, authB}, "claude", "claude-sonnet-4-6", now)
	if err != nil {
		t.Fatalf("availableAuthsForRouteModel() error = %v", err)
	}
	if len(available) != 2 {
		t.Fatalf("availableAuthsForRouteModel() len = %d, want 2 spread candidates across priority buckets", len(available))
	}
	if available[0].ID != "a" || available[1].ID != "b" {
		t.Fatalf("availableAuthsForRouteModel() ids = [%s %s], want [a b]", available[0].ID, available[1].ID)
	}
}

func TestManagerReserveHalfOpenProbe_AllowsSingleProbePerInterval(t *testing.T) {
	t.Parallel()

	now := time.Now()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	ok, next := manager.reserveHalfOpenProbe("auth-a", "gpt-5.4", now)
	if !ok {
		t.Fatal("first reserveHalfOpenProbe() = false, want true")
	}
	if next.IsZero() || !next.After(now) {
		t.Fatalf("first reserveHalfOpenProbe() next = %v, want future time", next)
	}

	ok, blockedUntil := manager.reserveHalfOpenProbe("auth-a", "gpt-5.4", now.Add(5*time.Second))
	if ok {
		t.Fatal("second reserveHalfOpenProbe() = true, want false inside half-open interval")
	}
	if blockedUntil.IsZero() || !blockedUntil.After(now) {
		t.Fatalf("second reserveHalfOpenProbe() blockedUntil = %v, want future time", blockedUntil)
	}
}

func TestManagerReserveHalfOpenProbe_PrunesExpiredProbeState(t *testing.T) {
	t.Parallel()

	now := time.Now()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.halfOpenProbeMu.Lock()
	for i := 0; i < halfOpenProbeStateLimit+10; i++ {
		key := halfOpenProbeKey(fmt.Sprintf("auth-%d", i), "gpt-5.4")
		manager.halfOpenProbeNext[key] = now.Add(-time.Second)
		manager.halfOpenProbeActiveUntil[key] = now.Add(-time.Second)
	}
	manager.halfOpenProbeMu.Unlock()

	ok, _ := manager.reserveHalfOpenProbe("fresh-auth", "gpt-5.4", now)
	if !ok {
		t.Fatal("reserveHalfOpenProbe() = false, want true after pruning expired state")
	}

	manager.halfOpenProbeMu.Lock()
	nextLen := len(manager.halfOpenProbeNext)
	activeLen := len(manager.halfOpenProbeActiveUntil)
	manager.halfOpenProbeMu.Unlock()
	if nextLen != 1 || activeLen != 1 {
		t.Fatalf("probe state len = next:%d active:%d, want 1/1", nextLen, activeLen)
	}
}

func TestManagerAvailableAuthsForRouteModel_AllCoolingUsesLowFrequencyProbe(t *testing.T) {
	t.Parallel()

	now := time.Now()
	const model = "claude-sonnet-4-6"
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	authA := &Auth{
		ID:         "a",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusActive,
				Unavailable:    true,
				NextRetryAfter: now.Add(2 * time.Minute),
				Quota:          QuotaState{Exceeded: true},
			},
		},
	}
	authB := &Auth{
		ID:         "b",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusActive,
				Unavailable:    true,
				NextRetryAfter: now.Add(3 * time.Minute),
				Quota:          QuotaState{Exceeded: true},
			},
		},
	}

	available, err := manager.availableAuthsForRouteModel([]*Auth{authA, authB}, "claude", model, now)
	if err != nil {
		t.Fatalf("availableAuthsForRouteModel(first probe) error = %v", err)
	}
	if len(available) != 1 || available[0].ID != "a" {
		t.Fatalf("availableAuthsForRouteModel(first probe) = %+v, want auth a", available)
	}
	models := manager.filterExecutionModels(available[0], model, []string{model}, false)
	if len(models) != 1 || models[0] != model {
		t.Fatalf("filterExecutionModels(first probe) = %+v, want fallback probe model", models)
	}

	available, err = manager.availableAuthsForRouteModel([]*Auth{authA, authB}, "claude", model, now.Add(time.Second))
	if err != nil {
		t.Fatalf("availableAuthsForRouteModel(second probe) error = %v", err)
	}
	if len(available) != 2 || available[0].ID != "a" || available[1].ID != "b" {
		t.Fatalf("availableAuthsForRouteModel(second probe) = %+v, want active auth a and new probe auth b", available)
	}

	available, err = manager.availableAuthsForRouteModel([]*Auth{authA, authB}, "claude", model, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("availableAuthsForRouteModel(active probe window) error = %v", err)
	}
	if len(available) != 2 || available[0].ID != "a" || available[1].ID != "b" {
		t.Fatalf("availableAuthsForRouteModel(active probe window) = %+v, want both active probes", available)
	}

	available, err = manager.availableAuthsForRouteModel([]*Auth{authA, authB}, "claude", model, now.Add(healthHalfOpenActiveTTL+time.Second))
	if err != nil {
		t.Fatalf("availableAuthsForRouteModel(quota active probe window) error = %v", err)
	}
	if len(available) != 2 || available[0].ID != "a" || available[1].ID != "b" {
		t.Fatalf("availableAuthsForRouteModel(quota active probe window) = %+v, want both quota probes", available)
	}

	available, err = manager.availableAuthsForRouteModel([]*Auth{authA, authB}, "claude", model, now.Add(quotaHalfOpenActiveTTL+time.Second))
	if err != nil {
		t.Fatalf("availableAuthsForRouteModel(next quota probe interval) error = %v", err)
	}
	if len(available) != 1 || available[0].ID != "a" {
		t.Fatalf("availableAuthsForRouteModel(next quota probe interval) = %+v, want auth a", available)
	}
}

func TestManagerAvailableAuthsForRouteModel_CodexIgnoresLocalCooling(t *testing.T) {
	t.Parallel()

	now := time.Now()
	const model = "gpt-5.5"
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	authA := &Auth{
		ID:         "a",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				Quota:          QuotaState{Exceeded: true},
				Health: HealthState{
					Observed:     true,
					Score:        10,
					BreakerState: HealthBreakerOpen,
					OpenUntil:    now.Add(30 * time.Minute),
				},
			},
		},
	}
	authB := &Auth{
		ID:             "b",
		Provider:       "codex",
		Attributes:     map[string]string{"priority": "10"},
		Unavailable:    true,
		NextRetryAfter: now.Add(30 * time.Minute),
		Quota:          QuotaState{Exceeded: true},
		Health:         HealthState{Observed: true, Score: 10, BreakerState: HealthBreakerOpen, OpenUntil: now.Add(30 * time.Minute)},
	}
	disabled := &Auth{ID: "disabled", Provider: "codex", Disabled: true}

	available, err := manager.availableAuthsForRouteModel([]*Auth{authA, authB, disabled}, "codex", model, now)
	if err != nil {
		t.Fatalf("availableAuthsForRouteModel() error = %v", err)
	}
	if len(available) != 2 {
		t.Fatalf("availableAuthsForRouteModel() len = %d, want 2 codex auths despite local cooling", len(available))
	}
	if available[0].ID != "a" || available[1].ID != "b" {
		t.Fatalf("availableAuthsForRouteModel() ids = [%s %s], want [a b]", available[0].ID, available[1].ID)
	}

	models := manager.filterExecutionModels(authA, model, []string{model}, false)
	if len(models) != 1 || models[0] != model {
		t.Fatalf("filterExecutionModels() = %+v, want codex model despite local cooling", models)
	}
}

func TestManagerMarkResult_CodexFailureDoesNotCooldown(t *testing.T) {
	t.Parallel()

	now := time.Now()
	const model = "gpt-5.5"
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.auths["codex-a"] = &Auth{
		ID:             "codex-a",
		Provider:       "codex",
		Status:         StatusActive,
		Unavailable:    true,
		NextRetryAfter: now.Add(30 * time.Minute),
		Quota:          QuotaState{Exceeded: true, NextRecoverAt: now.Add(30 * time.Minute), BackoffLevel: 2},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				Quota:          QuotaState{Exceeded: true, NextRecoverAt: now.Add(30 * time.Minute), BackoffLevel: 2},
				Health:         HealthState{Observed: true, Score: 10, BreakerState: HealthBreakerOpen, OpenUntil: now.Add(30 * time.Minute)},
			},
		},
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "codex-a",
		Provider: "codex",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: 429, Message: "upstream rate limited"},
	})

	updated := manager.auths["codex-a"]
	if updated.Unavailable || !updated.NextRetryAfter.IsZero() || updated.Quota.Exceeded {
		t.Fatalf("codex auth cooling = unavailable:%v next:%v quota:%+v, want clear", updated.Unavailable, updated.NextRetryAfter, updated.Quota)
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("codex model state missing")
	}
	if state.Unavailable || !state.NextRetryAfter.IsZero() || state.Quota.Exceeded || state.Status != StatusActive {
		t.Fatalf("codex model cooling = status:%s unavailable:%v next:%v quota:%+v, want active and clear", state.Status, state.Unavailable, state.NextRetryAfter, state.Quota)
	}
	if state.Health.BreakerState != "" || state.Health.Observed {
		t.Fatalf("codex model health = %+v, want clear", state.Health)
	}
}

func TestManagerReserveCodexModelSlot_DoesNotHardLimitModels(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.auths["codex-a"] = &Auth{ID: "codex-a", Provider: "codex"}

	releaseGPT54, err := manager.reserveCodexModelSlot("codex", "gpt-5.4")
	if err != nil {
		t.Fatalf("first reserveCodexModelSlot(gpt-5.4) error = %v", err)
	}
	defer releaseGPT54()

	releaseGPT54Second, err := manager.reserveCodexModelSlot("codex", "gpt-5.4")
	if err != nil {
		t.Fatalf("second reserveCodexModelSlot(gpt-5.4) error = %v, want soft tracking only", err)
	}
	defer releaseGPT54Second()

	releaseGPT55, err := manager.reserveCodexModelSlot("codex", "gpt-5.5")
	if err != nil {
		t.Fatalf("reserveCodexModelSlot(gpt-5.5) error = %v, want isolated model slot", err)
	}
	defer releaseGPT55()
}

func TestApplyHealthSuccess_HalfOpenNeedsTwoSuccessesToClose(t *testing.T) {
	t.Parallel()

	now := time.Now()
	health := HealthState{
		Observed:     true,
		Score:        40,
		BreakerState: HealthBreakerOpen,
		OpenUntil:    now.Add(-1 * time.Second),
	}

	applyHealthSuccess(&health, now)
	if health.BreakerState != HealthBreakerHalfOpen {
		t.Fatalf("after first probe success BreakerState = %q, want %q", health.BreakerState, HealthBreakerHalfOpen)
	}
	if health.HalfOpenSuccesses != 1 {
		t.Fatalf("after first probe success HalfOpenSuccesses = %d, want 1", health.HalfOpenSuccesses)
	}

	applyHealthSuccess(&health, now.Add(healthHalfOpenInterval))
	if health.BreakerState != HealthBreakerClosed {
		t.Fatalf("after second probe success BreakerState = %q, want %q", health.BreakerState, HealthBreakerClosed)
	}
	if health.HalfOpenSuccesses != 0 {
		t.Fatalf("after second probe success HalfOpenSuccesses = %d, want 0", health.HalfOpenSuccesses)
	}
}
