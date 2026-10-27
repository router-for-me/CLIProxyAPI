package test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxypool"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	integrationProvider = "claude"
	integrationModel    = ""
)

// resetPriorityExecutor is a do-nothing ProviderExecutor: these tests exercise
// credential selection and proxy assembly, never upstream execution.
type resetPriorityExecutor struct{ provider string }

func (e resetPriorityExecutor) Identifier() string { return e.provider }

func (e resetPriorityExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e resetPriorityExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("streaming is not used by integration tests")
}

func (e resetPriorityExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e resetPriorityExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e resetPriorityExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("http requests are not used by integration tests")
}

// Scenario A: config selects reset-priority, three accounts share a provider and
// model, and selection always prefers the smallest time-to-reset. Cooldowns push
// the next-best account ahead.
func TestResetPriorityRoutingSelectsNearestQuotaReset(t *testing.T) {
	cfg, errParse := config.ParseConfigBytes([]byte(`
routing:
  strategy: "reset-priority"
  reset-priority-urgent-seconds: 900
  reset-priority-jitter-seconds: 60
  reset-priority-fallback: "round-robin"
`))
	if errParse != nil {
		t.Fatalf("parse reset-priority config: %v", errParse)
	}
	if cfg.Routing.Strategy != "reset-priority" {
		t.Fatalf("routing.strategy = %q, want reset-priority", cfg.Routing.Strategy)
	}
	if cfg.Routing.ResetPriorityUrgentSeconds != 900 || cfg.Routing.ResetPriorityJitterSeconds != 60 {
		t.Fatalf("urgent/jitter = %d/%d, want 900/60",
			cfg.Routing.ResetPriorityUrgentSeconds, cfg.Routing.ResetPriorityJitterSeconds)
	}
	if cfg.Routing.ResetPriorityFallback != "round-robin" {
		t.Fatalf("fallback = %q, want round-robin", cfg.Routing.ResetPriorityFallback)
	}

	selector := resetPrioritySelectorFromConfig(t, cfg)
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(resetPriorityExecutor{provider: integrationProvider})

	now := time.Now()
	accounts := []struct {
		id    string
		delta time.Duration
	}{
		{id: "nearest", delta: 5 * time.Minute},
		{id: "second", delta: 10 * time.Minute},
		{id: "normal", delta: 20 * time.Minute},
	}
	for _, account := range accounts {
		auth := &coreauth.Auth{
			ID:       account.id,
			Provider: integrationProvider,
			Metadata: map[string]any{"type": integrationProvider},
			Quota: coreauth.QuotaState{
				Signals: map[string]string{
					"anthropic-ratelimit-unified-reset": strconv.FormatInt(now.Add(account.delta).Unix(), 10),
				},
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", account.id, errRegister)
		}
	}

	selected, errSelect := manager.SelectAuth(context.Background(), integrationProvider, integrationModel, cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("first SelectAuth: %v", errSelect)
	}
	if selected == nil || selected.ID != "nearest" {
		t.Fatalf("first selection = %v, want nearest", authID(selected))
	}

	coolDown(t, manager, "nearest")

	selected, errSelect = manager.SelectAuth(context.Background(), integrationProvider, integrationModel, cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("second SelectAuth: %v", errSelect)
	}
	if selected == nil || selected.ID != "second" {
		t.Fatalf("second selection = %v, want second", authID(selected))
	}

	// With both urgent accounts cooling down, selection must fall through to the
	// normal-tier fallback account instead of failing.
	coolDown(t, manager, "second")

	selected, errSelect = manager.SelectAuth(context.Background(), integrationProvider, integrationModel, cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("third SelectAuth: %v", errSelect)
	}
	if selected == nil || selected.ID != "normal" {
		t.Fatalf("third selection = %v, want normal fallback", authID(selected))
	}
}

func resetPrioritySelectorFromConfig(t *testing.T, cfg *config.Config) coreauth.Selector {
	t.Helper()

	if cfg == nil || cfg.Routing.Strategy != "reset-priority" {
		t.Fatalf("config routing strategy is not reset-priority")
	}
	var fallback coreauth.Selector = &coreauth.WeightedRoundRobinSelector{}
	if strings.EqualFold(strings.TrimSpace(cfg.Routing.ResetPriorityFallback), "round-robin") {
		fallback = &coreauth.RoundRobinSelector{}
	}
	return coreauth.NewResetPrioritySelector(
		fallback,
		time.Duration(cfg.Routing.ResetPriorityUrgentSeconds)*time.Second,
		time.Duration(cfg.Routing.ResetPriorityJitterSeconds)*time.Second,
	)
}

func coolDown(t *testing.T, manager *coreauth.Manager, authID string) {
	t.Helper()

	updated, committed, errModify := manager.Modify(context.Background(), authID, 0, func(auth *coreauth.Auth) error {
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "credential_quota"
		auth.Quota.NextRecoverAt = time.Now().Add(time.Hour)
		return nil
	})
	if errModify != nil {
		t.Fatalf("cooldown %s: %v", authID, errModify)
	}
	if !committed || updated == nil {
		t.Fatalf("cooldown %s was not committed", authID)
	}
}

func authID(auth *coreauth.Auth) string {
	if auth == nil {
		return "<nil>"
	}
	return auth.ID
}

// Scenario B: proxy pool config and two auth files are loaded through the SDK
// service wiring. An explicit account proxy must survive untouched, while a
// proxy_pool_id account must resolve to a concrete node and persist that binding.
func TestProxyPoolBindingDuringAuthLoad(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
		t.Fatalf("create auth dir: %v", errMkdir)
	}

	const (
		explicitProxy = "socks5://explicit.example.com:1080"
		poolID        = "pool-us-res"
		nodeA         = "http://127.0.0.1:18080"
		nodeB         = "http://127.0.0.1:18081"
	)
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := `
auth-dir: "` + filepath.ToSlash(authDir) + `"
proxy-pools:
  - id: "` + poolID + `"
    strategy: "sticky"
    nodes:
      - "` + nodeA + `"
      - "` + nodeB + `"
`
	if errWrite := os.WriteFile(configPath, []byte(configYAML), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	cfg, errParse := config.ParseConfigBytes([]byte(configYAML))
	if errParse != nil {
		t.Fatalf("parse proxy pool config: %v", errParse)
	}
	if len(cfg.ProxyPools) != 1 || cfg.ProxyPools[0].ID != poolID {
		t.Fatalf("parsed proxy pools = %#v, want one %q", cfg.ProxyPools, poolID)
	}

	writeAuthFile(t, filepath.Join(authDir, "explicit.json"), map[string]any{
		"type":      integrationProvider,
		"email":     "explicit@example.com",
		"proxy_url": explicitProxy,
	})
	writeAuthFile(t, filepath.Join(authDir, "pooled.json"), map[string]any{
		"type":          integrationProvider,
		"email":         "pooled@example.com",
		"proxy_pool_id": poolID,
	})

	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)

	// Build the real SDK service so config normalization and manager wiring run,
	// then install the same pool resolver Service.applyProxyPools installs. Running
	// Service.Run is intentionally avoided: it starts network-facing background
	// updaters that these offline integration tests must not contact.
	if _, errBuild := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithCoreAuthManager(manager).
		Build(); errBuild != nil {
		t.Fatalf("build service: %v", errBuild)
	}

	poolConfigs, errConvert := proxypool.ConfigsFromConfig(cfg.ProxyPools)
	if errConvert != nil {
		t.Fatalf("convert proxy pools: %v", errConvert)
	}
	poolManager, errPool := proxypool.NewManager(poolConfigs)
	if errPool != nil {
		t.Fatalf("build proxy pool manager: %v", errPool)
	}
	t.Cleanup(poolManager.Close)
	manager.SetProxyPoolResolver(testPoolResolver{manager: poolManager})

	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatalf("load auths: %v", errLoad)
	}

	loaded := authsByID(manager.List())
	if len(loaded) != 2 {
		t.Fatalf("loaded %d auths, want 2", len(loaded))
	}

	explicit := loaded["explicit.json"]
	if explicit == nil {
		t.Fatal("explicit auth was not loaded")
	}
	if explicit.ProxyURL != explicitProxy {
		t.Fatalf("explicit ProxyURL = %q, want %q", explicit.ProxyURL, explicitProxy)
	}
	if node := coreauth.ProxyPoolNode(explicit); node != "" {
		t.Fatalf("explicit ProxyPoolNode = %q, want empty", node)
	}

	pooled := loaded["pooled.json"]
	if pooled == nil {
		t.Fatal("pooled auth was not loaded")
	}
	if got := coreauth.ProxyPoolID(pooled); got != poolID {
		t.Fatalf("pooled ProxyPoolID = %q, want %q", got, poolID)
	}
	node := pooled.ProxyURL
	if node != nodeA && node != nodeB {
		t.Fatalf("pooled ProxyURL = %q, want one of %q/%q", node, nodeA, nodeB)
	}
	if got := coreauth.ProxyPoolNode(pooled); got != node {
		t.Fatalf("pooled ProxyPoolNode = %q, want assigned node %q", got, node)
	}

	// Sticky resolution is deterministic: reloading the same store yields the same node.
	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatalf("reload auths: %v", errLoad)
	}
	reloaded := authsByID(manager.List())["pooled.json"]
	if reloaded == nil || reloaded.ProxyURL != node {
		t.Fatalf("sticky node after reload = %q, want %q", proxyOf(reloaded), node)
	}

	// Registration (the watcher path used by service startup) persists the binding.
	if _, errRegister := manager.Register(context.Background(), reloaded.Clone()); errRegister != nil {
		t.Fatalf("register pooled auth: %v", errRegister)
	}
	raw, errRead := os.ReadFile(filepath.Join(authDir, "pooled.json"))
	if errRead != nil {
		t.Fatalf("read persisted auth: %v", errRead)
	}
	var persisted map[string]any
	if errJSON := json.Unmarshal(raw, &persisted); errJSON != nil {
		t.Fatalf("decode persisted auth: %v", errJSON)
	}
	if got, _ := persisted["proxy_pool_node"].(string); got != node {
		t.Fatalf("persisted proxy_pool_node = %q, want %q", got, node)
	}
	if got, _ := persisted["proxy_pool_id"].(string); got != poolID {
		t.Fatalf("persisted proxy_pool_id = %q, want %q", got, poolID)
	}
}

