package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// inFlightWiringExecutor is a gemini test executor whose dispatch behavior is
// driven by per-test function fields. It is used to observe the manager's
// per-credential in-flight accounting around real Execute/ExecuteStream
// dispatches through the three execution loops.
type inFlightWiringExecutor struct {
	executeFn func(context.Context, *Auth) (cliproxyexecutor.Response, error)
	streamFn  func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error)
}

func (*inFlightWiringExecutor) Identifier() string { return "gemini" }

func (e *inFlightWiringExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.executeFn != nil {
		return e.executeFn(ctx, auth)
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *inFlightWiringExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.streamFn != nil {
		return e.streamFn(ctx, auth)
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*inFlightWiringExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*inFlightWiringExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*inFlightWiringExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// newInFlightWiringManager builds a manager with one gemini executor and one
// registered api-key auth serving model, mirroring the pool-failover harness.
func newInFlightWiringManager(t *testing.T, executor *inFlightWiringExecutor, authID, model string) *Manager {
	t.Helper()
	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)
	registerSchedulerModels(t, "gemini", model, authID)
	auth := &Auth{
		ID:         authID,
		Provider:   "gemini",
		Status:     StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey, AttributeAPIKey: "k-" + authID},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", authID, errRegister)
	}
	return manager
}

// TestSchedulerInFlight_AdjustSemantics pins adjustInFlight/inFlightForAuth:
// pure deltas for known auths, zero for fresh or unknown auths, and no-ops for
// unknown auth IDs and empty IDs.
func TestSchedulerInFlight_AdjustSemantics(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "inflight-a", Provider: "gemini"},
		&Auth{ID: "inflight-b", Provider: "gemini"},
	)

	if got := scheduler.inFlightForAuth("inflight-a"); got != 0 {
		t.Fatalf("inFlightForAuth(fresh) = %d, want 0", got)
	}
	scheduler.adjustInFlight("inflight-a", 2)
	if got := scheduler.inFlightForAuth("inflight-a"); got != 2 {
		t.Fatalf("inFlightForAuth(after +2) = %d, want 2", got)
	}
	scheduler.adjustInFlight("inflight-a", -1)
	if got := scheduler.inFlightForAuth("inflight-a"); got != 1 {
		t.Fatalf("inFlightForAuth(after -1) = %d, want 1", got)
	}

	// Unknown auths and empty IDs are no-ops and never create state.
	scheduler.adjustInFlight("inflight-unknown", 5)
	if got := scheduler.inFlightForAuth("inflight-unknown"); got != 0 {
		t.Fatalf("inFlightForAuth(unknown) = %d, want 0", got)
	}
	scheduler.adjustInFlight("", 5)
	scheduler.adjustInFlight("   ", 5)
	if got := scheduler.inFlightForAuth("inflight-a"); got != 1 {
		t.Fatalf("inFlightForAuth(after no-op adjusts) = %d, want 1 (untouched)", got)
	}

	// Counting back down to zero clears the entry.
	scheduler.adjustInFlight("inflight-a", -1)
	if got := scheduler.inFlightForAuth("inflight-a"); got != 0 {
		t.Fatalf("inFlightForAuth(after settle) = %d, want 0", got)
	}
}

// TestSchedulerInFlight_ConcurrentAdjustsBalanceToZero drives 50 goroutines
// of mixed increment/decrement accounting through one auth's counter and
// requires the final count to be exactly zero. Run under -race this also
// pins that every access is serialized by the scheduler mutex.
func TestSchedulerInFlight_ConcurrentAdjustsBalanceToZero(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "inflight-race", Provider: "gemini"},
	)

	const goroutines = 50
	var wg sync.WaitGroup
	for worker := 0; worker < goroutines; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			if worker%2 == 0 {
				scheduler.adjustInFlight("inflight-race", 1)
				scheduler.adjustInFlight("inflight-race", 1)
				scheduler.adjustInFlight("inflight-race", -1)
				scheduler.adjustInFlight("inflight-race", -1)
				return
			}
			scheduler.adjustInFlight("inflight-race", 1)
			scheduler.adjustInFlight("inflight-race", -1)
		}(worker)
	}
	wg.Wait()

	if got := scheduler.inFlightForAuth("inflight-race"); got != 0 {
		t.Fatalf("inFlightForAuth(after concurrent accounting) = %d, want 0", got)
	}
}

