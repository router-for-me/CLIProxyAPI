package management

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

const callbackLifecycleExchangeError = "synthetic callback lifecycle exchange finished"

type callbackLifecycleCodex struct {
	entered, release chan struct{}
	calls            atomic.Int32
	invalid          atomic.Bool
}

func (*callbackLifecycleCodex) GenerateAuthURL(state string, _ *codex.PKCECodes) (string, error) {
	return "https://example.invalid/authorize?state=" + state, nil
}

func (s *callbackLifecycleCodex) ExchangeCodeForTokens(_ context.Context, code string, _ *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	if s.calls.Add(1) != 1 || code != "synthetic-original" {
		s.invalid.Store(true)
	} else {
		close(s.entered)
	}
	<-s.release
	return nil, errors.New(callbackLifecycleExchangeError)
}

func (s *callbackLifecycleCodex) CreateTokenStorage(*codex.CodexAuthBundle) *codex.CodexTokenStorage {
	s.invalid.Store(true)
	return nil
}

type callbackLifecycleTerminalLog struct {
	done chan struct{}
	once sync.Once
}

func (*callbackLifecycleTerminalLog) Levels() []log.Level { return []log.Level{log.ErrorLevel} }
func (h *callbackLifecycleTerminalLog) Fire(entry *log.Entry) error {
	if strings.HasPrefix(entry.Message, "Failed to exchange authorization code for tokens:") && strings.Contains(entry.Message, callbackLifecycleExchangeError) {
		h.once.Do(func() { close(h.done) })
	}
	return nil
}

func TestOAuthCallbackLifecycleDuplicateDuringActualExchange(t *testing.T) {
	callbackLifecycleCodexWorker(t, false)
}

func TestOAuthCallbackLifecycleOldExchangeCannotFailNewSession(t *testing.T) {
	callbackLifecycleCodexWorker(t, true)
}

func callbackLifecycleCodexWorker(t *testing.T, replaceDuringExchange bool) {
	t.Helper()
	if os.Getenv("OAUTH_CALLBACK_LIFECYCLE_WORKER_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.timeout=10s", "-test.v")
		command.Env = append(os.Environ(), "OAUTH_CALLBACK_LIFECYCLE_WORKER_CHILD=1")
		output, err := command.CombinedOutput()
		if err != nil {
			for _, diagnostic := range []string{
				"callback lifecycle: duplicate callback accepted",
				"callback lifecycle: unexpected callback residue",
				"callback lifecycle: stock worker terminal effect missing",
				"callback lifecycle: exchange outcome changed",
				"callback lifecycle: old exchange changed new session",
				"callback lifecycle: old exchange changed new callback",
			} {
				if strings.Contains(string(output), diagnostic) {
					t.Error(diagnostic)
				}
			}
			if strings.Contains(string(output), "DATA RACE") {
				t.Error("callback lifecycle: race detected")
			}
			t.Fatal("callback lifecycle: worker subprocess failed")
		}
		return
	}
	// Globals and any exceptional unfinished stock waiter are confined to this
	// single-test process. No raw child output crosses the parent boundary.
	store := newOAuthSessionStore(time.Minute)
	oauthSessions = store
	upstream := &callbackLifecycleCodex{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(upstream.release) }) }
	newCodexOAuthService = func(*config.Config) codexOAuthService { return upstream }
	terminal := &callbackLifecycleTerminalLog{done: make(chan struct{})}
	// The hook is inert after its one signal and remains installed. It signals
	// the last stock error log, after SetOAuthSessionError and before return;
	// it is a state-effect barrier, not a claim to join the worker goroutine.
	log.AddHook(terminal)
	t.Cleanup(func() {
		unblock()
		select {
		case <-terminal.done:
		case <-time.After(3 * time.Second):
			t.Error("callback lifecycle: stock worker terminal effect missing")
		}
	})
	dir := t.TempDir()
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, nil)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodGet, "/v8/management/oauth/auth-url", nil)
	handler.RequestCodexToken(c)
	state := ojson.Get(response.Body.String(), "state").String()
	if response.Code != http.StatusOK || state == "" {
		t.Fatal("callback lifecycle: actual worker start failed")
	}
	path, err := WriteOAuthCallbackFileForPendingSession(dir, "codex", state, "synthetic-original", "")
	if err != nil {
		t.Fatal("callback lifecycle: initial write failed")
	}
	select {
	case <-upstream.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("callback lifecycle: exchange barrier missing")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("callback lifecycle: actual worker did not consume callback")
	}
	var fresh []byte
	if replaceDuringExchange {
		store.Register(state, "codex")
		if _, err := WriteOAuthCallbackFileForPendingSession(dir, "codex", state, "synthetic-fresh-generation", ""); err != nil {
			t.Fatal("callback lifecycle: new generation publication failed")
		}
		fresh = callbackLifecycleRead(t, path)
	} else {
		if _, err := WriteOAuthCallbackFileForPendingSession(dir, "codex", state, "synthetic-duplicate", ""); !errors.Is(err, errOAuthSessionNotPending) {
			t.Error("callback lifecycle: duplicate callback accepted")
		}
		callbackLifecycleEntries(t, dir, 0)
	}
	if !store.IsPending(state, "codex") {
		t.Error("callback lifecycle: receipt ended pending exchange")
	}
	unblock()
	select {
	case <-terminal.done:
	case <-time.After(3 * time.Second):
		t.Fatal("callback lifecycle: stock worker terminal effect missing")
	}
	if upstream.calls.Load() != 1 || upstream.invalid.Load() {
		t.Error("callback lifecycle: exchange outcome changed")
	}
	if replaceDuringExchange {
		if !store.IsPending(state, "codex") {
			t.Error("callback lifecycle: old exchange changed new session")
		}
		if !bytes.Equal(fresh, callbackLifecycleRead(t, path)) {
			t.Error("callback lifecycle: old exchange changed new callback")
		}
		data, err := readOAuthCallbackFileForPendingSession(path, "codex", state)
		if err != nil || !bytes.Equal(data, fresh) {
			t.Error("callback lifecycle: new generation consumption failed")
		}
	} else if store.IsPending(state, "codex") {
		t.Error("callback lifecycle: exchange outcome changed")
	}
	callbackLifecycleEntries(t, dir, 0)
}
