package cliproxy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type issue8543BlockingStore struct {
	mu           sync.Mutex
	savedTokens  map[string]string
	blockToken   string
	saveEntered  chan struct{}
	enterOnce    sync.Once
	releaseSave  chan struct{}
	saveFinished chan struct{}
	finishOnce   sync.Once
}

func newIssue8543BlockingStore(blockToken string) *issue8543BlockingStore {
	return &issue8543BlockingStore{
		savedTokens:  make(map[string]string),
		blockToken:   blockToken,
		saveEntered:  make(chan struct{}),
		releaseSave:  make(chan struct{}),
		saveFinished: make(chan struct{}),
	}
}

func (s *issue8543BlockingStore) List(_ context.Context) ([]*coreauth.Auth, error) {
	return nil, nil
}

func (s *issue8543BlockingStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	token, _ := auth.Metadata["refresh_token"].(string)
	if s.blockToken != "" && token == s.blockToken {
		s.enterOnce.Do(func() { close(s.saveEntered) })
		<-s.releaseSave
	}
	s.mu.Lock()
	s.savedTokens[auth.ID] = token
	s.mu.Unlock()
	if s.blockToken != "" && token == s.blockToken {
		s.finishOnce.Do(func() { close(s.saveFinished) })
	}
	return auth.ID, nil
}

func (s *issue8543BlockingStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.savedTokens, id)
	s.mu.Unlock()
	return nil
}

func (s *issue8543BlockingStore) savedRefreshToken(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.savedTokens[id]
}

type issue8543RotatingExecutor struct {
	serviceTestPluginExecutor
	provider string
	newToken string
}

func (e *issue8543RotatingExecutor) Identifier() string { return e.provider }

func (e *issue8543RotatingExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["refresh_token"] = e.newToken
	updated.Metadata["access_token"] = "access-" + e.newToken
	return updated, nil
}

type issue8543RefreshEvaluator struct {
	wantToken string
}

func (e issue8543RefreshEvaluator) ShouldRefresh(_ time.Time, auth *coreauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	tok, _ := auth.Metadata["refresh_token"].(string)
	return tok != e.wantToken
}

type issue8543ShutdownHook struct {
	coreauth.NoopHook
	targetToken string
	onMatch     func()
	once        sync.Once
}

func (h *issue8543ShutdownHook) OnAuthUpdated(_ context.Context, auth *coreauth.Auth) {
	if auth == nil || auth.Metadata == nil {
		return
	}
	tok, _ := auth.Metadata["refresh_token"].(string)
	if tok == h.targetToken {
		h.once.Do(h.onMatch)
	}
}

func TestServiceShutdownWaitsForInFlightAutoRefreshSave(t *testing.T) {
	store := newIssue8543BlockingStore("rt-rotated")
	cfg := &config.Config{AuthAutoRefreshWorkers: 1}
	m := coreauth.NewManager(store, nil, nil)
	m.SetConfig(cfg)
	m.RegisterExecutor(&issue8543RotatingExecutor{provider: "issue8543", newToken: "rt-rotated"})

	const authID = "issue8543-wait-save"
	if _, err := m.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "issue8543",
		Status:   coreauth.StatusActive,
		Runtime:  issue8543RefreshEvaluator{wantToken: "rt-rotated"},
		Metadata: map[string]any{
			"access_token":  "access-old",
			"refresh_token": "rt-old",
		},
	}); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	s := &Service{cfg: cfg, coreManager: m}
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	s.runCancel = runCancel

	m.StartAutoRefresh(runCtx, time.Millisecond)

	select {
	case <-store.saveEntered:
	case <-time.After(5 * time.Second):
		close(store.releaseSave)
		t.Fatal("auto-refresh did not reach store.Save")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- s.Shutdown(context.Background())
	}()

	select {
	case err := <-shutdownDone:
		close(store.releaseSave)
		t.Fatalf("Shutdown returned %v while auto-refresh save was still in progress", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(store.releaseSave)

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return after store.Save completed")
	}

	if got := store.savedRefreshToken(authID); got != "rt-rotated" {
		t.Fatalf("saved refresh_token = %q, want %q", got, "rt-rotated")
	}
}

