package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestAutoRefreshPolicyRefusalUsesRetryDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newPolicyRefreshFixture(t)
		defer f.stop(t) // Registered before either worker starts.
		var checks atomic.Int64
		f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(_ context.Context, use AuthUse) error {
			if use.Kind != AuthUseRefresh || use.Auth == nil || use.Auth.ID != f.auth.ID {
				t.Error("unexpected background policy input")
			}
			// Bound the unfixed loop: after two refusals, a successful refresh makes
			// this auth not due. The failing baseline cannot spin indefinitely.
			if checks.Add(1) <= 2 {
				return context.Canceled
			}
			return nil
		}))
		f.start()
		for wantChecks := int64(1); wantChecks <= 2; wantChecks++ {
			synctest.Wait()
			current := f.current(t)
			if checks.Load() != wantChecks || f.exec.refreshes.Load() != 0 {
				t.Fatal("background policy refusal retried without waiting")
			}
			wantRetry := time.Now().Add(refreshCheckInterval)
			if !current.NextRefreshAfter.Equal(wantRetry) {
				t.Fatal("background policy refusal lost retry deadline")
			}
			if current.CredentialVersion != f.auth.CredentialVersion || current.Metadata["access_token"] != f.auth.Metadata["access_token"] ||
				current.Metadata["refresh_token"] != f.auth.Metadata["refresh_token"] || !current.LastRefreshedAt.IsZero() {
				t.Fatal("background policy refusal changed credential material")
			}
			f.requireHealthy(t)
			f.manager.mu.RLock()
			pending := f.manager.refreshJobs[f.auth.ID] != nil
			f.manager.mu.RUnlock()
			if next, scheduled := f.loop.peek(); pending || !scheduled || !next.Equal(wantRetry) {
				t.Fatal("background policy refusal was not rescheduled at its deadline")
			}
			time.Sleep(refreshCheckInterval - time.Nanosecond) // Virtual synctest time only.
			synctest.Wait()
			if checks.Load() != wantChecks || f.exec.refreshes.Load() != 0 {
				t.Fatal("background policy retry ran before its deadline")
			}
			time.Sleep(time.Nanosecond)
		}
		synctest.Wait()
		if checks.Load() != 3 || f.exec.refreshes.Load() != 1 || f.current(t).LastRefreshedAt.IsZero() {
			t.Fatal("background policy retry did not resume when permitted")
		}
		f.requireHealthy(t)
	})
}

func TestAutoRefreshPolicyWorkerControls(t *testing.T) {
	for _, mode := range []string{"no_policy", "permitted", "cancel_during_check"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newPolicyRefreshFixture(t)
				defer f.stop(t)
				var checks atomic.Int64
				if mode != "no_policy" {
					f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(context.Context, AuthUse) error {
						checks.Add(1)
						if mode == "cancel_during_check" {
							f.cancel()
						}
						return nil
					}))
				}
				f.start()
				synctest.Wait()
				wantChecks, wantRefresh := int64(1), int64(1)
				if mode == "no_policy" {
					wantChecks = 0
				}
				if mode == "cancel_during_check" {
					wantRefresh = 0
				}
				if checks.Load() != wantChecks || f.exec.refreshes.Load() != wantRefresh {
					t.Fatal("background policy control dispatched unexpectedly")
				}
				if mode == "cancel_during_check" && !f.current(t).NextRefreshAfter.IsZero() {
					t.Fatal("caller cancellation became policy retry backoff")
				}
				f.requireHealthy(t)
			})
		})
	}
}

func TestAutoRefreshPolicyCompletionPreservesNewerState(t *testing.T) {
	for _, mode := range []string{"new_backoff", "replacement_backoff", "new_registration"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newPolicyRefreshFixture(t)
				defer f.stop(t)
				job := f.refusedJob(t)
				wantBackoff := time.Now().Add(2 * time.Hour)
				var want *Auth
				var err error
				if mode == "new_registration" {
					replacement := f.auth.Clone()
					replacement.Metadata["access_token"] = "synthetic-replacement"
					replacement.NextRefreshAfter = wantBackoff
					want, err = f.manager.Register(f.ctx, replacement)
					if err == nil && want.RegistrationEpoch == job.registrationEpoch {
						t.Fatal("replacement did not receive a new registration epoch")
					}
				} else {
					var changed bool
					want, changed, err = f.manager.Modify(f.ctx, f.auth.ID, 0, func(a *Auth) error {
						a.NextRefreshAfter = wantBackoff
						if mode == "replacement_backoff" {
							a.Metadata["access_token"] = "synthetic-replacement"
						}
						return nil
					})
					if err == nil && !changed {
						t.Fatal("newer refresh state was not committed")
					}
				}
				if err != nil || want == nil || !want.NextRefreshAfter.Equal(wantBackoff) {
					t.Fatal("newer refresh state setup failed")
				}
				// A real queued refusal completed before a later public mutation.
				// Completion may remove its job, but must not overwrite that outcome.
				f.manager.finishRefreshJob(job, time.Now().Add(refreshCheckInterval), false)
				current := f.current(t)
				if !current.NextRefreshAfter.Equal(wantBackoff) || current.RegistrationEpoch != want.RegistrationEpoch ||
					current.CredentialVersion != want.CredentialVersion || current.Metadata["access_token"] != want.Metadata["access_token"] ||
					current.Generation != want.Generation || f.exec.refreshes.Load() != 0 {
					t.Fatal("policy job completion overwrote newer credential state")
				}
				f.loop.applyDirty(time.Now())
				if next, scheduled := f.loop.peek(); !scheduled || !next.Equal(wantBackoff) {
					t.Fatal("policy job completion replaced newer scheduling deadline")
				}
				f.requireHealthy(t)
			})
		})
	}
}

func TestForegroundPolicyRefusalDoesNotScheduleBackgroundRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newPolicyRefreshFixture(t)
		defer f.stop(t)
		f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(context.Context, AuthUse) error { return context.Canceled }))
		_, err := f.manager.ForceRefreshAuth(f.ctx, f.auth.ID)
		requireAuthUseRefusal(t, err)
		if f.exec.refreshes.Load() != 0 || !f.current(t).NextRefreshAfter.IsZero() {
			t.Fatal("foreground policy refusal changed background scheduling")
		}
		f.requireHealthy(t)
	})
}