// TestSchedulerInFlight_PickRegistersInFlight pins the pick-side pairing: a
// picked auth reads 1 while its dispatch is in flight, a second pick of a
// different auth reads 1 for each, and the release path returns both to zero.
// The acquire step mirrors what the execution loops do right after pool
// breaker admission (the manager-level wiring is covered by the
// ManagerExecute tests below).
func TestSchedulerInFlight_PickRegistersInFlight(t *testing.T) {
	model := "inflight-pick-model"
	registerSchedulerModels(t, "gemini", model, "inflight-pick-a", "inflight-pick-b")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "inflight-pick-a", Provider: "gemini"},
		&Auth{ID: "inflight-pick-b", Provider: "gemini"},
	)

	pick := func() *Auth {
		t.Helper()
		auth, errPick := scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
		if errPick != nil || auth == nil {
			t.Fatalf("pickSingle() error = %v, auth = %v", errPick, auth)
		}
		// Mirror the execution loop's acquire at execution commit.
		scheduler.adjustInFlight(auth.ID, 1)
		return auth
	}

	first := pick()
	if got := scheduler.inFlightForAuth(first.ID); got != 1 {
		t.Fatalf("inFlightForAuth(%s while in flight) = %d, want 1", first.ID, got)
	}
	second := pick()
	if first.ID == second.ID {
		t.Fatalf("second pick returned %s again, want a different auth", second.ID)
	}
	for _, authID := range []string{first.ID, second.ID} {
		if got := scheduler.inFlightForAuth(authID); got != 1 {
			t.Fatalf("inFlightForAuth(%s with both in flight) = %d, want 1", authID, got)
		}
	}

	// The release path drops each in-flight pick back to zero.
	scheduler.adjustInFlight(first.ID, -1)
	scheduler.adjustInFlight(second.ID, -1)
	for _, authID := range []string{first.ID, second.ID} {
		if got := scheduler.inFlightForAuth(authID); got != 0 {
			t.Fatalf("inFlightForAuth(%s after release) = %d, want 0", authID, got)
		}
	}
}

// TestSchedulerInFlight_RebuildPreservesCountsForLiveAuths pins why the
// counter lives outside scheduledAuthMeta: the metas are recreated on every
// upsert and rebuild (MarkResult re-upserts each result), so a meta-resident
// counter would be zeroed mid-flight. A rebuild must preserve counts for
// auths that remain and prune entries for auths that no longer exist.
func TestSchedulerInFlight_RebuildPreservesCountsForLiveAuths(t *testing.T) {
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "inflight-keep", Provider: "gemini"},
		&Auth{ID: "inflight-gone", Provider: "gemini"},
	)
	scheduler.adjustInFlight("inflight-keep", 2)
	scheduler.adjustInFlight("inflight-gone", 1)

	scheduler.rebuild([]*Auth{{ID: "inflight-keep", Provider: "gemini"}})

	if got := scheduler.inFlightForAuth("inflight-keep"); got != 2 {
		t.Fatalf("inFlightForAuth(live auth after rebuild) = %d, want 2", got)
	}
	if got := scheduler.inFlightForAuth("inflight-gone"); got != 0 {
		t.Fatalf("inFlightForAuth(removed auth after rebuild) = %d, want 0", got)
	}
}

// TestSchedulerInFlight_ManagerWrappersNilSafe pins that the manager wrappers
// tolerate nil managers, nil auths, and auths unknown to the scheduler.
func TestSchedulerInFlight_ManagerWrappersNilSafe(t *testing.T) {
	t.Parallel()

	var nilManager *Manager
	nilManager.acquireAuthInFlight(nil)
	nilManager.releaseAuthInFlight(nil)

	manager := NewManager(nil, nil, nil)
	manager.acquireAuthInFlight(nil)
	manager.releaseAuthInFlight(nil)
	unknown := &Auth{ID: "inflight-never-registered", Provider: "gemini"}
	manager.acquireAuthInFlight(unknown)
	manager.releaseAuthInFlight(unknown)
	if got := manager.scheduler.inFlightForAuth(unknown.ID); got != 0 {
		t.Fatalf("inFlightForAuth(unknown auth) = %d, want 0", got)
	}
}

