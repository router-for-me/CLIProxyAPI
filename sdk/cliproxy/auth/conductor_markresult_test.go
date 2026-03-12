package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type blockingStore struct {
	saveStarted chan struct{}
	releaseSave chan struct{}
}

func (s *blockingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *blockingStore) Save(ctx context.Context, auth *Auth) (string, error) {
	select {
	case s.saveStarted <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.releaseSave:
		return auth.ID, nil
	}
}

func (s *blockingStore) Delete(context.Context, string) error { return nil }

func TestManagerMarkResultDoesNotBlockSchedulerOnPersist(t *testing.T) {
	t.Parallel()

	manager := newMarkResultTestManager(t)
	store := &blockingStore{
		saveStarted: make(chan struct{}, 1),
		releaseSave: make(chan struct{}),
	}
	manager.SetStore(store)

	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.MarkResult(context.Background(), Result{
			AuthID:   "gemini-a",
			Provider: "gemini",
			Model:    "test-model",
			Success:  false,
			Error:    &Error{HTTPStatus: 429, Message: "quota"},
		})
	}()

	select {
	case <-store.saveStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for persist to start")
	}

	pickDone := make(chan *Auth, 1)
	go func() {
		got, _, err := manager.pickNext(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, nil)
		if err != nil {
			t.Errorf("pickNext() error = %v", err)
			pickDone <- nil
			return
		}
		pickDone <- got
	}()

	select {
	case got := <-pickDone:
		if got == nil || got.ID != "gemini-b" {
			t.Fatalf("pickNext() auth = %v, want gemini-b", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pickNext blocked on persist")
	}

	mixedDone := make(chan struct {
		auth     *Auth
		provider string
	}, 1)
	go func() {
		got, _, provider, err := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "test-model", cliproxyexecutor.Options{}, nil)
		if err != nil {
			t.Errorf("pickNextMixed() error = %v", err)
			mixedDone <- struct {
				auth     *Auth
				provider string
			}{}
			return
		}
		mixedDone <- struct {
			auth     *Auth
			provider string
		}{auth: got, provider: provider}
	}()

	select {
	case got := <-mixedDone:
		if got.auth == nil {
			t.Fatal("pickNextMixed() auth = nil")
		}
		if got.auth.ID == "gemini-a" {
			t.Fatalf("pickNextMixed() returned cooled auth gemini-a: provider=%s", got.provider)
		}
		if got.provider != "gemini" && got.provider != "claude" {
			t.Fatalf("pickNextMixed() provider = %q, want gemini/claude", got.provider)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pickNextMixed blocked on persist")
	}

	close(store.releaseSave)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for MarkResult to finish")
	}
}

func TestManagerMarkResultConcurrentPickNextAndPickNextMixedPreserveCooldownAndRecovery(t *testing.T) {
	t.Parallel()

	manager := newMarkResultTestManager(t)
	manager.MarkResult(context.Background(), Result{
		AuthID:   "gemini-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  false,
		Error:    &Error{HTTPStatus: 429, Message: "quota"},
	})

	errCh := make(chan error, 128)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iter := 0; iter < 20; iter++ {
				got, _, err := manager.pickNext(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, map[string]struct{}{})
				if err != nil {
					errCh <- fmt.Errorf("worker %d pickNext: %w", worker, err)
					return
				}
				if got == nil || got.ID != "gemini-b" {
					errCh <- fmt.Errorf("worker %d pickNext got %v, want gemini-b", worker, got)
					return
				}

				gotMixed, _, provider, err := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "test-model", cliproxyexecutor.Options{}, map[string]struct{}{})
				if err != nil {
					errCh <- fmt.Errorf("worker %d pickNextMixed: %w", worker, err)
					return
				}
				if gotMixed == nil {
					errCh <- fmt.Errorf("worker %d pickNextMixed returned nil auth", worker)
					return
				}
				if gotMixed.ID == "gemini-a" {
					errCh <- fmt.Errorf("worker %d pickNextMixed returned cooled auth gemini-a", worker)
					return
				}
				if provider != "gemini" && provider != "claude" {
					errCh <- fmt.Errorf("worker %d pickNextMixed provider=%q", worker, provider)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	recoveredManager := newMarkResultTestManager(t)
	recoveredManager.MarkResult(context.Background(), Result{
		AuthID:   "gemini-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  false,
		Error:    &Error{HTTPStatus: 429, Message: "quota"},
	})
	recoveredManager.MarkResult(context.Background(), Result{
		AuthID:   "gemini-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  true,
	})

	seenSingle := make(map[string]struct{}, 2)
	for index := 0; index < 8; index++ {
		got, _, err := recoveredManager.pickNext(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, map[string]struct{}{})
		if err != nil {
			t.Fatalf("pickNext() after recovery #%d error = %v", index, err)
		}
		if got == nil {
			t.Fatalf("pickNext() after recovery #%d auth = nil", index)
		}
		seenSingle[got.ID] = struct{}{}
	}
	if len(seenSingle) != 2 {
		t.Fatalf("after recovery pickNext seen %v, want both gemini-a and gemini-b", seenSingle)
	}

	seenMixedGemini := make(map[string]struct{}, 2)
	seenProviders := make(map[string]struct{}, 2)
	for index := 0; index < 12; index++ {
		got, _, provider, err := recoveredManager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "test-model", cliproxyexecutor.Options{}, map[string]struct{}{})
		if err != nil {
			t.Fatalf("pickNextMixed() after recovery #%d error = %v", index, err)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() after recovery #%d auth = nil", index)
		}
		seenProviders[provider] = struct{}{}
		if provider == "gemini" {
			seenMixedGemini[got.ID] = struct{}{}
		}
	}
	if len(seenProviders) != 2 {
		t.Fatalf("after recovery mixed providers seen %v, want gemini and claude", seenProviders)
	}
	if len(seenMixedGemini) != 2 {
		t.Fatalf("after recovery mixed gemini auths seen %v, want both gemini-a and gemini-b", seenMixedGemini)
	}
}

func newMarkResultTestManager(t *testing.T) *Manager {
	t.Helper()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}

	reg := registry.GetGlobalRegistry()
	for _, item := range []struct {
		id       string
		provider string
	}{
		{id: "gemini-a", provider: "gemini"},
		{id: "gemini-b", provider: "gemini"},
		{id: "claude-a", provider: "claude"},
	} {
		reg.RegisterClient(item.id, item.provider, []*registry.ModelInfo{{ID: "test-model"}})
	}
	t.Cleanup(func() {
		reg.UnregisterClient("gemini-a")
		reg.UnregisterClient("gemini-b")
		reg.UnregisterClient("claude-a")
	})

	for _, auth := range []*Auth{
		{ID: "gemini-a", Provider: "gemini", Metadata: map[string]any{"persist": true}},
		{ID: "gemini-b", Provider: "gemini", Metadata: map[string]any{"persist": true}},
		{ID: "claude-a", Provider: "claude", Metadata: map[string]any{"persist": true}},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
	}
	return manager
}
