package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestPickFillFirstAuthID_CapacitySpillover(t *testing.T) {
	t.Parallel()

	ordered := []string{"a", "b", "c"}

	if got := pickFillFirstAuthID(ordered, nil, 0); got != "a" {
		t.Fatalf("unlimited pick = %q, want a", got)
	}
	if got := pickFillFirstAuthID(ordered, map[string]int{"a": 1}, 2); got != "a" {
		t.Fatalf("under capacity pick = %q, want a", got)
	}
	if got := pickFillFirstAuthID(ordered, map[string]int{"a": 2}, 2); got != "b" {
		t.Fatalf("spill pick = %q, want b", got)
	}
	if got := pickFillFirstAuthID(ordered, map[string]int{"a": 5, "b": 3, "c": 4}, 2); got != "b" {
		t.Fatalf("soft overflow pick = %q, want least-loaded b", got)
	}
}

func TestFillFirstInflightTracker_AcquireRelease(t *testing.T) {
	t.Parallel()

	tracker := newFillFirstInflightTracker()
	release1 := tracker.acquire("auth-a")
	release2 := tracker.acquire("auth-a")
	if got := tracker.get("auth-a"); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	release1()
	release1() // once-only
	if got := tracker.get("auth-a"); got != 1 {
		t.Fatalf("count after one release = %d, want 1", got)
	}
	release2()
	if got := tracker.get("auth-a"); got != 0 {
		t.Fatalf("count after full release = %d, want 0", got)
	}
}

type fillFirstBusyExecutor struct {
	provider string
	started  chan string
	release  chan struct{}
	active   atomic.Int32
}

func (e *fillFirstBusyExecutor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "gemini"
}

func (e *fillFirstBusyExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.active.Add(1)
	defer e.active.Add(-1)
	if e.started != nil {
		select {
		case e.started <- auth.ID:
		case <-ctx.Done():
			return cliproxyexecutor.Response{}, ctx.Err()
		}
	}
	if e.release != nil {
		select {
		case <-e.release:
		case <-ctx.Done():
			return cliproxyexecutor.Response{}, ctx.Err()
		}
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *fillFirstBusyExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *fillFirstBusyExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *fillFirstBusyExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *fillFirstBusyExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestFillFirstMaxInflight_SpillsAcrossCredentials(t *testing.T) {
	selector := &FillFirstSelector{seed: 42}
	manager := NewManager(nil, selector, nil)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy:             "fill-first",
			FillFirstMaxInflight: 1,
		},
	})
	manager.SetSelector(selector)

	auths := []*Auth{
		{ID: "fill-first-inflight-a", Provider: "gemini", Status: StatusActive},
		{ID: "fill-first-inflight-b", Provider: "gemini", Status: StatusActive},
		{ID: "fill-first-inflight-c", Provider: "gemini", Status: StatusActive},
	}
	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() {
		for _, auth := range auths {
			reg.UnregisterClient(auth.ID)
		}
	})
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
		reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: "gemini-2.5-pro"}})
		manager.RefreshSchedulerEntry(auth.ID)
	}

	started := make(chan string, 8)
	release := make(chan struct{})
	executor := &fillFirstBusyExecutor{provider: "gemini", started: started, release: release}
	manager.RegisterExecutor(executor)

	var wg sync.WaitGroup
	errCh := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := manager.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{
				Model:   "gemini-2.5-pro",
				Payload: []byte(`{}`),
			}, cliproxyexecutor.Options{})
			errCh <- err
		}()
	}

	seen := make(map[string]struct{})
	deadline := time.After(3 * time.Second)
	for len(seen) < 3 {
		select {
		case id := <-started:
			seen[id] = struct{}{}
		case <-deadline:
			t.Fatalf("timed out waiting for spillover; started=%v", keysOf(seen))
		}
	}
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("Execute error = %v", err)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 distinct auths under max-inflight=1, got %v", keysOf(seen))
	}
}

func keysOf(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}
