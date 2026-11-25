package auth

import (
	"context"
	"reflect"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConductorLifecycleExecutorRegistrationNormalizesCase(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	first := &replaceAwareExecutor{id: "  MixedCaseProvider  "}

	rev1 := m.RegisterExecutorWithRevision(first)
	if rev1 != 1 {
		t.Fatalf("expected initial revision 1, got %d", rev1)
	}
	if got := m.ExecutorRevision("MixedCaseProvider"); got != 1 {
		t.Fatalf("expected ExecutorRevision(mixed-case) == 1, got %d", got)
	}
	if got := m.ExecutorRevision("mixedcaseprovider"); got != 1 {
		t.Fatalf("expected ExecutorRevision(lowercase) == 1, got %d", got)
	}
	if exec, ok := m.Executor("mixedcaseprovider"); !ok || exec != first {
		t.Fatalf("expected Executor(lowercase) to return registered executor, ok=%v exec=%v", ok, exec)
	}
	if exec, ok := m.Executor("MixedCaseProvider"); !ok || exec != first {
		t.Fatalf("expected Executor(mixed-case) to return registered executor, ok=%v exec=%v", ok, exec)
	}

	second := &replaceAwareExecutor{id: "mixedcaseprovider"}
	rev2 := m.RegisterExecutorWithRevision(second)
	if rev2 != 2 {
		t.Fatalf("expected replacement revision 2, got %d", rev2)
	}
	if got := m.ExecutorRevision("MixedCaseProvider"); got != 2 {
		t.Fatalf("expected ExecutorRevision after replacement == 2, got %d", got)
	}
	if closed := first.ClosedSessionIDs(); len(closed) != 1 || closed[0] != CloseAllExecutionSessionsID {
		t.Fatalf("expected replaced mixed-case executor sessions closed once, got %v", closed)
	}

	m.UnregisterExecutor("MixedCaseProvider")
	if _, ok := m.Executor("mixedcaseprovider"); ok {
		t.Fatalf("expected Executor(lowercase) to be unregistered")
	}
	if _, ok := m.Executor("MixedCaseProvider"); ok {
		t.Fatalf("expected Executor(mixed-case) to be unregistered")
	}
	if got := m.ExecutorRevision("mixedcaseprovider"); got != 3 {
		t.Fatalf("expected ExecutorRevision after unregister == 3, got %d", got)
	}
}

func newMultiModelPoolTestManager() *Manager {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name: "CompatA",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "upstream-a1", Alias: "shared-alias"},
					{Name: "upstream-a2", Alias: "shared-alias"},
					{Name: "upstream-a3", Alias: "shared-alias"},
					{Name: "upstream-b1", Alias: "second-alias"},
					{Name: "upstream-b2", Alias: "second-alias"},
				},
			},
			{
				Name: "CompatB",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "upstream-c1", Alias: "shared-alias"},
					{Name: "upstream-c2", Alias: "shared-alias"},
					{Name: "upstream-c3", Alias: "shared-alias"},
				},
			},
		},
	})
	return m
}

func newCompatTestAuth(id, compatName string) *Auth {
	return &Auth{
		ID:       id,
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":     "test-key-" + id,
			"compat_name": compatName,
		},
	}
}

func TestConductorLifecycleRemoveIfCleansModelPoolOffsets(t *testing.T) {
	t.Parallel()

	ctx := WithSkipPersist(context.Background())
	m := newMultiModelPoolTestManager()

	auth1, errRegister1 := m.Register(ctx, newCompatTestAuth("Auth-1", "CompatA"))
	if errRegister1 != nil {
		t.Fatalf("register auth1: %v", errRegister1)
	}
	auth10, errRegister10 := m.Register(ctx, newCompatTestAuth("Auth-10", "CompatA"))
	if errRegister10 != nil {
		t.Fatalf("register auth10: %v", errRegister10)
	}

	got1, _, _, _ := m.executionModelCandidatesWithAlias(auth1, "shared-alias")
	if want := []string{"upstream-a1", "upstream-a2", "upstream-a3"}; !reflect.DeepEqual(got1, want) {
		t.Fatalf("initial auth1 rotation = %v, want %v", got1, want)
	}
	_, _, _, _ = m.executionModelCandidatesWithAlias(auth1, "second-alias")

	got10First, _, _, _ := m.executionModelCandidatesWithAlias(auth10, "shared-alias")
	if want := []string{"upstream-a1", "upstream-a2", "upstream-a3"}; !reflect.DeepEqual(got10First, want) {
		t.Fatalf("initial auth10 rotation = %v, want %v", got10First, want)
	}

	if removed := m.RemoveIf(ctx, "Auth-1", nil); !removed {
		t.Fatalf("expected RemoveIf(Auth-1) to return true")
	}

	m.mu.RLock()
	remainingAuthCount := len(m.modelPoolOffsets)
	m.mu.RUnlock()
	if remainingAuthCount != 1 {
		t.Fatalf("expected only auth10 offset state to remain after RemoveIf(Auth-1), got map len %d", remainingAuthCount)
	}

	got10Next, _, _, _ := m.executionModelCandidatesWithAlias(auth10, "shared-alias")
	if want := []string{"upstream-a2", "upstream-a3", "upstream-a1"}; !reflect.DeepEqual(got10Next, want) {
		t.Fatalf("expected auth10 to continue rotating from prior offset, got %v, want %v", got10Next, want)
	}
}

