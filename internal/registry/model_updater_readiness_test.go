package registry

import (
	"context"
	"testing"
)

func TestWaitForStartupModelRefreshEmbeddedOnly(t *testing.T) {
	oldStarted := updaterStarted.Load()
	updaterStarted.Store(false)
	t.Cleanup(func() { updaterStarted.Store(oldStarted) })
	if err := WaitForStartupModelRefresh(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForStartupModelRefreshCompletionAndCancellation(t *testing.T) {
	oldStarted, oldDone := updaterStarted.Load(), startupRefreshDone
	updaterStarted.Store(true)
	startupRefreshDone = make(chan struct{})
	t.Cleanup(func() { updaterStarted.Store(oldStarted); startupRefreshDone = oldDone })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WaitForStartupModelRefresh(ctx); err != context.Canceled {
		t.Fatalf("canceled wait = %v", err)
	}
	close(startupRefreshDone)
	if err := WaitForStartupModelRefresh(context.Background()); err != nil {
		t.Fatalf("completed wait = %v", err)
	}
}
