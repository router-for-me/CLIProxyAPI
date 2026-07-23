package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
)

func TestHomeDispatchSelectionReleasesAttemptCancelTokensWithoutGrowingResources(t *testing.T) {
	registry := executionregistry.New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, executionregistry.ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	selection, errSelection := newHomeDispatchSelection(&Auth{ID: "home-auth"}, nil, "home", scope)
	if errSelection != nil {
		t.Fatal(errSelection)
	}

	for range 100 {
		_, release, errAttempt := selection.AttemptContext(context.Background())
		if errAttempt != nil {
			t.Fatalf("AttemptContext() error = %v", errAttempt)
		}
		release()
	}

	selection.resources.mu.Lock()
	resourceCount := len(selection.resources.closers)
	selection.resources.mu.Unlock()
	if resourceCount != 1 {
		t.Fatalf("bound resources = %d, want 1 attempt cancel registry", resourceCount)
	}
	if got := selection.attemptCancels.Len(); got != 0 {
		t.Fatalf("active attempt cancel tokens = %d, want 0", got)
	}

	selection.End("completed")
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if errDrain := registry.Drain(drainCtx); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

func TestAttemptCancelReleaseAfterCloseCancelsOnce(t *testing.T) {
	cancels := &attemptCancels{}
	var cancelCalls atomic.Int32
	release, errAdd := cancels.Add(func() { cancelCalls.Add(1) })
	if errAdd != nil {
		t.Fatalf("Add() error = %v", errAdd)
	}
	if errClose := cancels.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	release()
	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("cancel calls = %d, want 1", got)
	}
}

func TestHomeDispatchSelectionAttemptReleaseRacesDrainExactlyOnce(t *testing.T) {
	registry := executionregistry.New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, executionregistry.ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	selection, errSelection := newHomeDispatchSelection(&Auth{ID: "home-auth"}, nil, "home", scope)
	if errSelection != nil {
		t.Fatal(errSelection)
	}

	_, release, errAttempt := selection.AttemptContext(context.Background())
	if errAttempt != nil {
		t.Fatal(errAttempt)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		release()
	}()
	go func() {
		defer wg.Done()
		selection.End("draining")
	}()
	wg.Wait()

	if got := selection.attemptCancels.Len(); got != 0 {
		t.Fatalf("active attempt cancel tokens = %d, want 0", got)
	}
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}
