package management

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestOAuthCallbackLifecycleTerminalJoinsAdmittedWriter(t *testing.T) {
	for _, terminal := range []string{"cancel", "complete", "error", "expire"} {
		t.Run(terminal, func(t *testing.T) {
			store := newOAuthSessionStore(time.Minute)
			store.Register("synthetic-held", "anthropic")
			dir := t.TempDir()
			entered, release := make(chan struct{}), make(chan struct{})
			writerDone, terminalDone := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(func() {
				unblock()
				callbackLifecycleWait(t, writerDone)
				callbackLifecycleWait(t, terminalDone)
			})
			go func() {
				defer close(writerDone)
				_, err := store.publishCallback("synthetic-held", "anthropic", func() (string, error) {
					close(entered)
					<-release
					return writeOAuthCallbackFile(dir, "anthropic", "synthetic-held", "synthetic-code", "")
				})
				if err != nil && !errors.Is(err, errOAuthSessionNotPending) {
					t.Error("callback lifecycle: admitted writer failed unexpectedly")
				}
			}()
			callbackLifecycleWait(t, entered)
			go func() {
				defer close(terminalDone)
				switch terminal {
				case "cancel":
					if !store.Cancel("synthetic-held") {
						t.Error("callback lifecycle: cancellation rejected")
					}
				case "complete":
					store.Complete("synthetic-held")
				case "error":
					store.SetError("synthetic-held", "synthetic failure")
				case "expire":
					store.mu.Lock()
					session := store.sessions["synthetic-held"]
					session.ExpiresAt = time.Unix(1, 0)
					store.sessions["synthetic-held"] = session
					store.mu.Unlock()
					store.Get("synthetic-held")
				}
			}()
			callbackLifecycleAwaitState(t, store, "synthetic-held", false)
			select {
			case <-terminalDone:
				t.Error("callback lifecycle: terminal returned before admitted writer")
			default:
			}
			// The held file writer must not retain the store-wide lock.
			unrelatedDone := make(chan struct{})
			go func() {
				defer close(unrelatedDone)
				store.Register("synthetic-unrelated", "codex")
				_, err := store.publishCallback("synthetic-unrelated", "codex", func() (string, error) {
					return writeOAuthCallbackFile(dir, "codex", "synthetic-unrelated", "synthetic-other-code", "")
				})
				if err != nil {
					t.Error("callback lifecycle: unrelated publication blocked")
				}
			}()
			callbackLifecycleWait(t, unrelatedDone)
			unblock()
			callbackLifecycleWait(t, writerDone)
			callbackLifecycleWait(t, terminalDone)
			if _, err := os.Stat(filepath.Join(dir, ".oauth-anthropic-synthetic-held.oauth")); !errors.Is(err, os.ErrNotExist) {
				t.Error("callback lifecycle: terminal callback residue")
			}
			callbackLifecycleEntries(t, dir, 1)
		})
	}
}

