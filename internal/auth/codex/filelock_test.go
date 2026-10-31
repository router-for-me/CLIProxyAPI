package codex

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestWithFileLockContextDeadline(t *testing.T) {
	for _, phase := range []string{"process", "file"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "codex.json")
				var release func()
				if phase == "process" {
					mu, releaseEntry := lockForFilePath(path)
					defer releaseEntry()
					mu.Lock()
					release = mu.Unlock
				} else {
					var err error
					release, err = acquireInterProcessLockFile(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				defer func() {
					if release != nil {
						release()
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				done := make(chan error, 1)
				called := false
				go func() { done <- WithFileLockContext(ctx, path, func() error { called = true; return nil }) }()
				synctest.Wait()
				<-ctx.Done()
				if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("error=%v; want deadline exceeded", err)
				}
				if called {
					t.Fatal("canceled lock acquisition ran the write")
				}
				release()
				release = nil
				sentinel := errors.New("callback result")
				if err := WithFileLock(path, func() error { return sentinel }); !errors.Is(err, sentinel) {
					t.Fatalf("subsequent lock failed: %v", err)
				}
				if called {
					t.Fatal("timed out callback ran after release")
				}
			})
		})
	}
}

func TestWithFileLockContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WithFileLockContext(ctx, filepath.Join(t.TempDir(), "codex.json"), func() error { t.Error("canceled callback ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v; want canceled", err)
	}
}

func TestWithFileLockReleasesOneUsePaths(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		path := filepath.Join(dir, fmt.Sprintf("staged-%d", i))
		if err := WithFileLockContext(context.Background(), path, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	retained := 0
	codexFileLocks.Range(func(key, _ any) bool {
		if strings.HasPrefix(key.(string), canonicalLockPath(dir)+string(filepath.Separator)) {
			retained++
		}
		return true
	})
	if retained != 0 {
		t.Fatalf("completed one-use paths retained %d locks", retained)
	}
}

func TestWithFileLockCanceledWaiterKeepsActiveEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credential")
		mutex, releaseEntry := lockForFilePath(path)
		mutex.Lock()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- WithFileLockContext(ctx, path, func() error { t.Error("canceled waiter ran"); return nil })
		}()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("wait error = %v", err)
		}
		next, releaseNext := lockForFilePath(path)
		if next != mutex {
			t.Error("canceling a waiter replaced an active mutex")
		}
		releaseNext()
		mutex.Unlock()
		releaseEntry()
		if _, retained := codexFileLocks.Load(canonicalLockPath(path)); retained {
			t.Error("last user left a cached mutex")
		}
	})
}
