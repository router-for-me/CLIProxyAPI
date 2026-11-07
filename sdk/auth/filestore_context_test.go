package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestFileTokenStoreSaveHonorsContextWhileWaitingForLock(t *testing.T) {
	for _, held := range []bool{false, true} {
		name := "already-canceled"
		if held {
			name = "deadline-with-held-lock"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dir := t.TempDir()
				s := &FileTokenStore{baseDir: dir}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				if held {
					s.mu.Lock()
					defer s.mu.Unlock()
				} else {
					cancel()
				}
				auth := &cliproxyauth.Auth{
					ID:       "pending.json",
					FileName: "pending.json",
					Provider: "claude",
					Metadata: map[string]any{"type": "claude"},
				}
				done := make(chan error, 1)
				go func() {
					_, err := s.Save(ctx, auth)
					done <- err
				}()
				<-ctx.Done()
				synctest.Wait()
				select {
				case err := <-done:
					if !errors.Is(err, ctx.Err()) {
						t.Errorf("Save() error = %v, want %v", err, ctx.Err())
					}
				default:
					t.Fatal("Save did not return after its context expired while the store lock was held")
				}
				if _, err := os.Stat(filepath.Join(dir, auth.FileName)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("canceled Save created a local auth file: %v", err)
				}
			})
		})
	}
}
