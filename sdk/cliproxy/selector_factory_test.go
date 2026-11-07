package cliproxy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// This probe policy changes preference so cached affinity is distinguishable
// from a fresh policy decision, regardless of upstream candidate sorting.
type accountPolicy struct{ preferA atomic.Bool }

func (p *accountPolicy) Pick(_ context.Context, _, _ string, _ executor.Options, accounts []*coreauth.Auth) (*coreauth.Auth, error) {
	preferred := "b"
	if p.preferA.Load() {
		preferred = "a"
	}
	for _, account := range accounts {
		if account.ID == preferred {
			return account, nil
		}
	}
	return nil, errors.New("preferred account unavailable")
}

func TestSelectorFactoryPreservesRoutingThroughReload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Background metadata checks terminate at this local proxy. No live account
	// credentials are loaded and no inference request is sent.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer proxy.Close()
	dir := t.TempDir()
	catalog := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catalog, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Host: "127.0.0.1", Port: 0, AuthDir: filepath.Join(dir, "auth")}
	cfg.ProxyURL = proxy.URL
	cfg.RemoteManagement.DisableControlPanel = true
	cfg.Models = config.ModelCatalogs{Catalog: catalog, CodexCatalog: catalog, DevinCatalog: catalog}
	cfg.Routing.SessionAffinity = true
	var failFactory atomic.Bool
	policy := &accountPolicy{}
	custom := coreauth.NewSessionAffinitySelector(policy)
	defer custom.Stop()
	manager := coreauth.NewManager(nil, custom, nil)
	reloads := make(chan func(*config.Config), 1)
	started := make(chan bool, 1)
	service, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(cfgPath).
		WithCoreAuthManager(manager).
		WithSelectorFactory(func(c *config.Config) (coreauth.Selector, error) {
			if failFactory.Load() {
				return nil, errors.New("policy unavailable")
			}
			return cliproxy.NewRoutingSelector(c, policy), nil
		}).
		WithHooks(cliproxy.Hooks{OnAfterStart: func(_ *cliproxy.Service) {
			started <- manager.Selector() != custom
		}}).
		WithWatcherFactory(func(_, _ string, reload func(*config.Config)) (*cliproxy.WatcherWrapper, error) {
			reloads <- reload
			// The public wrapper cannot wrap the real watcher: its fields are
			// private. Capture the real reload callback for a deterministic probe.
			return &cliproxy.WatcherWrapper{}, nil
		}).Build()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("service shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("service did not stop")
		}
	})
	select {
	case kept := <-started:
		if !kept {
			t.Fatal("factory selector was not installed at startup")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("service did not start")
	}
	var reload func(*config.Config)
	select {
	case reload = <-reloads:
	case <-time.After(10 * time.Second):
		t.Fatal("service did not initialize its reload callback")
	}
	accounts := []*coreauth.Auth{{ID: "a"}, {ID: "b"}}
	opts := executor.Options{Headers: http.Header{"Session_id": {"probe-session"}}}
	pick := func() string {
		t.Helper()
		selected, err := manager.Selector().Pick(ctx, "codex", "probe-model", opts, accounts)
		if err != nil || selected == nil {
			t.Fatalf("selector pick: %v", err)
		}
		return selected.ID
	}
	if got := pick(); got != "b" {
		t.Fatalf("startup policy selected %q, expected b", got)
	}
	policy.preferA.Store(true)
	bound, err := manager.Selector().Pick(ctx, "codex", "probe-model", opts, accounts)
	if err != nil || bound == nil || bound.ID != "b" {
		t.Fatalf("same session must retain b after policy preference changes: auth=%v err=%v", bound, err)
	}
	fresh := executor.Options{Headers: http.Header{"Session_id": {"fresh-probe-session"}}}
	cold, err := manager.Selector().Pick(ctx, "codex", "probe-model", fresh, accounts)
	if err != nil || cold == nil || cold.ID != "a" {
		t.Fatalf("new session must use custom policy after preference changes: auth=%v err=%v", cold, err)
	}
	t.Log("STARTUP: custom policy selected b; same session retained b after preference changes; new session selected a")

	// An unrelated reload must retain the binding even when the policy changes.
	policy.preferA.Store(true)
	updated := cfg.CloneForRuntime()
	updated.RequestRetry = 1
	reload(updated)
	if got := pick(); got != "b" {
		t.Fatalf("unrelated reload lost session pin: got %q", got)
	}
	// A factory failure must preserve the active selector and allow retry.
	failFactory.Store(true)
	failedUpdate := updated.CloneForRuntime()
	failedUpdate.Routing.Strategy = "fill-first"
	prior := manager.Selector()
	reload(failedUpdate)
	if manager.Selector() != prior {
		t.Fatal("factory failure replaced active selector")
	}
	if got := pick(); got != "b" {
		t.Fatalf("factory failure lost session pin: got %q", got)
	}
	failFactory.Store(false)
	// Routing changes rebuild via the factory and use its policy, not stock fill-first.
	policy.preferA.Store(false)
	updated.Routing.Strategy = "fill-first"
	reload(updated)
	if got := pick(); got != "b" {
		t.Fatalf("routing reload lost custom policy: got %q", got)
	}
}

func TestSelectorFactoryRejectsInvalidResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		factory cliproxy.SelectorFactory
	}{
		{"error", func(*config.Config) (coreauth.Selector, error) { return nil, errors.New("unavailable") }},
		{"nil", func(*config.Config) (coreauth.Selector, error) { return nil, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cliproxy.NewBuilder().WithConfig(&config.Config{}).WithConfigPath("unused.yaml").WithSelectorFactory(tc.factory).Build()
			if err == nil {
				t.Fatal("invalid factory result accepted")
			}
		})
	}
}