// TestSchedulerInFlight_ManagerExecutePairsAcquireRelease wires the non-
// streaming loop end to end: the picked auth reads 1 while its dispatch is
// running and 0 once Execute returns.
func TestSchedulerInFlight_ManagerExecutePairsAcquireRelease(t *testing.T) {
	const authID = "inflight-wiring-a"
	const model = "inflight-wiring-model"
	executor := &inFlightWiringExecutor{}
	manager := newInFlightWiringManager(t, executor, authID, model)

	started := make(chan struct{})
	proceed := make(chan struct{})
	executor.executeFn = func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
		close(started)
		<-proceed
		return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
	}

	done := make(chan error, 1)
	go func() {
		_, errExec := manager.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		done <- errExec
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("executor.Execute never started")
	}
	if got := manager.scheduler.inFlightForAuth(authID); got != 1 {
		t.Fatalf("in-flight during dispatch = %d, want 1", got)
	}

	close(proceed)
	if errExec := <-done; errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	if got := manager.scheduler.inFlightForAuth(authID); got != 0 {
		t.Fatalf("in-flight after Execute completed = %d, want 0", got)
	}
}

// TestSchedulerInFlight_ManagerExecuteReleasesOnFailureRotation pins the
// release on the failure path: a failed dispatch rotates to the next pick
// (which exhausts the tried set and surfaces the error) and must return the
// counter to zero even though the request ultimately fails.
func TestSchedulerInFlight_ManagerExecuteReleasesOnFailureRotation(t *testing.T) {
	const authID = "inflight-wiring-fail"
	const model = "inflight-wiring-fail-model"
	executor := &inFlightWiringExecutor{}
	manager := newInFlightWiringManager(t, executor, authID, model)
	executor.executeFn = func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
		return cliproxyexecutor.Response{}, errors.New("inflight upstream failure")
	}

	_, errExec := manager.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("Execute() error = nil, want the surfaced upstream failure")
	}
	if got := manager.scheduler.inFlightForAuth(authID); got != 0 {
		t.Fatalf("in-flight after failed rotation = %d, want 0", got)
	}
}

// TestSchedulerInFlight_ManagerExecuteStreamReleasesAfterDrain wires the
// streaming loop end to end: the picked auth reads 1 while the stream is
// open and 0 once the stream has fully drained.
func TestSchedulerInFlight_ManagerExecuteStreamReleasesAfterDrain(t *testing.T) {
	const authID = "inflight-wiring-stream"
	const model = "inflight-wiring-stream-model"
	executor := &inFlightWiringExecutor{}
	manager := newInFlightWiringManager(t, executor, authID, model)
	executor.streamFn = func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
		chunks := make(chan cliproxyexecutor.StreamChunk, 1)
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("chunk")}
		close(chunks)
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}

	result, errStream := manager.ExecuteStream(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	if got := manager.scheduler.inFlightForAuth(authID); got != 1 {
		t.Fatalf("in-flight while stream open = %d, want 1", got)
	}

	for range result.Chunks {
	}
	if got := manager.scheduler.inFlightForAuth(authID); got != 0 {
		t.Fatalf("in-flight after stream drained = %d, want 0", got)
	}
}

// TestSchedulerInFlight_ManagerExecuteStreamReleasesOnClientDisconnect pins
// the leak fix for canceled streams: a client disconnect mid-stream never
// reaches MarkResult (no result chunk ever arrives), so the release must fire
// when the stream drain completes. The executor closes its channel when the
// request context cancels — realistic upstream-teardown behavior on a client
// disconnect — which lets the drain pipeline finish and release the hold.
// (An upstream that ignored cancellation and never closed its channel would
// keep the hold, mirroring the pre-existing drain-goroutine hang in that
// pathological case.)
func TestSchedulerInFlight_ManagerExecuteStreamReleasesOnClientDisconnect(t *testing.T) {
	const authID = "inflight-wiring-disconnect"
	const model = "inflight-wiring-disconnect-model"
	executor := &inFlightWiringExecutor{}
	manager := newInFlightWiringManager(t, executor, authID, model)
	ctx, cancel := context.WithCancel(context.Background())
	executor.streamFn = func(streamCtx context.Context, _ *Auth) (*cliproxyexecutor.StreamResult, error) {
		chunks := make(chan cliproxyexecutor.StreamChunk, 1)
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
		go func() {
			<-streamCtx.Done()
			close(chunks)
		}()
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}

	result, errStream := manager.ExecuteStream(ctx, []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	if got := manager.scheduler.inFlightForAuth(authID); got != 1 {
		t.Fatalf("in-flight while stream open = %d, want 1", got)
	}

	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := manager.scheduler.inFlightForAuth(authID); got == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("in-flight after client disconnect = %d, want 0 (leaked)", manager.scheduler.inFlightForAuth(authID))
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Draining the returned stream must not block after the release.
	if result == nil || result.Chunks == nil {
		t.Fatal("ExecuteStream() returned an empty stream result")
	}
}
