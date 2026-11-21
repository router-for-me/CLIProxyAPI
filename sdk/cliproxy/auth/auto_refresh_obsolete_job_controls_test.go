package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestBackgroundRefreshSkipsSameTokenRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newObsoleteRefreshFixture(t, true, true)
		defer f.stop(t)
		before := f.current(t)
		result := startSameTokenForeground(t, f, "renewed", 0)
		f.exec.release()
		awaitObsoleteRefresh(t, f.foregroundDone)
		if <-result != nil {
			t.Fatal("obsolete refresh same token foreground")
		}
		synctest.Wait()
		auth := f.current(t)
		if auth.CredentialVersion != before.CredentialVersion || authAccessToken(auth) != "synthetic-obsolete-a" || !auth.HasValidAccessToken(time.Now()) || !auth.NextRefreshAfter.IsZero() {
			t.Fatal("obsolete refresh same token state")
		}
		requireObsoleteJobGone(t, f)
		if f.exec.oldRefresh.Load() != 1 || f.exec.freshRefresh.Load() != 0 {
			t.Fatal("obsolete refresh same token extra dispatch")
		}
	})
}

func TestBackgroundRefreshPreservesNewerBackoff(t *testing.T) {
	for _, outcome := range []string{"failed", "ineffective"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newObsoleteRefreshFixture(t, true, true)
				defer f.stop(t)
				before := f.current(t)
				elapsed := time.Duration(0)
				if outcome == "ineffective" {
					// A 30s ineffective-refresh backoff written at t0+30s equals the
					// original job's t0+60s pending marker. Equality does not prove ownership.
					elapsed = 30 * time.Second
				}
				result := startSameTokenForeground(t, f, outcome, elapsed)
				pending := f.current(t).NextRefreshAfter
				f.exec.release()
				awaitObsoleteRefresh(t, f.foregroundDone)
				if err := <-result; (err != nil) != (outcome == "failed") {
					t.Fatal("obsolete refresh backoff foreground")
				}
				synctest.Wait()
				auth := f.current(t)
				wantSameTimestamp := outcome == "ineffective"
				if auth.CredentialVersion != before.CredentialVersion || authAccessToken(auth) != "synthetic-obsolete-a" || !auth.NextRefreshAfter.After(time.Now()) || auth.NextRefreshAfter.Equal(pending) != wantSameTimestamp {
					t.Error("obsolete refresh backoff state")
				}
				requireObsoleteJobGone(t, f)
				if f.exec.oldRefresh.Load() != 1 || f.exec.freshRefresh.Load() != 0 {
					t.Fatal("obsolete refresh backoff extra dispatch")
				}
			})
		})
	}
}

func TestBackgroundRefreshHonorsWorkerCancellation(t *testing.T) {
	t.Run("waiting_mutex", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newObsoleteRefreshFixture(t, true, false)
			defer f.stop(t)
			before := f.current(t)
			value, _ := f.manager.refreshLocks.LoadOrStore(f.id, &authRefreshLock{})
			lock := value.(*authRefreshLock)
			lock.mu.Lock()
			var once sync.Once
			release := func() { once.Do(lock.mu.Unlock) }
			defer release()
			f.queueDue(t)
			f.startWorker(t)
			requireObsoleteJobRunning(t, f)
			f.cancel()
			// Freshness and version cannot satisfy this cancellation control.
			probe := f.current(t)
			probe.NextRefreshAfter = time.Time{}
			if probe.CredentialVersion != before.CredentialVersion || !f.manager.shouldRefresh(probe, time.Now()) {
				t.Fatal("obsolete refresh cancellation precondition")
			}
			release()
			synctest.Wait()
			requireObsoleteJobGone(t, f)
			if f.exec.oldRefresh.Load() != 0 || f.exec.freshRefresh.Load() != 0 {
				t.Fatal("obsolete refresh canceled worker dispatch")
			}
		})
	})
	t.Run("during_evaluator", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newObsoleteRefreshFixture(t, true, false)
			defer f.stop(t)
			evaluator := &obsoleteCancelEvaluator{cancel: f.cancel}
			current := f.current(t)
			_, committed, err := f.manager.Modify(WithSkipPersist(context.Background()), f.id, current.RegistrationEpoch, func(auth *Auth) error {
				auth.Runtime = evaluator
				return nil
			})
			if err != nil || !committed {
				t.Fatal("obsolete refresh evaluator setup")
			}
			f.queueDue(t)
			evaluator.enabled.Store(true)
			f.startWorker(t)
			synctest.Wait()
			requireObsoleteJobGone(t, f)
			if !errors.Is(f.ctx.Err(), context.Canceled) || f.exec.oldRefresh.Load() != 0 || f.exec.freshRefresh.Load() != 0 {
				t.Fatal("obsolete refresh evaluator cancellation")
			}
		})
	})
}

