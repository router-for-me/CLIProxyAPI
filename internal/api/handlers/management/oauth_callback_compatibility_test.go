package management

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOAuthCallbackReceiptEquivalentPaths(t *testing.T) {
	for _, direction := range []string{"relative_writer", "relative_consumer", "exact_path"} {
		t.Run(direction, func(t *testing.T) {
			t.Chdir(t.TempDir())
			store := newOAuthSessionStore(time.Minute)
			replaceOAuthSessionStoreForTest(t, store)
			store.Register("synthetic-path", "codex")
			absoluteDir, err := filepath.Abs("callbacks")
			if err != nil {
				t.Fatal("callback compatibility: absolute directory failed")
			}
			dir := absoluteDir
			if direction == "relative_writer" {
				dir = "callbacks"
			}
			path, err := WriteOAuthCallbackFileForPendingSession(dir, "codex", "synthetic-path", "synthetic-code", "")
			if err != nil {
				t.Fatal("callback compatibility: initial publication failed")
			}
			before := callbackLifecycleRead(t, path)
			other := filepath.Join(t.TempDir(), filepath.Base(path))
			if os.WriteFile(other, before, 0600) != nil {
				t.Fatal("callback compatibility: unrelated file setup failed")
			}
			if _, err := readOAuthCallbackFileForPendingSession(other, "codex", "synthetic-path"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("callback compatibility: unrelated path accepted")
			}
			if !bytes.Equal(before, callbackLifecycleRead(t, other)) || !bytes.Equal(before, callbackLifecycleRead(t, path)) {
				t.Fatal("callback compatibility: rejected path changed a file")
			}
			consumePath := path
			if direction == "relative_writer" {
				consumePath = filepath.Join(absoluteDir, filepath.Base(path))
			} else if direction == "relative_consumer" {
				consumePath = filepath.Join("callbacks", filepath.Base(path))
			}
			data, err := readOAuthCallbackFileForPendingSession(consumePath, "codex", "synthetic-path")
			if err != nil || !bytes.Equal(data, before) {
				t.Fatal("callback compatibility: equivalent path was not consumed")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("callback compatibility: consumed file remains")
			}
			if _, err := readOAuthCallbackFileForPendingSession(path, "codex", "synthetic-path"); !errors.Is(err, errOAuthSessionNotPending) {
				t.Fatal("callback compatibility: duplicate consumption accepted")
			}
			if !bytes.Equal(before, callbackLifecycleRead(t, other)) {
				t.Fatal("callback compatibility: consumption removed unrelated file")
			}
		})
	}
}

func TestOAuthCallbackLegacyRegisteredProviderNames(t *testing.T) {
	for _, tc := range []struct {
		name, registered, writer, filename string
		plugin                             bool
	}{
		{"builtin_openai", "openai", "openai", "codex", false},
		{"builtin_openai_canonical_writer", "openai", "codex", "codex", false},
		{"builtin_claude", "claude", "claude", "anthropic", false},
		{"builtin_claude_canonical_writer", "claude", "anthropic", "anthropic", false},
		{"builtin_codex_alias_writer", "codex", "openai", "codex", false},
		{"builtin_anthropic_alias_writer", "anthropic", "claude", "anthropic", false},
		{"plugin_openai", "openai", "openai", "openai", true},
		{"plugin_claude", "claude", "claude", "claude", true},
		{"plugin_custom", "custom", "custom", "custom", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newOAuthSessionStore(time.Minute)
			replaceOAuthSessionStoreForTest(t, store)
			if tc.plugin {
				if store.RegisterPlugin("synthetic-provider", tc.registered, nil) != nil {
					t.Fatal("callback compatibility: plugin registration failed")
				}
			} else {
				store.Register("synthetic-provider", tc.registered)
			}
			dir := t.TempDir()
			path, err := WriteOAuthCallbackFile(dir, tc.writer, "synthetic-provider", "synthetic-code", "")
			if err != nil || filepath.Base(path) != ".oauth-"+tc.filename+"-synthetic-provider.oauth" {
				t.Fatal("callback compatibility: registered provider publication failed")
			}
			before := callbackLifecycleRead(t, path)
			if _, err := WriteOAuthCallbackFile(dir, tc.writer, "synthetic-provider", "synthetic-replay", ""); !errors.Is(err, errOAuthSessionNotPending) {
				t.Fatal("callback compatibility: duplicate publication accepted")
			}
			data, err := readOAuthCallbackFileForPendingSession(path, tc.registered, "synthetic-provider")
			if err != nil || !bytes.Equal(data, before) || !store.IsPending("synthetic-provider", tc.registered) {
				t.Fatal("callback compatibility: registered provider consumption failed")
			}
			store.Complete("synthetic-provider")
			if _, err := WriteOAuthCallbackFile(dir, tc.writer, "synthetic-provider", "synthetic-late", ""); !errors.Is(err, errOAuthSessionNotPending) {
				t.Fatal("callback compatibility: terminal publication accepted")
			}
			callbackLifecycleEntries(t, dir, 0)
		})
	}
}

func TestOAuthCallbackLegacyProviderMismatchRejected(t *testing.T) {
	for _, tc := range []struct {
		name, registered, writer string
		plugin                   bool
	}{
		{"builtin_cross_provider", "openai", "claude", false},
		{"plugin_openai_is_not_codex", "openai", "codex", true},
		{"plugin_claude_is_not_anthropic", "claude", "anthropic", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newOAuthSessionStore(time.Minute)
			replaceOAuthSessionStoreForTest(t, store)
			if tc.plugin {
				if store.RegisterPlugin("synthetic-mismatch", tc.registered, nil) != nil {
					t.Fatal("callback compatibility: plugin registration failed")
				}
			} else {
				store.Register("synthetic-mismatch", tc.registered)
			}
			dir := t.TempDir()
			if _, err := WriteOAuthCallbackFile(dir, tc.writer, "synthetic-mismatch", "synthetic-code", ""); !errors.Is(err, errOAuthSessionNotPending) {
				t.Fatal("callback compatibility: mismatched provider accepted")
			}
			callbackLifecycleEntries(t, dir, 0)
			if !store.IsPending("synthetic-mismatch", tc.registered) {
				t.Fatal("callback compatibility: rejection ended pending exchange")
			}
		})
	}
}

func TestOAuthCallbackLegacyProviderSourceReplacementRejected(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	prior := store.RegisterWithProxy("synthetic-source", "openai", "")
	if !store.Cancel("synthetic-source") || store.RegisterPlugin("synthetic-source", "openai", nil) != nil {
		t.Fatal("callback compatibility: source replacement failed")
	}
	dir := t.TempDir()
	published := false
	_, err := store.publishCallback("synthetic-source", "openai", func() (string, error) {
		published = true
		return writeOAuthCallbackFile(dir, "codex", "synthetic-source", "synthetic-old-code", "")
	}, prior)
	if !errors.Is(err, errOAuthSessionNotPending) || published {
		t.Fatal("callback compatibility: stale source semantics reached replacement")
	}
	callbackLifecycleEntries(t, dir, 0)
	path, err := WriteOAuthCallbackFile(dir, "openai", "synthetic-source", "synthetic-current-code", "")
	if err != nil || filepath.Base(path) != ".oauth-openai-synthetic-source.oauth" {
		t.Fatal("callback compatibility: replacement plugin callback failed")
	}
	store.Complete("synthetic-source")
	callbackLifecycleEntries(t, dir, 0)
}
