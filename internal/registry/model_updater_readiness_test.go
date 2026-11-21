package registry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func useStartupTestUpdater(t *testing.T, updater *catalogUpdater) {
	t.Helper()
	oldGeneral, oldCodex, oldDevin := generalCatalogUpdater, codexCatalogUpdater, devinCatalogUpdater
	generalCatalogUpdater, codexCatalogUpdater, devinCatalogUpdater = updater, &catalogUpdater{}, &catalogUpdater{}
	t.Cleanup(func() {
		generalCatalogUpdater, codexCatalogUpdater, devinCatalogUpdater = oldGeneral, oldCodex, oldDevin
	})
}

func awaitStartupTest(t *testing.T, ctx context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := WaitForStartupModelRefresh(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForStartupModelRefreshEmbeddedOnly(t *testing.T) {
	useStartupTestUpdater(t, &catalogUpdater{})
	awaitStartupTest(t, context.Background())
}

func TestWaitForStartupModelRefreshCompletionAndCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	updater := &catalogUpdater{
		fetch: func(ctx context.Context, _ string) ([]byte, error) {
			close(entered)
			select {
			case <-release:
				return nil, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		publish: func([]byte) ([]string, error) { return nil, nil },
	}
	useStartupTestUpdater(t, updater)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updater.configure(ctx, embeddedCatalogSource)
	<-entered
	select {
	case <-updater.startupDone:
		t.Fatal("startup completed before the fetch")
	default:
	}
	canceled, cancelWait := context.WithCancel(ctx)
	cancelWait()
	if err := WaitForStartupModelRefresh(canceled); err != context.Canceled {
		t.Fatalf("canceled wait = %v", err)
	}
	close(release)
	awaitStartupTest(t, ctx)
}

func TestWaitForStartupModelRefreshIncludesRegistrationCallback(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	refreshCallbackMu.Lock()
	oldCallback, oldPending := refreshCallback, pendingRefreshChanges
	refreshCallback = func([]string) { close(entered); <-release }
	pendingRefreshChanges = nil
	refreshCallbackMu.Unlock()
	t.Cleanup(func() {
		refreshCallbackMu.Lock()
		refreshCallback, pendingRefreshChanges = oldCallback, oldPending
		refreshCallbackMu.Unlock()
	})
	updater := &catalogUpdater{
		fetch:   func(context.Context, string) ([]byte, error) { return nil, nil },
		publish: func([]byte) ([]string, error) { return []string{"codex"}, nil },
	}
	useStartupTestUpdater(t, updater)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updater.configure(ctx, embeddedCatalogSource)
	<-entered
	select {
	case <-updater.startupDone:
		t.Fatal("startup completed before the registration callback")
	default:
	}
	close(release)
	awaitStartupTest(t, ctx)
}

func TestWaitForStartupModelRefreshFailedFetchAndDisabled(t *testing.T) {
	for _, source := range []string{embeddedCatalogSource, "disabled"} {
		t.Run(source, func(t *testing.T) {
			updater := &catalogUpdater{
				fetch: func(context.Context, string) ([]byte, error) {
					return nil, errors.New("fetch failed")
				},
				publish: func([]byte) ([]string, error) {
					t.Error("failed or disabled fetch must not publish")
					return nil, nil
				},
			}
			useStartupTestUpdater(t, updater)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			updater.configure(ctx, source)
			awaitStartupTest(t, ctx)
		})
	}
}

func TestWaitForStartupModelRefreshFollowsSourceReload(t *testing.T) {
	oldEntered, newEntered := make(chan struct{}), make(chan struct{})
	oldRelease, newRelease := make(chan struct{}), make(chan struct{})
	updater := &catalogUpdater{
		fetch: func(_ context.Context, source string) ([]byte, error) {
			if source == "old" {
				close(oldEntered)
				<-oldRelease // Simulate a reader that finishes after cancellation.
			} else {
				close(newEntered)
				<-newRelease
			}
			return []byte(source), nil
		},
		publish: func(data []byte) ([]string, error) {
			if string(data) != "new" {
				t.Error("stale generation published")
			}
			return nil, nil
		},
	}
	useStartupTestUpdater(t, updater)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updater.configure(ctx, "old")
	<-oldEntered
	oldDone := updater.startupDone
	updater.configure(ctx, "new")
	<-newEntered
	close(oldRelease)
	<-oldDone
	select {
	case <-updater.startupDone:
		t.Fatal("old refresh completed the new generation")
	default:
	}
	close(newRelease)
	awaitStartupTest(t, ctx)
}

func TestWaitForStartupModelRefreshIncludesProviderCatalogs(t *testing.T) {
	for _, provider := range []string{"devin", "codex"} {
		t.Run(provider, func(t *testing.T) {
			testStartupProviderCatalog(t, provider)
		})
	}
}

func testStartupProviderCatalog(t *testing.T, provider string) {
	useStartupTestUpdater(t, &catalogUpdater{})
	fetchEntered, releaseFetch := make(chan struct{}), make(chan struct{})
	callbackEntered, releaseCallback := make(chan struct{}), make(chan struct{})
	refreshCallbackMu.Lock()
	oldCallback, oldPending := refreshCallback, pendingRefreshChanges
	refreshCallback = func([]string) { close(callbackEntered); <-releaseCallback }
	pendingRefreshChanges = nil
	refreshCallbackMu.Unlock()
	t.Cleanup(func() {
		refreshCallbackMu.Lock()
		refreshCallback, pendingRefreshChanges = oldCallback, oldPending
		refreshCallbackMu.Unlock()
	})
	updater := &catalogUpdater{
		fetch: func(ctx context.Context, _ string) ([]byte, error) {
			close(fetchEntered)
			select {
			case <-releaseFetch:
				return nil, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		publish: func([]byte) ([]string, error) { return []string{provider}, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if provider == "codex" {
		codexCatalogUpdater = updater
	} else {
		devinCatalogUpdater = updater
	}
	updater.configure(ctx, embeddedCatalogSource)
	<-fetchEntered
	assertWaiting := func(stage string) {
		t.Helper()
		waitCtx, cancelWait := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancelWait()
		if err := WaitForStartupModelRefresh(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("startup did not wait for %s %s: %v", provider, stage, err)
		}
	}
	assertWaiting("fetch")
	close(releaseFetch)
	<-callbackEntered
	assertWaiting("registration callback")
	close(releaseCallback)
	<-updater.startupDone
	awaitStartupTest(t, ctx)
}