func TestOAuthCallbackLifecycleReregisterJoinsOldGeneration(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("synthetic-generation", "codex")
	dir := t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	writerDone, terminalDone, registerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		callbackLifecycleWait(t, writerDone)
		callbackLifecycleWait(t, terminalDone)
		callbackLifecycleWait(t, registerDone)
	})
	go func() {
		defer close(writerDone)
		_, err := store.publishCallback("synthetic-generation", "codex", func() (string, error) {
			close(entered)
			<-release
			return writeOAuthCallbackFile(dir, "codex", "synthetic-generation", "synthetic-old-code", "")
		})
		if err != nil && !errors.Is(err, errOAuthSessionNotPending) {
			t.Error("callback lifecycle: admitted writer failed unexpectedly")
		}
	}()
	callbackLifecycleWait(t, entered)
	go func() {
		defer close(terminalDone)
		if !store.Cancel("synthetic-generation") {
			t.Error("callback lifecycle: cancellation rejected")
		}
	}()
	callbackLifecycleAwaitState(t, store, "synthetic-generation", false)
	go func() {
		defer close(registerDone)
		store.Register("synthetic-generation", "codex")
	}()
	// Registration exposes the new session before joining the old receipt. Its
	// gate must remain closed to fresh publication until old cleanup finishes.
	callbackLifecycleAwaitState(t, store, "synthetic-generation", true)
	select {
	case <-registerDone:
		t.Error("callback lifecycle: registration bypassed old writer")
	default:
	}
	unblock()
	callbackLifecycleWait(t, writerDone)
	callbackLifecycleWait(t, terminalDone)
	callbackLifecycleWait(t, registerDone)
	callbackLifecycleEntries(t, dir, 0)
	path, err := store.publishCallback("synthetic-generation", "codex", func() (string, error) {
		return writeOAuthCallbackFile(dir, "codex", "synthetic-generation", "synthetic-new-code", "")
	})
	if err != nil {
		t.Fatal("callback lifecycle: new generation publication failed")
	}
	if !bytes.Contains(callbackLifecycleRead(t, path), []byte("synthetic-new-code")) {
		t.Error("callback lifecycle: new generation callback changed")
	}
	callbackLifecycleEntries(t, dir, 1)
}

func TestOAuthCallbackLifecycleFailedRemovalCannotAuthorizeNextGeneration(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	store.Register("synthetic-removal", "codex")
	dir := t.TempDir()
	path, err := WriteOAuthCallbackFileForPendingSession(dir, "codex", "synthetic-removal", "synthetic-old-code", "")
	if err != nil {
		t.Fatal("callback lifecycle: initial write failed")
	}
	old := callbackLifecycleRead(t, path)
	if os.Remove(path) != nil || os.Mkdir(path, 0700) != nil || os.WriteFile(filepath.Join(path, "obstacle"), nil, 0600) != nil {
		t.Fatal("callback lifecycle: fixture setup failed")
	}
	if !store.Cancel("synthetic-removal") {
		t.Fatal("callback lifecycle: cancellation rejected")
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatal("callback lifecycle: failed removal was not exercised")
	}
	if os.RemoveAll(path) != nil || os.WriteFile(path, old, 0600) != nil {
		t.Fatal("callback lifecycle: fixture setup failed")
	}
	store.Register("synthetic-removal", "codex")
	if _, err := readOAuthCallbackFileForPendingSession(path, "codex", "synthetic-removal"); !errors.Is(err, os.ErrNotExist) {
		t.Error("callback lifecycle: old callback authorized new generation")
	}
	if _, err := WriteOAuthCallbackFileForPendingSession(dir, "codex", "synthetic-removal", "synthetic-new-code", ""); err != nil {
		t.Fatal("callback lifecycle: new generation publication failed")
	}
	data, err := readOAuthCallbackFileForPendingSession(path, "codex", "synthetic-removal")
	if err != nil || !bytes.Contains(data, []byte("synthetic-new-code")) {
		t.Error("callback lifecycle: new generation consumption failed")
	}
	callbackLifecycleEntries(t, dir, 0)
}

func TestOAuthCallbackLifecycleRegisteredLegacyWriterCanBeConsumed(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	store.Register("synthetic-legacy", "anthropic")
	dir := t.TempDir()
	path, err := WriteOAuthCallbackFile(dir, "claude", "synthetic-legacy", "synthetic-code", "")
	if err != nil {
		t.Fatal("callback lifecycle: registered legacy write failed")
	}
	before := callbackLifecycleRead(t, path)
	data, err := readOAuthCallbackFileForPendingSession(path, "anthropic", "synthetic-legacy")
	if err != nil || !bytes.Equal(data, before) {
		t.Error("callback lifecycle: registered legacy consumption failed")
	}
	if !store.IsPending("synthetic-legacy", "anthropic") {
		t.Error("callback lifecycle: receipt ended pending exchange")
	}
	if _, err := WriteOAuthCallbackFile(dir, "anthropic", "synthetic-legacy", "synthetic-late", ""); !errors.Is(err, errOAuthSessionNotPending) {
		t.Error("callback lifecycle: duplicate callback accepted")
	}
	callbackLifecycleEntries(t, dir, 0)
}

