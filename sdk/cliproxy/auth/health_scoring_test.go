package auth

import (
	"testing"
	"time"
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

	applyHealthFailure(&health, now, 429)
	if health.BreakerState != HealthBreakerClosed {
		t.Fatalf("after first 429 BreakerState = %q, want %q", health.BreakerState, HealthBreakerClosed)
	}

	applyHealthFailure(&health, now.Add(30*time.Second), 429)
	if health.BreakerState != HealthBreakerOpen {
		t.Fatalf("after second 429 BreakerState = %q, want %q", health.BreakerState, HealthBreakerOpen)
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
