package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAcquisitionRejectsSymlinkBeforeSerialization(t *testing.T) {
	for _, backend := range []string{"object", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "login.json")
			target := filepath.Join(dir, "target")
			original := []byte(`{"type":"claude","access_token":"original"}`)
			if err := os.WriteFile(target, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("target", path); err != nil {
				t.Fatal(err)
			}
			reached := false
			remote := func() error { reached = true; return context.DeadlineExceeded }
			var s cliproxyauth.Store
			if backend == "object" {
				s = &ObjectTokenStore{authDir: dir, putObjectFn: func(context.Context, string, []byte, string) error { return remote() }}
			} else {
				s = &PostgresStore{authDir: dir, persistAuthFn: func(context.Context, string, []byte) error { return remote() }}
			}
			record := &cliproxyauth.Auth{ID: "login.json", FileName: "login.json", Provider: "claude", Storage: &claudeauth.ClaudeTokenStorage{AccessToken: "replacement"}, Metadata: map[string]any{"type": "claude"}}
			saved, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), record)
			if reached || err == nil || saved != "" {
				t.Fatalf("unexpected remote outcome: reached=%v saved=%q err=%v", reached, saved, err)
			}
			link, err := os.Readlink(path)
			if err != nil || link != "target" {
				t.Fatalf("link changed: %q, %v", link, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(original) {
				t.Errorf("acquisition changed symlink-backed credentials: got %q, want %q, err=%v", got, original, err)
			}
		})
	}
}

func TestAcquisitionSnapshotWaitsForCodexLock(t *testing.T) {
	for _, backend := range []string{"object", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "login.json")
				if err := codexauth.WriteJSONFileAtomic(path, map[string]any{"type": "codex", "access_token": "original"}); err != nil {
					t.Fatal(err)
				}
				var s cliproxyauth.Store
				if backend == "object" {
					s = &ObjectTokenStore{authDir: dir, putObjectFn: func(context.Context, string, []byte, string) error { return context.DeadlineExceeded }}
				} else {
					s = &PostgresStore{authDir: dir, persistAuthFn: func(context.Context, string, []byte) error { return context.DeadlineExceeded }}
				}
				done := make(chan error, 1)
				var latest []byte
				err := codexauth.WithFileLock(path, func() error {
					go func() {
						_, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), &cliproxyauth.Auth{ID: "login.json", FileName: "login.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "failed-login"}})
						done <- err
					}()
					synctest.Wait()
					if err := codexauth.WriteJSONFileAtomic(path, map[string]any{"type": "codex", "access_token": "successful-refresh", "refresh_token": "rotated"}); err != nil {
						return err
					}
					var err error
					latest, err = os.ReadFile(path)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Save error = %v", err)
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != string(latest) {
					t.Errorf("failed acquisition rolled back successful locked update: got %q, want %q, err=%v", got, latest, err)
				}
			})
		})
	}
}

func TestAcquisitionRollbackPreservesLockedRefresh(t *testing.T) {
	for _, backend := range []string{"object", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "login.json")
			latest := []byte(`{"type":"codex","access_token":"successful-refresh","refresh_token":"rotated"}`)
			remote := func() error {
				if err := codexauth.WithFileLock(path, func() error { return os.WriteFile(path, latest, 0o600) }); err != nil {
					t.Fatal(err)
				}
				return context.DeadlineExceeded
			}
			var s cliproxyauth.Store
			if backend == "object" {
				s = &ObjectTokenStore{authDir: dir, putObjectFn: func(context.Context, string, []byte, string) error { return remote() }}
			} else {
				s = &PostgresStore{authDir: dir, persistAuthFn: func(context.Context, string, []byte) error { return remote() }}
			}
			_, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), &cliproxyauth.Auth{ID: "login.json", FileName: "login.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "failed-login"}})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Save error = %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(latest) {
				t.Fatalf("rollback overwrote a successful refresh: %q, %v", got, err)
			}
		})
	}
}

func TestAcquisitionStorageFailurePreservesMirror(t *testing.T) {
	for _, backend := range []string{"object", "postgres"} {
		for _, provider := range []string{"codex", "claude"} {
			for _, existing := range []bool{false, true} {
				name := "new"
				if existing {
					name = "replacement"
				}
				t.Run(backend+"/"+provider+"/"+name, func(t *testing.T) {
					dir := t.TempDir()
					path := filepath.Join(dir, "login.json")
					original := []byte(`{"type":"` + provider + `","access_token":"original","note":"keep"}`)
					if existing {
						if err := os.WriteFile(path, original, 0o640); err != nil {
							t.Fatal(err)
						}
					}
					reached := false
					remote := func(data []byte) error {
						reached = true
						if !bytes.Contains(data, []byte(`"replacement"`)) {
							t.Errorf("remote did not receive acquired credentials: %s", data)
						}
						return context.DeadlineExceeded
					}
					var s cliproxyauth.Store
					if backend == "object" {
						s = &ObjectTokenStore{authDir: dir, putObjectFn: func(_ context.Context, _ string, data []byte, _ string) error { return remote(data) }}
					} else {
						s = &PostgresStore{authDir: dir, persistAuthFn: func(_ context.Context, _ string, data []byte) error { return remote(data) }}
					}
					record := &cliproxyauth.Auth{ID: "login.json", FileName: "login.json", Provider: provider, Metadata: map[string]any{"type": provider}}
					if provider == "codex" {
						record.Storage = &codexauth.CodexTokenStorage{AccessToken: "replacement"}
					} else {
						record.Storage = &claudeauth.ClaudeTokenStorage{AccessToken: "replacement"}
					}
					saved, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), record)
					if !reached || saved != "" || !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("Save = %q, %v, remote reached=%v", saved, err, reached)
					}
					got, err := os.ReadFile(path)
					if existing {
						if err != nil || string(got) != string(original) {
							t.Fatalf("prior credential changed: %q, %v", got, err)
						}
					} else if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("failed new credential remains readable: %q, %v", got, err)
					}
					entries, err := os.ReadDir(dir)
					if err != nil {
						t.Fatal(err)
					}
					for _, entry := range entries {
						if entry.IsDir() {
							t.Fatalf("staging directory remains: %s", entry.Name())
						}
					}
				})
			}
		}
	}
}

func TestAcquisitionSerializationFailurePreservesMirror(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "login.json")
	original := []byte(`{"type":"claude","access_token":"original"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	errWrite := errors.New("serialization failed")
	s := &ObjectTokenStore{authDir: dir, putObjectFn: func(context.Context, string, []byte, string) error {
		t.Error("remote save after serialization failure")
		return nil
	}}
	record := &cliproxyauth.Auth{ID: "login.json", FileName: "login.json", Storage: &callbackTokenStorage{save: func(path string) error {
		if err := os.WriteFile(path, []byte("partial credential"), 0o600); err != nil {
			return err
		}
		return errWrite
	}}}
	_, err := s.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), record)
	if !errors.Is(err, errWrite) {
		t.Fatalf("Save error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("serialization failure changed prior credential: %q, %v", got, err)
	}
}