func TestBackgroundRefreshRechecksStaleDueDecision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newObsoleteRefreshFixture(t, true, false)
		defer f.stop(t)
		observed := f.current(t)
		if !f.manager.shouldRefresh(observed, time.Now()) {
			t.Fatal("obsolete refresh stale decision precondition")
		}
		_, committed, err := f.manager.Modify(WithSkipPersist(context.Background()), f.id, observed.RegistrationEpoch, func(auth *Auth) error {
			auth.Metadata["access_token"] = "synthetic-obsolete-c"
			auth.Metadata["refresh_token"] = "synthetic-obsolete-grant-c"
			auth.Metadata["expired"] = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
			return nil
		})
		if err != nil || !committed || f.manager.shouldRefresh(f.current(t), time.Now()) {
			t.Fatal("obsolete refresh stale decision replacement")
		}
		// Model the real decision-to-enqueue boundary using its actual helper.
		// This does not claim a paused complete handleDueAuth invocation.
		job := f.manager.markRefreshPending(f.loop, f.id, observed.RegistrationEpoch, time.Now())
		if job != nil {
			f.loop.jobs <- job
			f.startWorker(t)
			synctest.Wait()
		}
		f.requireFinished(t)
		if f.exec.oldRefresh.Load() != 0 || f.exec.freshRefresh.Load() != 0 {
			t.Fatal("obsolete refresh stale decision dispatch")
		}
	})
}

type obsoleteSameTokenExecutor struct {
	*obsoleteRefreshExecutor
	outcome string
}

func (e *obsoleteSameTokenExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	next, err := e.obsoleteRefreshExecutor.Refresh(ctx, auth)
	if err != nil || e.outcome == "failed" {
		return nil, errors.New("synthetic transient refresh failure")
	}
	next.Metadata["access_token"] = authAccessToken(auth)
	next.Metadata["refresh_token"] = authRefreshToken(auth)
	if e.outcome == "ineffective" {
		next.Metadata["expired"] = auth.Metadata["expired"]
	}
	return next, nil
}

func startSameTokenForeground(t *testing.T, f *obsoleteRefreshFixture, outcome string, elapsed time.Duration) <-chan error {
	t.Helper()
	f.manager.RegisterExecutor(&obsoleteSameTokenExecutor{obsoleteRefreshExecutor: f.exec, outcome: outcome})
	f.foregroundDone = make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(f.foregroundDone)
		_, err := f.manager.ForceRefreshAuth(context.Background(), f.id)
		result <- err
	}()
	awaitObsoleteRefresh(t, f.exec.entered)
	f.queueDue(t)
	if elapsed > 0 {
		// Advance only the synthetic clock, before the mutex-waiting worker
		// starts. Channel barriers, rather than elapsed time, establish ordering.
		timer := time.NewTimer(elapsed)
		<-timer.C
		timer.Stop()
	}
	f.startWorker(t)
	requireObsoleteJobRunning(t, f)
	return result
}

func requireObsoleteJobRunning(t *testing.T, f *obsoleteRefreshFixture) {
	t.Helper()
	f.manager.mu.RLock()
	job := f.manager.refreshJobs[f.id]
	running := job != nil && job.running
	f.manager.mu.RUnlock()
	if !running {
		t.Fatal("obsolete refresh worker ordering")
	}
}

func requireObsoleteJobGone(t *testing.T, f *obsoleteRefreshFixture) {
	t.Helper()
	f.manager.mu.RLock()
	remaining := f.manager.refreshJobs[f.id] != nil
	f.manager.mu.RUnlock()
	if remaining {
		t.Fatal("obsolete refresh unfinished job")
	}
}

type obsoleteCancelEvaluator struct {
	cancel  context.CancelFunc
	enabled atomic.Bool
}

func (e *obsoleteCancelEvaluator) ShouldRefresh(time.Time, *Auth) bool {
	if e.enabled.Load() {
		e.cancel()
	}
	return true
}
