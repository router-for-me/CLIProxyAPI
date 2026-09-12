package auth

import (
	"context"
	"sync"
	"testing"
	"time"
)

type refreshJoinStore struct {
	started chan context.Context
	release chan struct{}
	saved   chan struct{}
}

func (s *refreshJoinStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func (s *refreshJoinStore) Delete(context.Context, string) error  { return nil }
func (s *refreshJoinStore) Save(ctx context.Context, auth *Auth) (string, error) {
	s.started <- ctx
	<-s.release
	close(s.saved)
	return auth.ID, nil
}

func TestStopAutoRefreshAndWaitJoinsCredentialPersistence(t *testing.T) {
	store := &refreshJoinStore{started: make(chan context.Context, 1), release: make(chan struct{}), saved: make(chan struct{})}
	manager := NewManager(store, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "join-test"})
	record := &Auth{ID: "join-test.json", Provider: "join-test", Metadata: map[string]any{
		"access_token": "synthetic-token", "expired": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}}
	if _, err := manager.Register(WithSkipPersist(context.Background()), record); err != nil {
		t.Fatal(err)
	}
	manager.StartAutoRefresh(context.Background(), time.Hour)
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(store.release) })
		manager.StopAutoRefreshAndWait()
	})
	manager.mu.RLock()
	loop := manager.refreshLoop
	manager.mu.RUnlock()
	// Schedule explicitly; no timer sleeps or live provider calls are involved.
	loop.jobs <- record.ID
	persistContext := <-store.started
	stopped := make(chan struct{})
	go func() {
		manager.StopAutoRefreshAndWait()
		close(stopped)
	}()
	<-persistContext.Done()
	select {
	case <-stopped:
		t.Fatal("stop returned while credential persistence was still blocked")
	default:
	}
	releaseOnce.Do(func() { close(store.release) })
	<-stopped
	select {
	case <-store.saved:
	default:
		t.Fatal("stop returned before credential persistence completed")
	}
	select {
	case <-loop.done:
	default:
		t.Fatal("refresh loop was not joined")
	}
}
