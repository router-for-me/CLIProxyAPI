package auth

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

func TestCanceledRefreshPreservesReplacement(t *testing.T) {
	for _, replace := range []string{"modify", "register"} {
		t.Run(replace, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newObsoleteRefreshFixture(t, true, true)
				defer f.stop(t)
				f.manager.RegisterExecutor(&obsoleteCanceledExecutor{f.exec})
				before := f.current(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				f.foregroundDone = make(chan struct{})
				result := make(chan error, 1)
				go func() {
					defer close(f.foregroundDone)
					_, err := f.manager.ForceRefreshAuth(ctx, f.id)
					result <- err
				}()
				awaitObsoleteRefresh(t, f.exec.entered)
				install := func(auth *Auth) error {
					auth.Metadata["access_token"] = "synthetic-obsolete-c"
					auth.Metadata["refresh_token"] = "synthetic-obsolete-grant-c"
					auth.Metadata["expired"] = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
					auth.NextRefreshAfter = time.Time{}
					return nil
				}
				if replace == "modify" {
					_, committed, err := f.manager.Modify(WithSkipPersist(context.Background()), f.id, before.RegistrationEpoch, install)
					if err != nil || !committed {
						t.Fatal("obsolete refresh canceled replacement setup")
					}
				} else {
					replacement := before.Clone()
					_ = install(replacement)
					if _, err := f.manager.Register(WithSkipPersist(context.Background()), replacement); err != nil {
						t.Fatal("obsolete refresh canceled replacement setup")
					}
				}
				replacement := f.current(t)
				newEpoch := replacement.RegistrationEpoch != before.RegistrationEpoch
				if newEpoch != (replace == "register") || (!newEpoch && replacement.CredentialVersion <= before.CredentialVersion) || !replacement.NextRefreshAfter.IsZero() {
					t.Fatal("obsolete refresh canceled replacement precondition")
				}
				cancel()
				f.exec.release()
				awaitObsoleteRefresh(t, f.foregroundDone)
				if !errors.Is(<-result, context.Canceled) {
					t.Fatal("obsolete refresh canceled foreground result")
				}
				current := f.current(t)
				if current.RegistrationEpoch != replacement.RegistrationEpoch || current.CredentialVersion != replacement.CredentialVersion || current.Generation != replacement.Generation || !current.NextRefreshAfter.Equal(replacement.NextRefreshAfter) || current.Status != replacement.Status || !reflect.DeepEqual(current.Metadata, replacement.Metadata) {
					t.Error("obsolete refresh canceled replacement changed")
				}
				if f.exec.oldRefresh.Load() != 1 || f.exec.freshRefresh.Load() != 0 {
					t.Fatal("obsolete refresh canceled replacement dispatch")
				}
			})
		})
	}
}

type obsoleteCanceledExecutor struct{ *obsoleteRefreshExecutor }

func (e *obsoleteCanceledExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	_, err := e.obsoleteRefreshExecutor.Refresh(ctx, auth)
	if err != nil {
		return nil, err
	}
	return nil, ctx.Err()
}
