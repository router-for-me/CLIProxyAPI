package management

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These tests use the existing callback/session APIs and synthetic values only.
// They never include callback contents, state, paths, or error text in failures.
func TestOAuthCallbackLifecycleDuplicateBeforeConsumption(t *testing.T) {
	for _, provider := range []struct {
		name, registered, callback string
		plugin                     bool
	}{
		{"claude_alias", "anthropic", "claude", false},
		{"codex_alias", "codex", "openai", false},
		{"plugin_without_alias", "openai", "openai", true},
	} {
		t.Run(provider.name, func(t *testing.T) {
			store := newOAuthSessionStore(time.Minute)
			replaceOAuthSessionStoreForTest(t, store)
			if provider.plugin {
				if store.RegisterPlugin("synthetic-state", provider.registered, nil) != nil {
					t.Fatal("callback lifecycle: registration failed")
				}
			} else {
				store.Register("synthetic-state", provider.registered)
			}
			dir := t.TempDir()
			path, err := WriteOAuthCallbackFileForPendingSession(dir, provider.callback, "synthetic-state", "synthetic-first", "")
			if err != nil {
				t.Fatal("callback lifecycle: initial write failed")
			}
			before := callbackLifecycleRead(t, path)
			_, err = WriteOAuthCallbackFileForPendingSession(dir, provider.callback, "synthetic-state", "synthetic-second", "")
			if !errors.Is(err, errOAuthSessionNotPending) {
				t.Error("callback lifecycle: duplicate callback accepted")
			}
			if !bytes.Equal(before, callbackLifecycleRead(t, path)) {
				t.Error("callback lifecycle: duplicate replaced first callback")
			}
			if !store.IsPending("synthetic-state", provider.registered) {
				t.Error("callback lifecycle: receipt ended pending exchange")
			}
			callbackLifecycleEntries(t, dir, 1)
		})
	}
}

func TestOAuthCallbackLifecycleTerminalCleanup(t *testing.T) {
	for _, terminal := range []string{"cancel", "complete", "error", "failed_commit", "failed_poll", "expired", "provider_complete"} {
		t.Run(terminal, func(t *testing.T) {
			store := newOAuthSessionStore(time.Minute)
			replaceOAuthSessionStoreForTest(t, store)
			store.Register("synthetic-terminal", "anthropic")
			dir := t.TempDir()
			if _, err := WriteOAuthCallbackFileForPendingSession(dir, "anthropic", "synthetic-terminal", "synthetic-code", ""); err != nil {
				t.Fatal("callback lifecycle: initial write failed")
			}
			switch terminal {
			case "cancel":
				if !CancelOAuthSession("synthetic-terminal") {
					t.Fatal("callback lifecycle: cancellation rejected")
				}
			case "complete":
				CompleteOAuthSession("synthetic-terminal")
			case "error":
				SetOAuthSessionError("synthetic-terminal", "synthetic failure")
			case "failed_commit":
				if !store.claimCommit("synthetic-terminal", "anthropic") {
					t.Fatal("callback lifecycle: commit admission failed")
				}
				FailOAuthSessionCommit("synthetic-terminal", "synthetic failure")
			case "failed_poll":
				store.FailPendingPoll("synthetic-terminal", "synthetic failure")
			case "expired":
				store.mu.Lock()
				session := store.sessions["synthetic-terminal"]
				session.ExpiresAt = time.Unix(1, 0)
				store.sessions["synthetic-terminal"] = session
				store.mu.Unlock()
				store.Get("synthetic-terminal")
			case "provider_complete":
				if CompleteOAuthSessionsByProvider("anthropic") != 1 {
					t.Fatal("callback lifecycle: provider completion failed")
				}
			}
			callbackLifecycleEntries(t, dir, 0)
			if _, err := WriteOAuthCallbackFileForPendingSession(dir, "anthropic", "synthetic-terminal", "synthetic-late", ""); !errors.Is(err, errOAuthSessionNotPending) {
				t.Error("callback lifecycle: terminal callback accepted")
			}
			callbackLifecycleEntries(t, dir, 0)
		})
	}
}

