package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestScheduler_ModelCooldown_DoesNotTriggerRebuild verifies that when credentials
// for a model enter cooldown, pick failures do NOT trigger full scheduler rebuilds.
// Rebuilding on cooldown pick failure causes catastrophic lock contention (issue #5988).
func TestScheduler_ModelCooldown_DoesNotTriggerRebuild(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	model := "scheduler-cooldown-no-rebuild-model"
	auth := &Auth{
		ID:       "auth-cooldown-no-rebuild",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	// Verify initial pick succeeds.
	picked, errPick := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil || picked == nil || picked.ID != auth.ID {
		t.Fatalf("initial pickSingle failed: picked=%v, err=%v", picked, errPick)
	}

	// Put credential into cooldown.
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "gemini",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// Directly verify shouldRetrySchedulerPick does NOT treat modelCooldownError as retryable.
	_, errCooldown := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	var targetCooldownErr *modelCooldownError
	if !errors.As(errCooldown, &targetCooldownErr) {
		t.Fatalf("expected pickSingle to return *modelCooldownError, got: %v", errCooldown)
	}
	if shouldRetrySchedulerPick(errCooldown) {
		t.Fatalf("shouldRetrySchedulerPick returned true for modelCooldownError; cooldown must not trigger scheduler rebuild")
	}

	// Capture scheduler provider pointer before pickNext.
	manager.scheduler.mu.Lock()
	providerSchedulerBefore := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	// Calling pickNext on a cooled down model should return modelCooldownError without rebuilding.
	_, _, errPickNext := manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPickNext == nil {
		t.Fatal("expected pickNext to fail with cooldown error, got nil")
	}
	if !errors.As(errPickNext, &targetCooldownErr) {
		t.Fatalf("expected pickNext error to be *modelCooldownError, got: %v", errPickNext)
	}

	manager.scheduler.mu.Lock()
	providerSchedulerAfter := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	// In the buggy implementation, syncScheduler re-allocates providers map and providerScheduler during rebuild.
	if providerSchedulerBefore != providerSchedulerAfter {
		t.Fatalf("pickNext unexpectedly triggered a full scheduler rebuild on cooldown error")
	}

	// Verify that syncedVersion has caught up to currentVersion so subsequent picks take the fast-path.
	if manager.currentVersion() != manager.syncedVersion.Load() {
		t.Fatalf("syncedVersion (%d) did not advance to currentVersion (%d)", manager.syncedVersion.Load(), manager.currentVersion())
	}

	// Subsequent pickNext call must take the fast path without scanning.
	_, _, errSubsequent := manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if !errors.As(errSubsequent, &targetCooldownErr) {
		t.Fatalf("expected subsequent pickNext error to be *modelCooldownError, got: %v", errSubsequent)
	}
}

// TestScheduler_ConcurrentCooldownPicks_DoNotBlockHealthyModel verifies that concurrent
// requests for a cooling-down model do not trigger rebuilds or starve picks on healthy models.
func TestScheduler_ConcurrentCooldownPicks_DoNotBlockHealthyModel(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	cooledModel := "model-cooling"
	healthyModel := "model-healthy"

	authCool := &Auth{ID: "auth-cool", Provider: "gemini"}
	authHealthy := &Auth{ID: "auth-healthy", Provider: "gemini"}

	if _, errRegisterCool := manager.Register(ctx, authCool); errRegisterCool != nil {
		t.Fatalf("register authCool: %v", errRegisterCool)
	}
	if _, errRegisterHealthy := manager.Register(ctx, authHealthy); errRegisterHealthy != nil {
		t.Fatalf("register authHealthy: %v", errRegisterHealthy)
	}

	registerSchedulerModels(t, "gemini", cooledModel, authCool.ID)
	registerSchedulerModels(t, "gemini", healthyModel, authHealthy.ID)
	manager.RefreshSchedulerEntry(authCool.ID)
	manager.RefreshSchedulerEntry(authHealthy.ID)

	// Put authCool into cooldown.
	manager.MarkResult(ctx, Result{
		AuthID:   authCool.ID,
		Provider: "gemini",
		Model:    cooledModel,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	manager.scheduler.mu.Lock()
	providerBefore := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	// Launch concurrent picks for cooledModel.
	var wg sync.WaitGroup
	const concurrency = 30
	startGate := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			_, _, _ = manager.pickNext(ctx, "gemini", cooledModel, cliproxyexecutor.Options{}, nil)
		}()
	}

	// Healthy model pick must complete quickly without being blocked behind rebuild storms.
	healthyDone := make(chan struct{})
	go func() {
		<-startGate
		picked, exec, errPick := manager.pickNext(ctx, "gemini", healthyModel, cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil || exec == nil {
			t.Errorf("healthy pick failed: picked=%v, err=%v", picked, errPick)
		}
		close(healthyDone)
	}()

	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()

	// Start all concurrent goroutines simultaneously.
	close(startGate)

	// Verify healthy model completes while cooldown picks are underway.
	select {
	case <-healthyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy model pick was blocked/starved by concurrent cooldown picks")
	}

	// Verify all cooldown workers finish within timeout.
	select {
	case <-workersDone:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent cooldown pick workers did not complete within timeout")
	}

	manager.scheduler.mu.Lock()
	providerAfter := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	if providerBefore != providerAfter {
		t.Fatalf("concurrent cooldown picks triggered scheduler rebuild(s)")
	}
}

// TestScheduler_InterleavedMarkResult_DoesNotTriggerRebuild verifies that ongoing,
// concurrent MarkResult operations (which bump Generation and update model states)
// do NOT trigger full scheduler rebuilds when pick operations fail for cooled models.
func TestScheduler_InterleavedMarkResult_DoesNotTriggerRebuild(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	modelActive := "model-active"
	modelCooldown := "model-in-cooldown"

	authActive := &Auth{ID: "auth-active", Provider: "gemini"}
	authCooled := &Auth{ID: "auth-cooled", Provider: "gemini"}

	if _, errRegisterActive := manager.Register(ctx, authActive); errRegisterActive != nil {
		t.Fatalf("register authActive: %v", errRegisterActive)
	}
	if _, errRegisterCooled := manager.Register(ctx, authCooled); errRegisterCooled != nil {
		t.Fatalf("register authCooled: %v", errRegisterCooled)
	}

	registerSchedulerModels(t, "gemini", modelActive, authActive.ID)
	registerSchedulerModels(t, "gemini", modelCooldown, authCooled.ID)
	manager.RefreshSchedulerEntry(authActive.ID)
	manager.RefreshSchedulerEntry(authCooled.ID)

	// Put authCooled into cooldown.
	manager.MarkResult(ctx, Result{
		AuthID:   authCooled.ID,
		Provider: "gemini",
		Model:    modelCooldown,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	manager.scheduler.mu.Lock()
	providerBefore := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	var stopFlag atomic.Bool
	var wg sync.WaitGroup

	// Background worker continuously calling MarkResult on authActive to increment Generation.
	const resultWorkers = 5
	for i := 0; i < resultWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stopFlag.Load() {
				manager.MarkResult(ctx, Result{
					AuthID:   authActive.ID,
					Provider: "gemini",
					Model:    modelActive,
					Success:  true,
				})
			}
		}()
	}

	// Concurrent workers attempting pickNext on modelCooldown while MarkResult is actively mutating generations.
	const pickWorkers = 20
	pickDone := make(chan struct{})
	go func() {
		var pickWg sync.WaitGroup
		for i := 0; i < pickWorkers; i++ {
			pickWg.Add(1)
			go func() {
				defer pickWg.Done()
				for j := 0; j < 10; j++ {
					_, _, errPick := manager.pickNext(ctx, "gemini", modelCooldown, cliproxyexecutor.Options{}, nil)
					if errPick == nil {
						t.Errorf("expected pickNext on cooldown model to fail")
					}
				}
			}()
		}
		pickWg.Wait()
		close(pickDone)
	}()

	select {
	case <-pickDone:
	case <-time.After(2 * time.Second):
		stopFlag.Store(true)
		t.Fatal("pick workers timed out while MarkResult was running")
	}

	stopFlag.Store(true)
	wg.Wait()

	manager.scheduler.mu.Lock()
	providerAfter := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	if providerBefore != providerAfter {
		t.Fatalf("interleaved MarkResult caused unexpected scheduler rebuild(s)")
	}
}

// TestScheduler_LargeCredentialSet_CooldownDoesNotLockStarve verifies that with a large
// number of credentials (as in production Antigravity deployments with issue #5988),
// concurrent cooldown pick failures do not cause lock starvation or rebuilds.
func TestScheduler_LargeCredentialSet_CooldownDoesNotLockStarve(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "antigravity"})

	const credentialCount = 40
	targetModel := "antigravity-cooldown-model"
	siblingModel := "antigravity-healthy-model"

	reg := registry.GetGlobalRegistry()
	for i := 0; i < credentialCount; i++ {
		authID := fmt.Sprintf("ag-auth-%d", i)
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
		}
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("register auth %d: %v", i, errRegister)
		}
		// Register BOTH models in a single call to preserve targetModel.
		reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{
			{ID: targetModel},
			{ID: siblingModel},
		})
		t.Cleanup(func(id string) func() {
			return func() { reg.UnregisterClient(id) }
		}(authID))
		manager.RefreshSchedulerEntry(authID)
	}

	// Verify targetModel can be picked initially.
	initialPicked, errInitial := manager.scheduler.pickSingle(ctx, "antigravity", targetModel, cliproxyexecutor.Options{}, nil)
	if errInitial != nil || initialPicked == nil {
		t.Fatalf("initial targetModel pick failed: %v", errInitial)
	}

	// Put all credentials into cooldown for targetModel only.
	for i := 0; i < credentialCount; i++ {
		authID := fmt.Sprintf("ag-auth-%d", i)
		manager.MarkResult(ctx, Result{
			AuthID:   authID,
			Provider: "antigravity",
			Model:    targetModel,
			Success:  false,
			Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
		})
	}

	// Verify targetModel is in modelCooldownError.
	_, errTargetCooldown := manager.scheduler.pickSingle(ctx, "antigravity", targetModel, cliproxyexecutor.Options{}, nil)
	var cooldownErr *modelCooldownError
	if !errors.As(errTargetCooldown, &cooldownErr) {
		t.Fatalf("expected targetModel error to be *modelCooldownError, got: %v", errTargetCooldown)
	}

	manager.scheduler.mu.Lock()
	providerBefore := manager.scheduler.providers["antigravity"]
	manager.scheduler.mu.Unlock()

	// Launch concurrent picks on targetModel (all cooling) and siblingModel (healthy).
	var wg sync.WaitGroup
	const concurrentPicks = 30
	startGate := make(chan struct{})

	for i := 0; i < concurrentPicks; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			_, _, errPick := manager.pickNext(ctx, "antigravity", targetModel, cliproxyexecutor.Options{}, nil)
			if errPick == nil {
				t.Errorf("expected pickNext on targetModel to fail with cooldown error")
			}
			var cdErr *modelCooldownError
			if !errors.As(errPick, &cdErr) {
				t.Errorf("expected pickNext error to be *modelCooldownError, got: %v", errPick)
			}
		}()
	}

	healthyDone := make(chan struct{})
	go func() {
		<-startGate
		picked, _, errPick := manager.pickNext(ctx, "antigravity", siblingModel, cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil {
			t.Errorf("sibling model pick failed: picked=%v, err=%v", picked, errPick)
		}
		close(healthyDone)
	}()

	close(startGate)

	select {
	case <-healthyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy sibling model pick was starved by large credential set cooldown picks")
	}

	picksDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(picksDone)
	}()

	select {
	case <-picksDone:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent targetModel picks timed out")
	}

	manager.scheduler.mu.Lock()
	providerAfter := manager.scheduler.providers["antigravity"]
	manager.scheduler.mu.Unlock()

	if providerBefore != providerAfter {
		t.Fatalf("large credential set cooldown picks triggered full scheduler rebuild")
	}
}

