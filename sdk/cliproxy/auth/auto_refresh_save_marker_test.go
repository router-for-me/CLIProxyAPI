package auth

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestBackgroundRefreshMarkerCreatedDuringSavePreservesOutcome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newObsoleteRefreshFixture(t, true, false)
		defer f.stop(t)
		store := &obsoleteRefreshSaveStore{entered: make(chan struct{}), unblock: make(chan struct{})}
		defer store.release()
		f.manager.SetStore(store)
		f.manager.RegisterExecutor(&obsoleteSameTokenExecutor{obsoleteRefreshExecutor: f.exec, outcome: "ineffective"})
		f.foregroundDone = make(chan struct{})
		result := make(chan error, 1)
		go func() {
			defer close(f.foregroundDone)
			_, err := f.manager.ForceRefreshAuth(context.Background(), f.id)
			result <- err
		}()
		awaitObsoleteRefresh(t, store.entered)
		// Provider work has finished; the final manager commit is held in the
		// real Store.Save path with manager.mu released. No job existed earlier.
		f.queueDue(t)
		pending := f.current(t).NextRefreshAfter
		if !store.outcome.After(time.Now()) || store.outcome.Equal(pending) {
			t.Fatal("obsolete refresh save precondition")
		}
		store.release()
		awaitObsoleteRefresh(t, f.foregroundDone)
		if <-result != nil {
			t.Fatal("obsolete refresh save foreground")
		}
		if !f.current(t).NextRefreshAfter.Equal(store.outcome) {
			t.Error("obsolete refresh save marker overwrote outcome")
		}
		f.startWorker(t)
		synctest.Wait()
		requireObsoleteJobGone(t, f)
		if !f.current(t).NextRefreshAfter.Equal(store.outcome) || f.exec.oldRefresh.Load() != 1 || f.exec.freshRefresh.Load() != 0 {
			t.Fatal("obsolete refresh save extra dispatch")
		}
	})
}

type obsoleteRefreshSaveStore struct {
	entered, unblock       chan struct{}
	enterOnce, releaseOnce sync.Once
	outcome                time.Time // Published by closing entered; never changed afterward.
}

func (s *obsoleteRefreshSaveStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.enterOnce.Do(func() {
		s.outcome = auth.NextRefreshAfter
		close(s.entered)
	})
	<-s.unblock
	return "", nil
}
func (s *obsoleteRefreshSaveStore) release() { s.releaseOnce.Do(func() { close(s.unblock) }) }
func (*obsoleteRefreshSaveStore) List(context.Context) ([]*Auth, error) {
	return nil, nil
}
func (*obsoleteRefreshSaveStore) Delete(context.Context, string) error { return nil }
