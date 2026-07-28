package executionregistry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDrainRejectsLateInstallAndCancelsBoundScopes(t *testing.T) {
	registry := New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, ScopeSpec{RequestID: "req-1", CredentialID: "cred-1", Model: "gpt", Kind: "http", StartedAt: time.Now()})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	closed := atomic.Int32{}
	if errBind := scope.Bind(func() error {
		closed.Add(1)
		go scope.End("canceled")
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if errDrain := registry.Drain(ctx); errDrain != nil {
		t.Fatal(errDrain)
	}
	if closed.Load() != 1 {
		t.Fatalf("close calls = %d", closed.Load())
	}
	if _, errLate := registry.BeginDispatch(); !errors.Is(errLate, ErrRegistryNotAccepting) {
		t.Fatalf("late dispatch error = %v", errLate)
	}
}

func TestScopeEndIsExactlyOnce(t *testing.T) {
	registry := New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	closed := atomic.Int32{}
	if errBind := scope.Bind(func() error {
		closed.Add(1)
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}

	done := make(chan struct{})
	go func() {
		scope.End("complete")
		close(done)
	}()
	scope.End("duplicate")
	<-done
	if closed.Load() != 1 {
		t.Fatalf("close calls = %d, want 1", closed.Load())
	}
}

func TestDrainWaitsForPendingDispatch(t *testing.T) {
	registry := New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- registry.Drain(ctx) }()

	select {
	case errDrain := <-done:
		t.Fatalf("Drain() returned before pending dispatch ended: %v", errDrain)
	case <-time.After(20 * time.Millisecond):
	}
	pending.End()
	if errDrain := <-done; errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

func TestDrainReturnsWhenBlockingResourceCloseExceedsContext(t *testing.T) {
	registry := New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if errBind := scope.Bind(func() error {
		close(started)
		<-release
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	errDrain := registry.Drain(ctx)
	if !errors.Is(errDrain, context.DeadlineExceeded) {
		t.Fatalf("Drain() error = %v, want context deadline exceeded", errDrain)
	}
	select {
	case <-started:
	default:
		t.Fatal("Drain() did not start closing the bound resource")
	}
	if state := State(registry.state.Load()); state != StateDraining {
		t.Fatalf("registry state = %v, want draining", state)
	}

	ended := make(chan struct{})
	go func() {
		scope.End("canceled")
		close(ended)
	}()
	close(release)
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("Scope.End() did not wait for resource close completion")
	}
	if errDrain = registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() after resource close = %v", errDrain)
	}
}

func TestDrainWaitsForBlockingResourceClose(t *testing.T) {
	registry := New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if errBind := scope.Bind(func() error {
		close(started)
		<-release
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- registry.Drain(ctx) }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Drain() did not close the bound resource")
	}
	go scope.End("canceled")
	select {
	case errDrain := <-done:
		t.Fatalf("Drain() returned before the resource close completed: %v", errDrain)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if errDrain := <-done; errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

func TestConcurrentDrainWaitsForBlockingResourceClose(t *testing.T) {
	registry := New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if errBind := scope.Bind(func() error {
		close(started)
		<-release
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	firstDrain := make(chan error, 1)
	go func() { firstDrain <- registry.Drain(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first Drain() did not close the bound resource")
	}
	ended := make(chan struct{})
	go func() {
		scope.End("canceled")
		close(ended)
	}()
	select {
	case <-ended:
		t.Fatal("Scope.End() returned before the resource close completed")
	case <-time.After(20 * time.Millisecond):
	}
	secondDrain := make(chan error, 1)
	go func() { secondDrain <- registry.Drain(ctx) }()
	select {
	case errDrain := <-secondDrain:
		t.Fatalf("second Drain() returned before resource close completed: %v", errDrain)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("Scope.End() did not complete after the resource close")
	}
	if errDrain := <-firstDrain; errDrain != nil {
		t.Fatalf("first Drain() error = %v", errDrain)
	}
	if errDrain := <-secondDrain; errDrain != nil {
		t.Fatalf("second Drain() error = %v", errDrain)
	}
}

func TestConcurrentCloseWaitsForBlockingResourceClose(t *testing.T) {
	registry := New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if errBind := scope.Bind(func() error {
		close(started)
		<-release
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}

	firstClose := make(chan error, 1)
	go func() { firstClose <- registry.Close() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first Close() did not close the bound resource")
	}

	secondClose := make(chan error, 1)
	go func() { secondClose <- registry.Close() }()
	select {
	case errClose := <-secondClose:
		t.Fatalf("second Close() returned before resource close completed: %v", errClose)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	if errClose := <-firstClose; errClose != nil {
		t.Fatalf("first Close() error = %v", errClose)
	}
	if errClose := <-secondClose; errClose != nil {
		t.Fatalf("second Close() error = %v", errClose)
	}
}

func TestDrainRejectsLateBind(t *testing.T) {
	registry := New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- registry.Drain(ctx) }()

	deadline := time.After(time.Second)
	for State(registry.state.Load()) == StateAccepting {
		select {
		case <-deadline:
			t.Fatal("registry did not begin draining")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if errBind := scope.Bind(func() error { return nil }); !errors.Is(errBind, ErrRegistryNotAccepting) {
		t.Fatalf("Bind() error = %v, want ErrRegistryNotAccepting", errBind)
	}
	scope.End("canceled")
	if errDrain := <-done; errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

func TestDrainRejectsLateInstall(t *testing.T) {
	registry := New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- registry.Drain(ctx) }()

	deadline := time.After(time.Second)
	for State(registry.state.Load()) == StateAccepting {
		select {
		case <-deadline:
			t.Fatal("registry did not begin draining")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if _, errInstall := registry.Install(pending, ScopeSpec{}); !errors.Is(errInstall, ErrRegistryNotAccepting) {
		t.Fatalf("Install() error = %v, want ErrRegistryNotAccepting", errInstall)
	}
	if errDrain := <-done; errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}