func TestOAuthCallbackLifecycleOldConsumerCannotStealNewCallback(t *testing.T) {
	for _, transition := range []string{"replacement", "cancel_then_register", "expiry_then_register"} {
		t.Run(transition, func(t *testing.T) {
			store := newOAuthSessionStore(time.Minute)
			old := store.RegisterWithProxy("synthetic-consumer", "codex", "")
			if transition == "cancel_then_register" && !store.Cancel("synthetic-consumer") {
				t.Fatal("callback lifecycle: cancellation rejected")
			}
			if transition == "expiry_then_register" {
				store.mu.Lock()
				session := store.sessions["synthetic-consumer"]
				session.ExpiresAt = time.Unix(1, 0)
				store.sessions["synthetic-consumer"] = session
				store.mu.Unlock()
			}
			fresh := store.RegisterWithProxy("synthetic-consumer", "codex", "")
			dir := t.TempDir()
			path, err := store.publishCallback("synthetic-consumer", "codex", func() (string, error) {
				return writeOAuthCallbackFile(dir, "codex", "synthetic-consumer", "synthetic-new-code", "")
			})
			if err != nil {
				t.Fatal("callback lifecycle: new generation publication failed")
			}
			before := callbackLifecycleRead(t, path)
			if _, err := old.consume(path); !errors.Is(err, errOAuthSessionNotPending) {
				t.Error("callback lifecycle: old consumer stole new callback")
			}
			if !bytes.Equal(before, callbackLifecycleRead(t, path)) {
				t.Error("callback lifecycle: old consumer changed new callback")
			}
			data, err := fresh.consume(path)
			if err != nil || !bytes.Equal(data, before) {
				t.Error("callback lifecycle: new generation consumption failed")
			}
			if !store.IsPending("synthetic-consumer", "codex") {
				t.Error("callback lifecycle: receipt ended pending exchange")
			}
			callbackLifecycleEntries(t, dir, 0)
		})
	}
}

func TestOAuthCallbackLifecycleRepeatedTerminalJoinsAdmittedWriter(t *testing.T) {
	for _, repeated := range []string{"get_completed", "complete", "cancel"} {
		t.Run(repeated, func(t *testing.T) {
			store := newOAuthSessionStore(time.Minute)
			store.Register("synthetic-repeat", "codex")
			joins := make(chan struct{}, 8)
			store.beforeCallbackCleanup = func() { joins <- struct{}{} }
			dir := t.TempDir()
			entered, release := make(chan struct{}), make(chan struct{})
			writerDone, terminalDone, repeatedDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(func() {
				unblock()
				callbackLifecycleWait(t, writerDone)
				callbackLifecycleWait(t, terminalDone)
				callbackLifecycleWait(t, repeatedDone)
			})
			go func() {
				defer close(writerDone)
				_, err := store.publishCallback("synthetic-repeat", "codex", func() (string, error) {
					close(entered)
					<-release
					return writeOAuthCallbackFile(dir, "codex", "synthetic-repeat", "synthetic-code", "")
				})
				if err != nil && !errors.Is(err, errOAuthSessionNotPending) {
					t.Error("callback lifecycle: admitted writer failed unexpectedly")
				}
			}()
			callbackLifecycleWait(t, entered)
			go func() {
				defer close(terminalDone)
				if repeated == "cancel" {
					if !store.Cancel("synthetic-repeat") {
						t.Error("callback lifecycle: cancellation rejected")
					}
				} else {
					store.Complete("synthetic-repeat")
				}
			}()
			callbackLifecycleWait(t, joins)
			callbackLifecycleAwaitState(t, store, "synthetic-repeat", false)
			go func() {
				defer close(repeatedDone)
				switch repeated {
				case "get_completed":
					session, ok := store.Get("synthetic-repeat")
					if !ok || !session.Completed {
						t.Error("callback lifecycle: completed status changed")
					}
				case "complete":
					store.Complete("synthetic-repeat")
				case "cancel":
					if store.Cancel("synthetic-repeat") {
						t.Error("callback lifecycle: repeated cancel changed outcome")
					}
				}
			}()
			select {
			case <-joins:
			case <-repeatedDone:
				t.Error("callback lifecycle: repeated terminal bypassed cleanup join")
				return
			case <-time.After(3 * time.Second):
				t.Fatal("callback lifecycle: repeated cleanup join missing")
			}
			select {
			case <-repeatedDone:
				t.Error("callback lifecycle: repeated terminal returned before writer")
			default:
			}
			unblock()
			callbackLifecycleWait(t, writerDone)
			callbackLifecycleWait(t, terminalDone)
			callbackLifecycleWait(t, repeatedDone)
			callbackLifecycleEntries(t, dir, 0)
		})
	}
}