func TestOAuthCallbackLifecycleWriteFailureCanRetry(t *testing.T) {
	for _, failure := range []string{"directory", "publish"} {
		t.Run(failure, func(t *testing.T) {
			store := newOAuthSessionStore(time.Minute)
			replaceOAuthSessionStoreForTest(t, store)
			store.Register("synthetic-retry", "codex")
			root := t.TempDir()
			dir := filepath.Join(root, "auth")
			obstacle := dir
			if failure == "directory" {
				if os.WriteFile(obstacle, nil, 0600) != nil {
					t.Fatal("callback lifecycle: fixture setup failed")
				}
			} else {
				obstacle = filepath.Join(dir, ".oauth-codex-synthetic-retry.oauth")
				if os.MkdirAll(obstacle, 0700) != nil {
					t.Fatal("callback lifecycle: fixture setup failed")
				}
			}
			if _, err := WriteOAuthCallbackFileForPendingSession(dir, "codex", "synthetic-retry", "synthetic-first", ""); err == nil {
				t.Fatal("callback lifecycle: filesystem failure was not exercised")
			}
			if !store.IsPending("synthetic-retry", "codex") || os.Remove(obstacle) != nil {
				t.Fatal("callback lifecycle: failed write ended retry eligibility")
			}
			if _, err := WriteOAuthCallbackFileForPendingSession(dir, "codex", "synthetic-retry", "synthetic-retry-code", ""); err != nil {
				t.Fatal("callback lifecycle: retry write failed")
			}
			callbackLifecycleEntries(t, dir, 1)
		})
	}
}

func TestOAuthCallbackLifecycleIsolation(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	dir := t.TempDir()
	states := []struct {
		state, provider string
		plugin          bool
	}{
		{"synthetic-target", "anthropic", false},
		{"synthetic-sibling", "anthropic", false},
		{"synthetic-other", "codex", false},
		{"synthetic-plugin", "anthropic", true},
	}
	paths := make([]string, len(states))
	payloads := make([][]byte, len(states))
	for i, item := range states {
		if item.plugin {
			if store.RegisterPlugin(item.state, item.provider, nil) != nil {
				t.Fatal("callback lifecycle: registration failed")
			}
		} else {
			store.Register(item.state, item.provider)
		}
		path, err := WriteOAuthCallbackFileForPendingSession(dir, item.provider, item.state, "synthetic-code", "")
		if err != nil {
			t.Fatal("callback lifecycle: initial write failed")
		}
		paths[i], payloads[i] = path, callbackLifecycleRead(t, path)
	}
	if _, err := WriteOAuthCallbackFileForPendingSession(dir, "codex", states[0].state, "synthetic-wrong-provider", ""); !errors.Is(err, errOAuthSessionNotPending) {
		t.Error("callback lifecycle: wrong provider accepted")
	}
	if !CancelOAuthSession(states[0].state) {
		t.Fatal("callback lifecycle: cancellation rejected")
	}
	if _, err := os.Stat(paths[0]); !errors.Is(err, os.ErrNotExist) {
		t.Error("callback lifecycle: terminal callback residue")
	}
	for i := 1; i < len(states); i++ {
		if !store.IsPending(states[i].state, states[i].provider) || !bytes.Equal(payloads[i], callbackLifecycleRead(t, paths[i])) {
			t.Error("callback lifecycle: unrelated session changed")
		}
	}
	if CompleteOAuthSessionsByProvider("anthropic") != 1 {
		t.Error("callback lifecycle: provider completion scope changed")
	}
	if _, err := os.Stat(paths[1]); !errors.Is(err, os.ErrNotExist) {
		t.Error("callback lifecycle: terminal callback residue")
	}
	for i := 2; i < len(states); i++ {
		if !store.IsPending(states[i].state, states[i].provider) || !bytes.Equal(payloads[i], callbackLifecycleRead(t, paths[i])) {
			t.Error("callback lifecycle: unrelated session changed")
		}
	}
}

func callbackLifecycleRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("callback lifecycle: callback missing")
	}
	return data
}

func callbackLifecycleEntries(t *testing.T, dir string, count int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != count {
		t.Error("callback lifecycle: unexpected callback residue")
	}
}
