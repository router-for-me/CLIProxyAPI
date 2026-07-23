package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestHomeDispatchSelectionOwnsScopeOutsideAuth(t *testing.T) {
	registry := executionregistry.New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, executionregistry.ScopeSpec{RequestID: "req-1", CredentialID: "cred-1", Model: "gpt", Kind: "http", StartedAt: time.Now()})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	selection, errSelection := newHomeDispatchSelection(&Auth{ID: "cred-1", Provider: "codex"}, nil, "codex", scope)
	if errSelection != nil {
		t.Fatal(errSelection)
	}
	clone := selection.CloneAuth()
	if clone == nil || clone.ID != "cred-1" || clone.Runtime != nil {
		t.Fatalf("clone = %#v", clone)
	}
	closed := atomic.Int32{}
	if errBind := selection.Bind(func() error { closed.Add(1); return nil }); errBind != nil {
		t.Fatal(errBind)
	}
	selection.End("completed")
	selection.End("duplicate")
	if closed.Load() != 1 {
		t.Fatalf("close calls = %d", closed.Load())
	}
}

func TestHomeDispatchSelectionDrainsResourcesAddedDuringEnd(t *testing.T) {
	registry := executionregistry.New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, executionregistry.ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	selection, errSelection := newHomeDispatchSelection(&Auth{ID: "cred-1"}, nil, "test", scope)
	if errSelection != nil {
		t.Fatal(errSelection)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	if errBind := selection.Bind(func() error {
		close(started)
		<-release
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}

	done := make(chan struct{})
	go func() {
		selection.End("draining")
		close(done)
	}()
	<-started

	closedLate := atomic.Int32{}
	errLate := selection.Bind(func() error {
		closedLate.Add(1)
		return errors.New("late close")
	})
	if !errors.Is(errLate, executionregistry.ErrRegistryNotAccepting) {
		t.Fatalf("late Bind() error = %v, want ErrRegistryNotAccepting", errLate)
	}
	if closedLate.Load() != 1 {
		t.Fatalf("late close calls = %d, want 1", closedLate.Load())
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("End did not complete")
	}

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if errDrain := registry.Drain(drainCtx); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

type gatedHomeDispatcher struct {
	loaded  chan struct{}
	release chan struct{}
	rpop    atomic.Int32
}

func (d *gatedHomeDispatcher) HeartbeatOK() bool {
	select {
	case <-d.loaded:
	default:
		close(d.loaded)
	}
	<-d.release
	return true
}

func (d *gatedHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.rpop.Add(1)
	return nil, errors.New("old Home dispatcher was used")
}

func (*gatedHomeDispatcher) AbortAmbiguousDispatch() {}

func TestManagerHomeDispatchBundleCompareAndClearDoesNotRemoveReplacement(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	first := manager.PublishHomeDispatch(&gatedHomeDispatcher{loaded: make(chan struct{}), release: make(chan struct{})}, executionregistry.New(), 1)
	second := manager.PublishHomeDispatch(&gatedHomeDispatcher{loaded: make(chan struct{}), release: make(chan struct{})}, executionregistry.New(), 2)

	if manager.ClearHomeDispatchBundle(first) {
		t.Fatal("ClearHomeDispatchBundle() cleared a replacement bundle")
	}
	if got := manager.HomeDispatchBundle(); got != second {
		t.Fatalf("HomeDispatchBundle() = %p, want %p", got, second)
	}
	if !manager.ClearHomeDispatchBundle(second) {
		t.Fatal("ClearHomeDispatchBundle() = false, want true")
	}
	if got := manager.HomeDispatchBundle(); got != nil {
		t.Fatalf("HomeDispatchBundle() = %p, want nil", got)
	}
}

func TestPickHomeDispatchSelectionDoesNotMixDetachedBundleWithReplacement(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	oldDispatcher := &gatedHomeDispatcher{loaded: make(chan struct{}), release: make(chan struct{})}
	oldRegistry := executionregistry.New()
	oldBundle := manager.PublishHomeDispatch(oldDispatcher, oldRegistry, 1)

	result := make(chan error, 1)
	go func() {
		_, errSelect := manager.pickHomeDispatchSelection(context.Background(), "gpt-5.4", cliproxyexecutor.Options{})
		result <- errSelect
	}()
	select {
	case <-oldDispatcher.loaded:
	case <-time.After(time.Second):
		t.Fatal("selection did not load the old dispatch bundle")
	}

	if !manager.ClearHomeDispatchBundle(oldBundle) {
		t.Fatal("ClearHomeDispatchBundle() = false, want true")
	}
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if errDrain := oldRegistry.Drain(drainCtx); errDrain != nil {
		t.Fatalf("old registry Drain() error = %v", errDrain)
	}
	manager.PublishHomeDispatch(&gatedHomeDispatcher{loaded: make(chan struct{}), release: make(chan struct{})}, executionregistry.New(), 2)
	close(oldDispatcher.release)

	select {
	case errSelect := <-result:
		var authErr *Error
		if !errors.As(errSelect, &authErr) || authErr.Code != "home_unavailable" {
			t.Fatalf("pickHomeDispatchSelection() error = %v, want home_unavailable", errSelect)
		}
	case <-time.After(time.Second):
		t.Fatal("selection did not resume after the old bundle was detached")
	}
	if got := oldDispatcher.rpop.Load(); got != 0 {
		t.Fatalf("old dispatcher RPopAuth() calls = %d, want 0", got)
	}
}