func TestOAuthCallbackLifecycleStaleCommitCannotChangeNewSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	const state = "synthetic-stale-commit"
	old := store.RegisterWithProxy(state, "codex", "")
	fresh := store.RegisterWithProxy(state, "codex", "http://127.0.0.1:3128")
	expected, ok := store.Get(state)
	if !ok {
		t.Fatal("callback lifecycle: new session missing")
	}
	closed := fresh.closed.Load()
	unchanged := func() {
		t.Helper()
		current, exists := store.Get(state)
		if !exists || !reflect.DeepEqual(current, expected) || fresh.closed.Load() != closed {
			t.Fatal("callback lifecycle: stale commit changed new session")
		}
	}
	if store.claimCommit(state, "codex", old) {
		t.Fatal("callback lifecycle: stale commit was admitted")
	}
	unchanged()
	store.SetError(state, "synthetic old failure", old)
	unchanged()
	store.FailCommit(state, "synthetic old commit failure", old)
	unchanged()
	store.CompleteWithAuth(state, "synthetic-old-auth", old)
	unchanged()
	if !store.claimCommit(state, "codex", fresh) {
		t.Fatal("callback lifecycle: fresh commit was rejected")
	}
	expected, ok = store.Get(state)
	closed = fresh.closed.Load()
	if !ok || !expected.Committing || expected.Completed {
		t.Fatal("callback lifecycle: fresh commit state missing")
	}
	store.SetError(state, "synthetic late old failure", old)
	unchanged()
	store.FailCommit(state, "synthetic late old commit failure", old)
	unchanged()
	store.CompleteWithAuth(state, "synthetic-old-auth", old)
	unchanged()
	store.CompleteWithAuth(state, "synthetic-fresh-auth", fresh)
	completed, ok := store.Get(state)
	if !ok || !completed.Completed || completed.Committing || completed.Status != "" || completed.AuthID != "synthetic-fresh-auth" || !completed.Saved || !completed.Adopted {
		t.Fatal("callback lifecycle: fresh completion failed")
	}
}

func callbackLifecycleWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("callback lifecycle: ordering barrier missing")
	}
}

// The timer bounds observation; only the state under the store mutex establishes
// ordering. No elapsed duration is treated as proof that a goroutine advanced.
func callbackLifecycleAwaitState(t *testing.T, store *oauthSessionStore, state string, pending bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		store.mu.RLock()
		session, exists := store.sessions[state]
		matches := exists && !session.Completed && session.Status == ""
		store.mu.RUnlock()
		if matches == pending {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("callback lifecycle: terminal transition missing")
		case <-tick.C:
		}
	}
}