// TestScheduler_RebuildDuringConcurrentMarkResult_PreservesLatestState verifies
// that when rebuild runs with an earlier snapshot, in-flight MarkResult state mutations
// (e.g. cooldown or recovery) are preserved and never reverted (Reviewer Finding P1).
func TestScheduler_RebuildDuringConcurrentMarkResult_PreservesLatestState(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	model := "model-concurrent-mark-rebuild"
	auth := &Auth{
		ID:       "auth-concurrent-mark",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	// Case A: Snapshot G is healthy, concurrent MarkResult puts auth into cooldown (G+1).
	manager.mu.RLock()
	healthySnapshot := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "gemini",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// Rebuild with the older healthy snapshot G.
	manager.scheduler.rebuild([]*Auth{healthySnapshot})

	// Cooldown state must be preserved (must NOT revert to healthy).
	_, errPickCooldown := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	var targetCooldownErr *modelCooldownError
	if !errors.As(errPickCooldown, &targetCooldownErr) {
		t.Fatalf("rebuild with stale snapshot reverted cooldown state; expected *modelCooldownError, got: %v", errPickCooldown)
	}

	// Case B: Snapshot G is in cooldown, concurrent MarkResult restores/recovers auth (G+2).
	manager.mu.RLock()
	cooledSnapshot := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	// Clear cooldown via ResetQuota.
	if _, _, errReset := manager.ResetQuota(ctx, auth.ID); errReset != nil {
		t.Fatalf("reset quota: %v", errReset)
	}

	// Rebuild with older cooled snapshot.
	manager.scheduler.rebuild([]*Auth{cooledSnapshot})

	// Recovered state must be preserved (must NOT revert to cooldown).
	pickedRecovered, errPickRecovered := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPickRecovered != nil || pickedRecovered == nil || pickedRecovered.ID != auth.ID {
		t.Fatalf("rebuild with stale snapshot reverted recovered state: picked=%v, err=%v", pickedRecovered, errPickRecovered)
	}
}

// TestScheduler_ModelProjectionCooldown_DoesNotInvalidateFastPath verifies that
// ApplyClientModelProjections (cooldowns / quota events) does not increment RegistrationEpoch,
// ensuring the fast path remains 100% active during cooldowns (Reviewer Finding P2).
func TestScheduler_ModelProjectionCooldown_DoesNotInvalidateFastPath(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	model := "model-projection-fastpath"
	auth := &Auth{
		ID:       "auth-proj-fastpath",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	manager.syncScheduler()
	initialVersion := manager.currentVersion()
	if initialVersion != manager.syncedVersion.Load() {
		t.Fatalf("expected syncedVersion (%d) to equal currentVersion (%d)", manager.syncedVersion.Load(), initialVersion)
	}

	// Apply cooldown via MarkResult (invokes ApplyClientModelProjections).
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "gemini",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// RegistrationEpoch and currentVersion must NOT have changed.
	if manager.currentVersion() != initialVersion {
		t.Fatalf("currentVersion changed from %d to %d on quota cooldown; cooldown must not invalidate structural registration epoch", initialVersion, manager.currentVersion())
	}

	// Fast path is confirmed intact.
	_, errPick := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if manager.shouldRetrySchedulerPick(errPick, initialVersion) {
		t.Fatal("shouldRetrySchedulerPick unexpectedly returned true on cooldown error")
	}
}

// TestScheduler_RebuildDuringConcurrentUpdate_DoesNotResurrectDisabledCredential verifies
// that an active snapshot taken at Generation G cannot resurrect a credential that was
// disabled at Generation G+1 when rebuild executes (Reviewer Finding P1).
func TestScheduler_RebuildDuringConcurrentUpdate_DoesNotResurrectDisabledCredential(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	model := "model-disabled-rebuild"
	auth := &Auth{
		ID:       "auth-disabled-rebuild",
		Provider: "gemini",
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	// Capture active snapshot at Generation 1.
	manager.mu.RLock()
	activeSnapshotG1 := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	// Update auth to disabled at Generation 2.
	disabledAuth := activeSnapshotG1.Clone()
	disabledAuth.Disabled = true
	disabledAuth.Status = StatusDisabled
	if _, errUpdate := manager.Update(ctx, disabledAuth); errUpdate != nil {
		t.Fatalf("update auth to disabled: %v", errUpdate)
	}

	// Rebuild scheduler using the older active snapshot G1.
	manager.scheduler.rebuild([]*Auth{activeSnapshotG1})

	// Verify the disabled credential is NOT resurrected in scheduler and cannot be picked.
	picked, errPick := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick == nil || picked != nil {
		t.Fatalf("rebuild with stale active snapshot resurrected disabled credential: %v", picked)
	}
}

// TestScheduler_IncrementalRefresh_AllowsFailedPickToRetryAndSucceed verifies that
// when an initial pick attempt fails before an incremental RefreshSchedulerEntry occurs,
// the subsequent shouldRetrySchedulerPick check recognizes that an incremental update completed,
// advances syncedVersion, and allows the request to retry and succeed without full rebuild (Reviewer P2).
func TestScheduler_IncrementalRefresh_AllowsFailedPickToRetryAndSucceed(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	initialModel := "model-incremental-initial"
	newModel := "model-incremental-new"

	auth := &Auth{
		ID:       "auth-incremental-test",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", initialModel, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)
	manager.syncScheduler()

	// Initial pick on newModel fails because it is not registered yet.
	beforeVer := manager.syncedVersion.Load()
	_, errInitialPick := manager.scheduler.pickSingle(ctx, "gemini", newModel, cliproxyexecutor.Options{}, nil)
	if errInitialPick == nil {
		t.Fatal("expected pickSingle on newModel to fail initially")
	}

	// Register newModel in registry and execute incremental RefreshSchedulerEntry (no full rebuild).
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{
		{ID: initialModel},
		{ID: newModel},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	// shouldRetrySchedulerPick must recognize that incremental update occurred and return true to allow retry.
	if !manager.shouldRetrySchedulerPick(errInitialPick, beforeVer) {
		t.Fatal("shouldRetrySchedulerPick returned false after incremental RefreshSchedulerEntry; expected true to allow retry")
	}

	// Retrying pickSingle must now succeed.
	picked, errRetry := manager.scheduler.pickSingle(ctx, "gemini", newModel, cliproxyexecutor.Options{}, nil)
	if errRetry != nil || picked == nil || picked.ID != auth.ID {
		t.Fatalf("retry pickSingle failed: picked=%v, err=%v", picked, errRetry)
	}
}

// TestScheduler_ConcurrentPicks_OneSyncAllowsOthersToRetry verifies that when multiple
// concurrent requests fail on an un-synced model, one request performing the sync allows
// all other waiting/concurrent requests to retry and succeed without failing (Reviewer Finding P2).
func TestScheduler_ConcurrentPicks_OneSyncAllowsOthersToRetry(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	modelInitial := "model-concurrent-init"
	modelNew := "model-concurrent-new"

	auth := &Auth{
		ID:       "auth-concurrent-retry",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", modelInitial, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)
	manager.syncScheduler()

	// Register modelNew directly into registry.
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{
		{ID: modelInitial},
		{ID: modelNew},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	const numRequests = 10
	var wg sync.WaitGroup
	results := make([]error, numRequests)
	phase1Done := make(chan struct{})
	var phase1Count atomic.Int32
	phase2Start := make(chan struct{})

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			// Phase 1: All requests attempt initial pick before sync and observe the failure.
			beforeVer := manager.syncedVersion.Load()
			selected, errPick := manager.scheduler.pickSingle(ctx, "gemini", modelNew, cliproxyexecutor.Options{}, nil)
			if errPick != nil && modelNew != "" {
				if phase1Count.Add(1) == int32(numRequests) {
					close(phase1Done)
				}
				<-phase2Start
				if manager.shouldRetrySchedulerPick(errPick, beforeVer) {
					selected, errPick = manager.scheduler.pickSingle(ctx, "gemini", modelNew, cliproxyexecutor.Options{}, nil)
				}
			}
			if errPick == nil && (selected == nil || selected.ID != auth.ID) {
				results[idx] = errors.New("picked nil or wrong auth")
			} else {
				results[idx] = errPick
			}
		}()
	}

	// Wait until all requests have failed phase 1 initial pick.
	select {
	case <-phase1Done:
	case <-time.After(2 * time.Second):
		t.Fatal("phase 1 timed out waiting for all initial pick attempts to fail")
	}
	// Release all requests into phase 2 to concurrently evaluate retry.
	close(phase2Start)
	wg.Wait()

	for i, err := range results {
		if err != nil {
			t.Errorf("request %d failed to discover newly synced model: %v", i, err)
		}
	}
}

// TestScheduler_RegisterClient_InvalidatesFastPath verifies that external model registry
// changes (RegisterClient) reliably invalidate the fast-path even if a previous sync already occurred
// (Reviewer Finding P2).
func TestScheduler_RegisterClient_InvalidatesFastPath(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	initialModel := "model-init"
	dynamicModel := "model-dynamic"

	auth := &Auth{
		ID:       "auth-dynamic-reg",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", initialModel, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	// Ensure manager and scheduler are completely synced (syncedVersion == currentVersion).
	manager.syncScheduler()
	if manager.currentVersion() != manager.syncedVersion.Load() {
		t.Fatalf("expected syncedVersion (%d) to equal currentVersion (%d)", manager.syncedVersion.Load(), manager.currentVersion())
	}

	// Register dynamicModel directly into global registry without calling RefreshSchedulerEntry.
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{
		{ID: initialModel},
		{ID: dynamicModel},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// currentVersion must have increased because registry generation changed.
	if manager.currentVersion() <= manager.syncedVersion.Load() {
		t.Fatalf("currentVersion (%d) did not increase after RegisterClient (syncedVersion=%d)", manager.currentVersion(), manager.syncedVersion.Load())
	}

	// pickNext on dynamicModel must succeed by discovering the new model.
	picked, exec, errPick := manager.pickNext(ctx, "gemini", dynamicModel, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext failed to discover dynamically registered model: %v", errPick)
	}
	if picked == nil || picked.ID != auth.ID || exec == nil {
		t.Fatalf("pickNext returned unexpected auth=%v, exec=%v", picked, exec)
	}
}

// TestScheduler_UnschedulableAuth_DoesNotTriggerRebuildLoop verifies that unschedulable auths
// (e.g. auths with empty provider or empty ID) are not counted as schedulable auths, preventing
// an infinite rebuild loop where activeCount never matches s.authProviders (Reviewer Finding P2).
func TestScheduler_UnschedulableAuth_DoesNotTriggerRebuildLoop(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	model := "model-unschedulable-test"

	// Register a valid auth that will cool down.
	validAuth := &Auth{
		ID:       "auth-valid-cooling",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, validAuth); errRegister != nil {
		t.Fatalf("register valid auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", model, validAuth.ID)
	manager.RefreshSchedulerEntry(validAuth.ID)

	// Register an unschedulable auth directly in manager (e.g. Empty provider).
	unschedulableAuth := &Auth{
		ID:       "auth-empty-provider",
		Provider: "", // Unschedulable!
	}
	if _, errRegister := manager.Register(ctx, unschedulableAuth); errRegister != nil {
		t.Fatalf("register unschedulable auth: %v", errRegister)
	}

	// Put validAuth into cooldown.
	manager.MarkResult(ctx, Result{
		AuthID:   validAuth.ID,
		Provider: "gemini",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// Initial pick on model should sync once and converge.
	_, _, errPickInitial := manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPickInitial == nil {
		t.Fatal("expected pickNext to fail with cooldown error")
	}

	// Synced version must have converged to currentVersion despite the unschedulable auth.
	if manager.currentVersion() != manager.syncedVersion.Load() {
		t.Fatalf("syncedVersion (%d) did not converge to currentVersion (%d) in presence of unschedulable auth",
			manager.syncedVersion.Load(), manager.currentVersion())
	}

	manager.scheduler.mu.Lock()
	providersBefore := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	// Subsequent pick attempts on the cooling model MUST NOT trigger rebuilds.
	for i := 0; i < 5; i++ {
		_, _, _ = manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	}

	manager.scheduler.mu.Lock()
	providersAfter := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	if providersBefore != providersAfter {
		t.Fatalf("subsequent picks triggered scheduler rebuild(s) due to unschedulable auth mismatch")
	}
}