func TestServiceShutdownReturnsContextErrorWhenAutoRefreshSaveExceedsDeadline(t *testing.T) {
	store := newIssue8543BlockingStore("rt-rotated")
	defer func() {
		select {
		case <-store.releaseSave:
		default:
			close(store.releaseSave)
		}
	}()

	cfg := &config.Config{AuthAutoRefreshWorkers: 1}
	m := coreauth.NewManager(store, nil, nil)
	m.SetConfig(cfg)
	m.RegisterExecutor(&issue8543RotatingExecutor{provider: "issue8543", newToken: "rt-rotated"})

	const authID = "issue8543-timeout-save"
	if _, err := m.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "issue8543",
		Status:   coreauth.StatusActive,
		Runtime:  issue8543RefreshEvaluator{wantToken: "rt-rotated"},
		Metadata: map[string]any{
			"access_token":  "access-old",
			"refresh_token": "rt-old",
		},
	}); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	s := &Service{cfg: cfg, coreManager: m}
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	s.runCancel = runCancel

	m.StartAutoRefresh(runCtx, time.Millisecond)

	select {
	case <-store.saveEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("auto-refresh did not reach store.Save")
	}

	shutdownCtx, cancelShutdown := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelShutdown()

	errShutdown := s.Shutdown(shutdownCtx)
	if !errors.Is(errShutdown, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want error wrapping context.DeadlineExceeded", errShutdown)
	}

	close(store.releaseSave)
	select {
	case <-store.saveFinished:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight save did not finish after release")
	}
}

func TestServiceShutdownFromRefreshHookDoesNotDeadlock(t *testing.T) {
	store := newIssue8543BlockingStore("")
	cfg := &config.Config{AuthAutoRefreshWorkers: 1}

	var svc *Service
	shutdownErrCh := make(chan error, 1)
	hook := &issue8543ShutdownHook{
		targetToken: "rt-rotated",
		onMatch: func() {
			shutdownErrCh <- svc.Shutdown(context.Background())
		},
	}

	m := coreauth.NewManager(store, nil, hook)
	m.SetConfig(cfg)
	m.RegisterExecutor(&issue8543RotatingExecutor{provider: "issue8543", newToken: "rt-rotated"})

	const authID = "issue8543-hook-shutdown"
	if _, err := m.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "issue8543",
		Status:   coreauth.StatusActive,
		Runtime:  issue8543RefreshEvaluator{wantToken: "rt-rotated"},
		Metadata: map[string]any{
			"access_token":  "access-old",
			"refresh_token": "rt-old",
		},
	}); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	svc = &Service{cfg: cfg, coreManager: m}
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	svc.runCancel = runCancel

	m.StartAutoRefresh(runCtx, time.Millisecond)

	select {
	case err := <-shutdownErrCh:
		if err != nil {
			t.Fatalf("Shutdown from OnAuthUpdated error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown called from refresh OnAuthUpdated hook deadlocked")
	}

	if got := store.savedRefreshToken(authID); got != "rt-rotated" {
		t.Fatalf("saved refresh_token = %q, want %q", got, "rt-rotated")
	}
}

