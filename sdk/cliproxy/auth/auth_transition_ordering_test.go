package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type authTransitionSelector struct{}

func (authTransitionSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, errors.New("fixture has no candidate")
	}
	return auths[0].Clone(), nil
}

func TestAuthTransitionOrdersBeforeCheckAndMutation(t *testing.T) {
	for _, next := range []string{"check", "mutation"} {
		t.Run(next, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newAuthUseFixture(t)
				// Use the public selector path so the next request reads published
				// manager state before the refresh's post-commit scheduler update.
				f.manager.SetSelector(authTransitionSelector{})
				e := &authTransitionExecutor{authUseExecutor: f.exec, refresh: rotatedTransitionAuth}
				f.manager.RegisterExecutor(e)
				entered, release := make(chan struct{}, 1), make(chan struct{})
				var releaseOnce sync.Once
				var approved, managerUnlocked atomic.Bool
				var executionChecks atomic.Int64
				f.manager.SetAuthExecutionPolicy(&authTransitionPolicy{
					check: func(_ context.Context, use AuthUse) error {
						if use.Kind == AuthUseExecute {
							executionChecks.Add(1)
							if !approved.Load() || authAccessToken(use.Auth) != "synthetic-rotated" {
								return errors.New("fixture transition not observed")
							}
						}
						return nil
					},
					observe: func(_ context.Context, event AuthTransition) {
						if event.Kind != AuthUseRefresh {
							t.Error("unexpected fixture transition kind")
						}
						// The observer must not run under Manager.mu. Inspect the
						// lock directly rather than reentering a Manager method.
						if f.manager.mu.TryLock() {
							managerUnlocked.Store(true)
							f.manager.mu.Unlock()
						}
						entered <- struct{}{}
						<-release
						approved.Store(true)
					},
				})
				refreshResult, refreshDone := make(chan error, 1), make(chan struct{})
				go func() {
					defer close(refreshDone)
					_, err := f.manager.ForceRefreshAuth(context.Background(), f.auth.ID)
					refreshResult <- err
				}()
				defer func() {
					releaseOnce.Do(func() { close(release) })
					synctest.Wait()
					<-refreshDone
				}()
				synctest.Wait()
				select {
				case <-entered:
				default:
					t.Fatal("provider commit did not enter observer")
				}
				current, ok := f.manager.GetByID(f.auth.ID)
				if !ok || authAccessToken(current) != "synthetic-rotated" || !managerUnlocked.Load() {
					t.Fatal("observer did not follow publication outside manager lock")
				}
				nextResult, nextDone := make(chan error, 1), make(chan struct{})
				go func() {
					defer close(nextDone)
					if next == "check" {
						nextResult <- f.invoke(AuthUseExecute, cliproxyexecutor.Options{})
						return
					}
					_, _, err := f.manager.Modify(context.Background(), f.auth.ID, 0, func(a *Auth) error {
						a.Metadata["access_token"] = "synthetic-manual"
						return nil
					})
					nextResult <- err
				}()
				defer func() {
					releaseOnce.Do(func() { close(release) })
					synctest.Wait()
					<-nextDone
				}()
				synctest.Wait()
				select {
				case <-nextDone:
					t.Fatal("next operation passed a pending transition observer")
				default:
				}
				_, inputs := f.exec.observed()
				if len(inputs) != 0 || executionChecks.Load() != 0 {
					t.Fatal("execution checked or dispatched before commit observation")
				}
				releaseOnce.Do(func() { close(release) })
				synctest.Wait()
				if <-refreshResult != nil || <-nextResult != nil {
					t.Fatal("ordered operation failed after observer completed")
				}
				_, inputs = f.exec.observed()
				if next == "check" && (len(inputs) != 1 || executionChecks.Load() != 1 || authAccessToken(inputs[0]) != "synthetic-rotated") {
					t.Fatal("next request did not use observed refreshed credential")
				}
			})
		})
	}
}
