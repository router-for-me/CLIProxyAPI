package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// rankedOnceSelectionModel is the route model used while selecting. It is empty on
// purpose: per-model routing is driven by the global model registry, which is
// orthogonal to this composition, so an empty route model keeps every registered
// credential eligible and leaves the ranked candidate list as the only filter.
const rankedOnceSelectionModel = ""

// newRankedOnceTestManager registers one counting executor and a set of auths so a
// ranked selection and a pinned one-shot execution can be observed on one manager.
func newRankedOnceTestManager(t *testing.T, executor *onceTestExecutor, auths ...*Auth) (*Manager, *onceResultHook) {
	t.Helper()
	hook := &onceResultHook{}
	manager := NewManager(nil, &RoundRobinSelector{}, hook)
	// A generous retry budget proves the one-shot path never consults it.
	manager.SetRetryConfig(5, time.Second, 5)
	manager.RegisterExecutor(executor)
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	return manager, hook
}

// TestRankedSelectionComposesWithExecuteWithAuthOnce walks the whole contribution end
// to end: a request-scoped ranked candidate list picks the credential, that exact
// credential is then executed by the pinned one-shot path, the provider executor is
// entered exactly once, and exactly one execution result is marked against it.
func TestRankedSelectionComposesWithExecuteWithAuthOnce(t *testing.T) {
	tests := []struct {
		name           string
		disableLowRank bool
		wantAuthID     string
	}{
		{
			name:       "lowest eligible rank wins and is the credential executed once",
			wantAuthID: "once-rank-low",
		},
		{
			name:           "next rank is executed once only when the lowest rank is ineligible",
			disableLowRank: true,
			wantAuthID:     "once-rank-high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &onceTestExecutor{provider: "codex"}
			manager, hook := newRankedOnceTestManager(t, executor,
				&Auth{ID: "once-rank-low", Provider: "codex", Status: StatusActive, Disabled: tt.disableLowRank},
				&Auth{ID: "once-rank-high", Provider: "codex", Status: StatusActive},
				&Auth{ID: "once-unlisted", Provider: "codex", Status: StatusActive},
			)
			opts := rankedCandidateOptions(
				cliproxyexecutor.AuthSelectionCandidate{AuthID: "once-rank-low", PriorityRank: 0, StableOrder: 0},
				cliproxyexecutor.AuthSelectionCandidate{AuthID: "once-rank-high", PriorityRank: 1, StableOrder: 0},
			)

			selected, errSelect := manager.SelectAuth(context.Background(), "codex", rankedOnceSelectionModel, opts)
			if errSelect != nil {
				t.Fatalf("SelectAuth() error = %v", errSelect)
			}
			if selected.ID != tt.wantAuthID {
				t.Fatalf("SelectAuth() auth id = %q, want %q", selected.ID, tt.wantAuthID)
			}

			// The caller hands the ranked winner straight to the pinned one-shot path.
			// The candidate metadata rides along untouched to prove it is inert there:
			// ExecuteWithAuthOnce never selects, so it neither re-ranks nor re-picks.
			in := onceRequest(selected.ID)
			in.Options = opts
			resp, facts, errExec := manager.ExecuteWithAuthOnce(context.Background(), in)
			if errExec != nil {
				t.Fatalf("ExecuteWithAuthOnce() error = %v", errExec)
			}
			if got := string(resp.Payload); got != "ok" {
				t.Fatalf("ExecuteWithAuthOnce() payload = %q, want %q", got, "ok")
			}
			if !facts.RequestWritten {
				t.Fatalf("HTTPAttemptFacts.RequestWritten = false, want true")
			}

			if got := executor.executeCalls.Load(); got != 1 {
				t.Fatalf("executor invocations = %d, want 1", got)
			}
			if got := executor.models(); len(got) != 1 || got[0] != "test-model" {
				t.Fatalf("executed models = %#v, want [test-model]", got)
			}

			results := hook.snapshot()
			if len(results) != 1 {
				t.Fatalf("MarkResult calls = %d, want 1; results=%#v", len(results), results)
			}
			if results[0].AuthID != tt.wantAuthID {
				t.Fatalf("marked auth id = %q, want %q", results[0].AuthID, tt.wantAuthID)
			}
			if results[0].Provider != "codex" {
				t.Fatalf("marked provider = %q, want %q", results[0].Provider, "codex")
			}
			if results[0].Model != "test-model" {
				t.Fatalf("marked model = %q, want %q", results[0].Model, "test-model")
			}
			if !results[0].Success {
				t.Fatalf("marked success = false, want true")
			}
		})
	}
}