func TestServiceShutdownRepeatedAndConcurrentCallsHonorPerCallDeadline(t *testing.T) {
	t.Run("sequential call after timeout still waits and errors while save is blocked", func(t *testing.T) {
		store := newIssue8543BlockingStore("rt-rotated")
		defer func() {
			select {
			case <-store.releaseSave:
			default:
				close(store.releaseSave)
			}
		}()

		cfg := &config.Config{AuthAutoRefreshWorkers: 1}
		m := coreauth.NewManager(store, nil, nil)
		m.SetConfig(cfg)
		m.RegisterExecutor(&issue8543RotatingExecutor{provider: "issue8543", newToken: "rt-rotated"})

		const authID = "issue8543-repeated-shutdown"
		if _, err := m.Register(context.Background(), &coreauth.Auth{
			ID:       authID,
			Provider: "issue8543",
			Status:   coreauth.StatusActive,
			Runtime:  issue8543RefreshEvaluator{wantToken: "rt-rotated"},
			Metadata: map[string]any{
				"access_token":  "access-old",
				"refresh_token": "rt-old",
			},
		}); err != nil {
			t.Fatalf("Register error: %v", err)
		}

		s := &Service{cfg: cfg, coreManager: m}
		runCtx, runCancel := context.WithCancel(context.Background())
		defer runCancel()
		s.runCancel = runCancel

		m.StartAutoRefresh(runCtx, time.Millisecond)

		select {
		case <-store.saveEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("auto-refresh did not reach store.Save")
		}

		firstCtx, firstCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer firstCancel()
		if errFirst := s.Shutdown(firstCtx); !errors.Is(errFirst, context.DeadlineExceeded) {
			t.Fatalf("first Shutdown error = %v, want context.DeadlineExceeded", errFirst)
		}

		secondCtx, secondCancel := context.WithCancel(context.Background())
		secondCancel()
		if errSecond := s.Shutdown(secondCtx); !errors.Is(errSecond, context.Canceled) {
			t.Fatalf("second Shutdown error = %v, want context.Canceled while save is still blocked", errSecond)
		}

		thirdDone := make(chan error, 1)
		go func() {
			thirdDone <- s.Shutdown(context.Background())
		}()

		select {
		case err := <-thirdDone:
			t.Fatalf("third Shutdown returned %v while save was still blocked", err)
		case <-time.After(50 * time.Millisecond):
		}

		close(store.releaseSave)

		select {
		case err := <-thirdDone:
			if err != nil {
				t.Fatalf("third Shutdown error = %v, want nil after save finishes", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("third Shutdown did not return after store.Save completed")
		}
	})

	t.Run("concurrent call with shorter deadline does not block behind background shutdown", func(t *testing.T) {
		store := newIssue8543BlockingStore("rt-rotated")
		defer func() {
			select {
			case <-store.releaseSave:
			default:
				close(store.releaseSave)
			}
		}()

		cfg := &config.Config{AuthAutoRefreshWorkers: 1}
		m := coreauth.NewManager(store, nil, nil)
		m.SetConfig(cfg)
		m.RegisterExecutor(&issue8543RotatingExecutor{provider: "issue8543", newToken: "rt-rotated"})

		const authID = "issue8543-concurrent-shutdown"
		if _, err := m.Register(context.Background(), &coreauth.Auth{
			ID:       authID,
			Provider: "issue8543",
			Status:   coreauth.StatusActive,
			Runtime:  issue8543RefreshEvaluator{wantToken: "rt-rotated"},
			Metadata: map[string]any{
				"access_token":  "access-old",
				"refresh_token": "rt-old",
			},
		}); err != nil {
			t.Fatalf("Register error: %v", err)
		}

		s := &Service{cfg: cfg, coreManager: m}
		runCtx, runCancel := context.WithCancel(context.Background())
		defer runCancel()
		s.runCancel = runCancel

		m.StartAutoRefresh(runCtx, time.Millisecond)

		select {
		case <-store.saveEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("auto-refresh did not reach store.Save")
		}

		firstDone := make(chan error, 1)
		go func() {
			firstDone <- s.Shutdown(context.Background())
		}()

		// Wait until the first Shutdown is actively waiting on auto-refresh save.
		deadline := time.Now().Add(5 * time.Second)
		for s.shutdownRefreshWaiters.Load() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("first Shutdown did not enter auto-refresh wait")
			}
			time.Sleep(time.Millisecond)
		}

		shortCtx, shortCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer shortCancel()
		secondDone := make(chan error, 1)
		go func() {
			secondDone <- s.Shutdown(shortCtx)
		}()

		select {
		case errSecond := <-secondDone:
			if !errors.Is(errSecond, context.DeadlineExceeded) {
				t.Fatalf("concurrent Shutdown error = %v, want context.DeadlineExceeded", errSecond)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent Shutdown with shorter deadline blocked behind first Shutdown")
		}

		// First Shutdown must still be waiting until store.Save finishes.
		select {
		case errFirst := <-firstDone:
			t.Fatalf("first Shutdown returned %v while save was still blocked", errFirst)
		case <-time.After(50 * time.Millisecond):
		}

		close(store.releaseSave)

		select {
		case errFirst := <-firstDone:
			if errFirst != nil {
				t.Fatalf("first Shutdown error = %v, want nil", errFirst)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("first Shutdown did not return after store.Save completed")
		}
	})

	t.Run("concurrent call after first times out into blocked teardown still honors its own deadline", func(t *testing.T) {
		store := newIssue8543BlockingStore("rt-rotated")
		defer func() {
			select {
			case <-store.releaseSave:
			default:
				close(store.releaseSave)
			}
		}()

		watcherEntered := make(chan struct{})
		releaseWatcher := make(chan struct{})
		defer func() {
			select {
			case <-releaseWatcher:
			default:
				close(releaseWatcher)
			}
		}()

		cfg := &config.Config{AuthAutoRefreshWorkers: 1}
		m := coreauth.NewManager(store, nil, nil)
		m.SetConfig(cfg)
		m.RegisterExecutor(&issue8543RotatingExecutor{provider: "issue8543", newToken: "rt-rotated"})

		const authID = "issue8543-teardown-blocked"
		if _, err := m.Register(context.Background(), &coreauth.Auth{
			ID:       authID,
			Provider: "issue8543",
			Status:   coreauth.StatusActive,
			Runtime:  issue8543RefreshEvaluator{wantToken: "rt-rotated"},
			Metadata: map[string]any{
				"access_token":  "access-old",
				"refresh_token": "rt-old",
			},
		}); err != nil {
			t.Fatalf("Register error: %v", err)
		}

		s := &Service{
			cfg:         cfg,
			coreManager: m,
			watcher: &WatcherWrapper{
				stop: func() error {
					close(watcherEntered)
					<-releaseWatcher
					return nil
				},
			},
		}
		runCtx, runCancel := context.WithCancel(context.Background())
		defer runCancel()
		s.runCancel = runCancel

		m.StartAutoRefresh(runCtx, time.Millisecond)

		select {
		case <-store.saveEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("auto-refresh did not reach store.Save")
		}

		firstCtx, firstCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer firstCancel()
		firstDone := make(chan error, 1)
		go func() {
			firstDone <- s.Shutdown(firstCtx)
		}()

		select {
		case <-watcherEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("first Shutdown did not enter watcher.Stop")
		}

		secondCtx, secondCancel := context.WithCancel(context.Background())
		secondCancel()
		secondDone := make(chan error, 1)
		go func() {
			secondDone <- s.Shutdown(secondCtx)
		}()

		select {
		case errSecond := <-secondDone:
			if !errors.Is(errSecond, context.Canceled) {
				t.Fatalf("second Shutdown error = %v, want context.Canceled", errSecond)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("second Shutdown blocked behind first Shutdown's teardown instead of returning ctx.Err()")
		}

		close(releaseWatcher)
		select {
		case errFirst := <-firstDone:
			if !errors.Is(errFirst, context.DeadlineExceeded) {
				t.Fatalf("first Shutdown error = %v, want context.DeadlineExceeded", errFirst)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("first Shutdown did not return after watcher.Stop unblocked")
		}

		close(store.releaseSave)
		select {
		case <-store.saveFinished:
		case <-time.After(5 * time.Second):
			t.Fatal("in-flight save did not finish after release")
		}
	})
}

func TestServiceShutdownPreservesRefreshTimeoutWhenWatcherStopAlsoFails(t *testing.T) {
	store := newIssue8543BlockingStore("rt-rotated")
	defer func() {
		select {
		case <-store.releaseSave:
		default:
			close(store.releaseSave)
		}
	}()

	cfg := &config.Config{AuthAutoRefreshWorkers: 1}
	m := coreauth.NewManager(store, nil, nil)
	m.SetConfig(cfg)
	m.RegisterExecutor(&issue8543RotatingExecutor{provider: "issue8543", newToken: "rt-rotated"})

	const authID = "issue8543-dual-error"
	if _, err := m.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "issue8543",
		Status:   coreauth.StatusActive,
		Runtime:  issue8543RefreshEvaluator{wantToken: "rt-rotated"},
		Metadata: map[string]any{
			"access_token":  "access-old",
			"refresh_token": "rt-old",
		},
	}); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	watcherErr := errors.New("watcher stop boom")
	s := &Service{
		cfg:         cfg,
		coreManager: m,
		watcher: &WatcherWrapper{
			stop: func() error { return watcherErr },
		},
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	s.runCancel = runCancel

	m.StartAutoRefresh(runCtx, time.Millisecond)

	select {
	case <-store.saveEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("auto-refresh did not reach store.Save")
	}

	shutdownCtx, cancelShutdown := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelShutdown()

	errShutdown := s.Shutdown(shutdownCtx)
	if !errors.Is(errShutdown, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want error wrapping context.DeadlineExceeded", errShutdown)
	}
	if !errors.Is(errShutdown, watcherErr) {
		t.Fatalf("Shutdown error = %v, want error wrapping watcher stop error", errShutdown)
	}

	close(store.releaseSave)
	select {
	case <-store.saveFinished:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight save did not finish after release")
	}
}
