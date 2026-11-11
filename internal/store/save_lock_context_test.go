package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestTokenStoresSaveHonorsContextWhileWaitingForLock(t *testing.T) {
	factories := []struct {
		name string
		new  func(string) (cliproxyauth.Store, sync.Locker)
	}{
		{"git", func(dir string) (cliproxyauth.Store, sync.Locker) {
			s := &GitTokenStore{baseDir: dir}
			return s, &s.mu
		}},
		{"object", func(dir string) (cliproxyauth.Store, sync.Locker) {
			s := &ObjectTokenStore{authDir: dir}
			return s, &s.mu
		}},
		{"postgres", func(dir string) (cliproxyauth.Store, sync.Locker) {
			s := &PostgresStore{authDir: dir}
			return s, &s.mu
		}},
	}
	for _, factory := range factories {
		t.Run(factory.name, func(t *testing.T) {
			for _, held := range []bool{false, true} {
				name := "already-canceled"
				if held {
					name = "deadline-with-held-lock"
				}
				t.Run(name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						dir := t.TempDir()
						s, lock := factory.new(dir)
						ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
						defer cancel()
						if held {
							lock.Lock()
							defer lock.Unlock()
						} else {
							cancel()
						}
						storageCalled := false
						auth := &cliproxyauth.Auth{
							ID:       "pending.json",
							FileName: "pending.json",
							Provider: "claude",
							Metadata: map[string]any{"type": "claude"},
							Storage: &callbackTokenStorage{save: func(string) error {
								storageCalled = true
								return errors.New("unexpected token write")
							}},
						}
						type result struct {
							path string
							err  error
						}
						done := make(chan result, 1)
						go func() {
							path, err := s.Save(ctx, auth)
							done <- result{path, err}
						}()
						<-ctx.Done()
						synctest.Wait()
						select {
						case got := <-done:
							if got.path != "" || !errors.Is(got.err, ctx.Err()) {
								t.Errorf("Save() = (%q, %v), want empty path and %v", got.path, got.err, ctx.Err())
							}
						default:
							t.Fatal("Save did not return after its context expired while the store lock was held")
						}
						if storageCalled {
							t.Error("canceled Save reached token storage")
						}
						if _, err := os.Stat(filepath.Join(dir, auth.FileName)); !errors.Is(err, os.ErrNotExist) {
							t.Errorf("canceled Save created a local auth file: %v", err)
						}
					})
				})
			}
		})
	}
}