// TestRankedSelectionNeverFeedsAnUnlistedCredentialToExecuteOnce pins the negative
// half of the composition: a credential outside the candidate list is never the
// ranked winner, so the paid one-shot execution can never land on it.
func TestRankedSelectionNeverFeedsAnUnlistedCredentialToExecuteOnce(t *testing.T) {
	executor := &onceTestExecutor{provider: "codex"}
	manager, hook := newRankedOnceTestManager(t, executor,
		&Auth{ID: "once-listed", Provider: "codex", Status: StatusActive},
		&Auth{ID: "once-unlisted", Provider: "codex", Status: StatusActive},
	)
	opts := rankedCandidateOptions(
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "once-listed", PriorityRank: 0, StableOrder: 0},
	)

	for index := 0; index < 8; index++ {
		selected, errSelect := manager.SelectAuth(context.Background(), "codex", rankedOnceSelectionModel, opts)
		if errSelect != nil {
			t.Fatalf("SelectAuth() #%d error = %v", index, errSelect)
		}
		if selected.ID != "once-listed" {
			t.Fatalf("SelectAuth() #%d auth id = %q, want %q", index, selected.ID, "once-listed")
		}
		in := onceRequest(selected.ID)
		in.Options = opts
		if _, _, errExec := manager.ExecuteWithAuthOnce(context.Background(), in); errExec != nil {
			t.Fatalf("ExecuteWithAuthOnce() #%d error = %v", index, errExec)
		}
	}

	if got := executor.executeCalls.Load(); got != 8 {
		t.Fatalf("executor invocations = %d, want 8 (one per call)", got)
	}
	results := hook.snapshot()
	if len(results) != 8 {
		t.Fatalf("MarkResult calls = %d, want 8 (one per call)", len(results))
	}
	for index, result := range results {
		if result.AuthID != "once-listed" {
			t.Fatalf("marked auth id #%d = %q, want %q", index, result.AuthID, "once-listed")
		}
	}
}

// TestRankedSelectionAndExecuteOnceShareTheHomeFailClosedContract keeps the two
// primitives consistent under Home: ranked selection refuses before any remote
// dispatch, and the pinned one-shot path refuses because Home credentials are not
// manager persisted. Neither reaches an executor.
func TestRankedSelectionAndExecuteOnceShareTheHomeFailClosedContract(t *testing.T) {
	executor := &onceTestExecutor{provider: "codex"}
	manager, hook := newRankedOnceTestManager(t, executor,
		&Auth{ID: "once-home", Provider: "codex", Status: StatusActive},
	)
	dispatcher := &rankedHomeTestDispatcher{}
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)

	opts := rankedCandidateOptions(
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "once-home", PriorityRank: 0, StableOrder: 0},
	)
	_, _, _, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, rankedOnceSelectionModel, opts, nil)
	assertOnceErrorCode(t, errPick, "ranked_candidates_unsupported_in_home")

	in := onceRequest("once-home")
	in.Options = opts
	_, facts, errExec := manager.ExecuteWithAuthOnce(context.Background(), in)
	assertOnceErrorCode(t, errExec, "auth_not_durable")
	if facts.RequestCount != 0 || facts.RequestWritten {
		t.Fatalf("HTTPAttemptFacts = %#v, want a dispatch-free zero value", facts)
	}

	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("executor invocations = %d, want 0", got)
	}
	if results := hook.snapshot(); len(results) != 0 {
		t.Fatalf("MarkResult calls = %d, want 0; results=%#v", len(results), results)
	}
	if calls := dispatcher.dispatchCalls(); calls != 0 {
		t.Fatalf("home dispatch calls = %d, want 0", calls)
	}
}

// assertOnceErrorCode fails unless err is the package error type with the wanted code.
func assertOnceErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var authErr *Error
	if !errors.As(err, &authErr) {
		t.Fatalf("error = %v (%T), want *Error with code %q", err, err, want)
	}
	if authErr.Code != want {
		t.Fatalf("error code = %q, want %q", authErr.Code, want)
	}
}