// testPoolResolver mirrors Service.ResolveAuthProxy/ResolvePoolNode so the auth
// assembly path can be exercised without starting the service HTTP server.
type testPoolResolver struct {
	manager *proxypool.Manager
}

func (r testPoolResolver) ResolveAuthProxy(_ context.Context, auth *coreauth.Auth) (string, string, error) {
	poolID := coreauth.ProxyPoolID(auth)
	if poolID == "" {
		return "", "", nil
	}
	node, errResolve := r.manager.ResolveNode(auth.Provider, auth.ID, poolID, coreauth.ProxyPoolNode(auth))
	if errResolve != nil {
		return "", "", errResolve
	}
	return node, node, nil
}

func (r testPoolResolver) ResolvePoolNode(_ context.Context, provider, authID, poolID string) (string, string, error) {
	node, errResolve := r.manager.Resolve(provider, authID, poolID)
	if errResolve != nil {
		return "", "", errResolve
	}
	return node, node, nil
}

func writeAuthFile(t *testing.T, path string, payload map[string]any) {
	t.Helper()

	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("marshal auth file: %v", errMarshal)
	}
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}
}

func authsByID(auths []*coreauth.Auth) map[string]*coreauth.Auth {
	out := make(map[string]*coreauth.Auth, len(auths))
	for _, auth := range auths {
		if auth != nil {
			out[auth.ID] = auth
		}
	}
	return out
}