func TestConductorLifecycleRemoveIfPreservesPipeAndCaseDistinctAuthOffsets(t *testing.T) {
	t.Parallel()

	t.Run("pipe_in_auth_id", func(t *testing.T) {
		t.Parallel()

		ctx := WithSkipPersist(context.Background())
		m := newMultiModelPoolTestManager()

		teamAuth, errTeam := m.Register(ctx, newCompatTestAuth("team", "CompatA"))
		if errTeam != nil {
			t.Fatalf("register team: %v", errTeam)
		}
		teamEastAuth, errEast := m.Register(ctx, newCompatTestAuth("team|east", "CompatA"))
		if errEast != nil {
			t.Fatalf("register team|east: %v", errEast)
		}

		_, _, _, _ = m.executionModelCandidatesWithAlias(teamAuth, "shared-alias")
		firstEast, _, _, _ := m.executionModelCandidatesWithAlias(teamEastAuth, "shared-alias")
		if want := []string{"upstream-a1", "upstream-a2", "upstream-a3"}; !reflect.DeepEqual(firstEast, want) {
			t.Fatalf("initial team|east rotation = %v, want %v", firstEast, want)
		}

		if removed := m.RemoveIf(ctx, "team", nil); !removed {
			t.Fatalf("expected RemoveIf(team) to return true")
		}

		nextEast, _, _, _ := m.executionModelCandidatesWithAlias(teamEastAuth, "shared-alias")
		if want := []string{"upstream-a2", "upstream-a3", "upstream-a1"}; !reflect.DeepEqual(nextEast, want) {
			t.Fatalf("expected team|east to continue rotating from prior offset after removing team, got %v, want %v", nextEast, want)
		}
	})

	t.Run("case_distinct_auth_ids", func(t *testing.T) {
		t.Parallel()

		ctx := WithSkipPersist(context.Background())
		m := newMultiModelPoolTestManager()

		upperAuth, errUpper := m.Register(ctx, newCompatTestAuth("Team", "CompatA"))
		if errUpper != nil {
			t.Fatalf("register Team: %v", errUpper)
		}
		lowerAuth, errLower := m.Register(ctx, newCompatTestAuth("team", "CompatB"))
		if errLower != nil {
			t.Fatalf("register team: %v", errLower)
		}

		_, _, _, _ = m.executionModelCandidatesWithAlias(upperAuth, "shared-alias")
		firstLower, _, _, _ := m.executionModelCandidatesWithAlias(lowerAuth, "shared-alias")
		if want := []string{"upstream-c1", "upstream-c2", "upstream-c3"}; !reflect.DeepEqual(firstLower, want) {
			t.Fatalf("initial team rotation = %v, want %v", firstLower, want)
		}

		if removed := m.RemoveIf(ctx, "Team", nil); !removed {
			t.Fatalf("expected RemoveIf(Team) to return true")
		}

		nextLower, _, _, _ := m.executionModelCandidatesWithAlias(lowerAuth, "shared-alias")
		if want := []string{"upstream-c2", "upstream-c3", "upstream-c1"}; !reflect.DeepEqual(nextLower, want) {
			t.Fatalf("expected lowercase team to continue rotating from prior offset after removing Team, got %v, want %v", nextLower, want)
		}
	})
}

func TestConductorLifecycleStaleSnapshotDoesNotRecreateModelPoolOffsets(t *testing.T) {
	t.Parallel()

	ctx := WithSkipPersist(context.Background())
	m := newMultiModelPoolTestManager()

	registered, errRegister := m.Register(ctx, newCompatTestAuth("Auth-Stale", "CompatA"))
	if errRegister != nil {
		t.Fatalf("register Auth-Stale: %v", errRegister)
	}
	staleSnapshot, ok := m.GetByID(registered.ID)
	if !ok || staleSnapshot == nil {
		t.Fatalf("expected GetByID to return registered auth")
	}

	_, _, _, _ = m.executionModelCandidatesWithAlias(staleSnapshot, "shared-alias")

	if removed := m.RemoveIf(ctx, registered.ID, nil); !removed {
		t.Fatalf("expected RemoveIf to return true")
	}

	m.mu.RLock()
	afterRemoveLen := len(m.modelPoolOffsets)
	m.mu.RUnlock()
	if afterRemoveLen != 0 {
		t.Fatalf("expected modelPoolOffsets to be empty after RemoveIf, got len %d", afterRemoveLen)
	}

	// Simulate an in-flight request resolving the multi-model pool with the pre-removal snapshot.
	_, _, _, _ = m.executionModelCandidatesWithAlias(staleSnapshot, "shared-alias")
	_ = m.ResolveExecutionModel(staleSnapshot, "shared-alias")

	m.mu.RLock()
	afterStaleLen := len(m.modelPoolOffsets)
	m.mu.RUnlock()
	if afterStaleLen != 0 {
		t.Fatalf("expected stale snapshot resolution not to recreate modelPoolOffsets entries, got len %d", afterStaleLen)
	}

	// Re-registering the same ID with a new epoch must not let the old snapshot advance its pool offset.
	reRegistered, errReRegister := m.Register(ctx, newCompatTestAuth("Auth-Stale", "CompatA"))
	if errReRegister != nil {
		t.Fatalf("re-register Auth-Stale: %v", errReRegister)
	}
	_, _, _, _ = m.executionModelCandidatesWithAlias(staleSnapshot, "shared-alias")
	firstFresh, _, _, _ := m.executionModelCandidatesWithAlias(reRegistered, "shared-alias")
	if want := []string{"upstream-a1", "upstream-a2", "upstream-a3"}; !reflect.DeepEqual(firstFresh, want) {
		t.Fatalf("expected stale snapshot not to advance re-registered auth offset, got %v, want %v", firstFresh, want)
	}
}
