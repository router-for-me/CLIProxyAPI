package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type authPolicyMetadataStore struct {
	authTransitionStore
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	canceled atomic.Bool
}

func (s *authPolicyMetadataStore) Save(ctx context.Context, _ *Auth) (string, error) {
	if isMetadataPersistContext(ctx) {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
			return "", nil
		case <-ctx.Done():
			s.canceled.Store(true)
			return "", ctx.Err()
		}
	}
	return "", nil
}

func TestAuthExecutionPolicyWaitsForMetadataSaveWithoutPreemption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newAuthUseFixture(t)
		store := &authPolicyMetadataStore{entered: make(chan struct{}), release: make(chan struct{})}
		f.manager.SetStore(store)
		f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(context.Context, AuthUse) error { return nil }))
		var releaseOnce sync.Once
		metadataDone := make(chan struct{})
		var updated, preempted bool
		go func() {
			defer close(metadataDone)
			updated, preempted = f.manager.PersistMetadataEntry(context.Background(), f.auth.ID, f.auth.RegistrationEpoch, "policy_fixture", true, nil)
		}()
		defer func() {
			releaseOnce.Do(func() { close(store.release) })
			synctest.Wait()
			<-metadataDone
		}()
		synctest.Wait()
		select {
		case <-store.entered:
		default:
			t.Fatal("metadata save did not reach actual store")
		}
		executionResult, executionDone := make(chan error, 1), make(chan struct{})
		go func() {
			defer close(executionDone)
			executionResult <- f.invoke(AuthUseExecute, cliproxyexecutor.Options{})
		}()
		defer func() {
			releaseOnce.Do(func() { close(store.release) })
			synctest.Wait()
			<-executionDone
		}()
		synctest.Wait()
		_, inputs := f.exec.observed()
		if store.canceled.Load() || len(inputs) != 0 {
			t.Fatal("execution bypassed or preempted metadata persistence")
		}
		select {
		case <-executionDone:
			t.Fatal("execution did not wait for metadata save")
		default:
		}
		releaseOnce.Do(func() { close(store.release) })
		synctest.Wait()
		if <-executionResult != nil || !updated || preempted || store.canceled.Load() {
			t.Fatal("read barrier changed metadata persistence outcome")
		}
		current, ok := f.manager.GetByID(f.auth.ID)
		_, inputs = f.exec.observed()
		if !ok || current.Metadata["policy_fixture"] != true || len(inputs) != 1 {
			t.Fatal("metadata save or following provider execution was lost")
		}
	})
}