func proxyOf(auth *coreauth.Auth) string {
	if auth == nil {
		return "<nil>"
	}
	return auth.ProxyURL
}

// recordingLauncher is a fake browser.Launcher that records every call.
type recordingLauncher struct {
	mu    sync.Mutex
	calls []browser.LaunchOptions
	err   error
}

func (l *recordingLauncher) Launch(_ context.Context, opts browser.LaunchOptions) error {
	l.mu.Lock()
	l.calls = append(l.calls, opts)
	l.mu.Unlock()
	return l.err
}

func (l *recordingLauncher) recorded() []browser.LaunchOptions {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]browser.LaunchOptions, len(l.calls))
	copy(out, l.calls)
	return out
}

// Scenario C: the installed launcher owns OpenURL while it succeeds, and a
// launcher failure falls back to the system browser. The system browser is
// isolated with a PATH that contains no browser command so the test never opens
// a real window.
func TestKunKunLauncherHookAndCallbackPrefix(t *testing.T) {
	t.Cleanup(browser.ClearLauncher)
	browser.ClearLauncher()

	t.Run("launcher handles the OAuth URL", func(t *testing.T) {
		restorePath := isolateSystemBrowser(t)
		defer restorePath()

		launcher := &recordingLauncher{}
		browser.SetLauncher(launcher)

		ctx := browser.WithOAuthProxyURL(context.Background(), "http://user:pass@127.0.0.1:18080")
		ctx = browser.WithOAuthProvider(ctx, integrationProvider)
		authURL := "https://auth.example.com/oauth?redirect_uri=http%3A%2F%2F127.0.0.1%3A54545%2Fcallback&state=1"
		if errOpen := browser.OpenURLWithContext(ctx, authURL); errOpen != nil {
			t.Fatalf("OpenURLWithContext: %v", errOpen)
		}

		calls := launcher.recorded()
		if len(calls) != 1 {
			t.Fatalf("launcher calls = %d, want 1", len(calls))
		}
		if calls[0].AuthURL != authURL {
			t.Fatalf("AuthURL = %q, want %q", calls[0].AuthURL, authURL)
		}
		if calls[0].ProxyURL != "http://user:pass@127.0.0.1:18080" {
			t.Fatalf("ProxyURL = %q, want the session proxy", calls[0].ProxyURL)
		}
		if !strings.HasPrefix(calls[0].ProfileID, integrationProvider+"-") {
			t.Fatalf("ProfileID = %q, want a %s- prefix", calls[0].ProfileID, integrationProvider)
		}
	})

	t.Run("launcher failure falls back to the system browser", func(t *testing.T) {
		restorePath := isolateSystemBrowser(t)
		defer restorePath()

		launcher := &recordingLauncher{err: errors.New("kunkun unavailable")}
		browser.SetLauncher(launcher)

		errOpen := browser.OpenURLWithContext(context.Background(), "https://auth.example.com/oauth")
		if errOpen == nil {
			t.Fatal("OpenURLWithContext = nil, want the isolated system-browser failure")
		}
		// The launcher error is logged and replaced by the system-browser error, so
		// seeing anything but "kunkun unavailable" proves the fallback ran.
		if strings.Contains(errOpen.Error(), "kunkun unavailable") {
			t.Fatalf("OpenURLWithContext returned the launcher error without falling back: %v", errOpen)
		}
		if calls := launcher.recorded(); len(calls) != 1 {
			t.Fatalf("launcher calls = %d, want 1 before fallback", len(calls))
		}
	})

	t.Run("driver derives callback prefix from redirect_uri", func(t *testing.T) {
		if _, errLook := exec.LookPath("sh"); errLook != nil {
			t.Skip("sh is not available on this platform")
		}

		projectRoot := t.TempDir()
		profilesDir := t.TempDir()
		script := "printf '%s\\n' \"$@\" > \"$0.args\"\necho \"OAUTH_STATUS:completed\"\n"
		if errWrite := os.WriteFile(filepath.Join(projectRoot, "main.py"), []byte(script), 0o600); errWrite != nil {
			t.Fatalf("write fake KunKun main.py: %v", errWrite)
		}

		driver := browser.NewKunKunDriver(config.KunKunBrowserConfig{
			ProjectRoot:          projectRoot,
			PythonBin:            "sh",
			ProfilesDir:          profilesDir,
			LaunchTimeoutSeconds: 10,
		})
		authURL := "https://auth.example.com/oauth?redirect_uri=http%3A%2F%2Flocalhost%3A54545%2Fcallback&state=abc"
		errLaunch := driver.Launch(context.Background(), browser.LaunchOptions{
			AuthURL:   authURL,
			ProxyURL:  "http://user:pass@127.0.0.1:18080",
			ProfileID: "claude-integration",
		})
		if errLaunch != nil {
			t.Fatalf("Launch: %v", errLaunch)
		}

		rawArgs, errRead := os.ReadFile(filepath.Join(projectRoot, "main.py.args"))
		if errRead != nil {
			t.Fatalf("read recorded driver args: %v", errRead)
		}
		args := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
		if got := flagValue(args, "--callback-prefix"); got != "http://localhost:54545/callback" {
			t.Fatalf("--callback-prefix = %q, want derived redirect_uri", got)
		}
		if got := flagValue(args, "--url"); got != authURL {
			t.Fatalf("--url = %q, want %q", got, authURL)
		}
		if got := flagValue(args, "--profile-id"); got != "claude-integration" {
			t.Fatalf("--profile-id = %q, want claude-integration", got)
		}
		if got := flagValue(args, "--proxy"); got != "http://user:pass@127.0.0.1:18080" {
			t.Fatalf("--proxy = %q, want the account proxy", got)
		}

		profileDir := filepath.Join(profilesDir, "claude-integration")
		if info, errStat := os.Stat(profileDir); errStat != nil || !info.IsDir() {
			t.Fatalf("profile directory %q was not created: %v", profileDir, errStat)
		}
	})
}

// isolateSystemBrowser points PATH at an empty directory so OpenURL's fallback
// cannot launch a real browser, and returns a function that restores PATH.
func isolateSystemBrowser(t *testing.T) func() {
	t.Helper()

	previous := os.Getenv("PATH")
	if errSet := os.Setenv("PATH", t.TempDir()); errSet != nil {
		t.Fatalf("set PATH: %v", errSet)
	}
	return func() {
		if errSet := os.Setenv("PATH", previous); errSet != nil {
			t.Errorf("restore PATH: %v", errSet)
		}
	}
}

func flagValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}
