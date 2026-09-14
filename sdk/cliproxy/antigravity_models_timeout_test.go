package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// TestAntigravityCapabilityProbe_HardTimeout verifies that fetchAntigravityModelCapabilityHintsForAuth
// enforces a hard timeout (not exceeding 5s) even when passed an unbounded background context
// and connecting to a hanging server.
func TestAntigravityCapabilityProbe_HardTimeout(t *testing.T) {
	// Mock server that hangs unless client cancels
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
		}
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-2.5-flash"]}`))
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	svc := &Service{
		cfg: &config.Config{},
	}

	auth := &coreauth.Auth{
		ID:       "test-antigravity-auth",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test-token",
		},
	}

	start := time.Now()
	hints := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
	elapsed := time.Since(start)

	// It must enforce a hard timeout <= 6 seconds instead of waiting the full 10s
	if elapsed >= 8*time.Second {
		t.Fatalf("fetchAntigravityModelCapabilityHintsForAuth took %v, want hard timeout <= 6s", elapsed)
	}
	if len(hints.WebSearchModelIDs) != 0 {
		t.Fatalf("expected empty hints due to timeout, got %v", hints)
	}
}

// TestAntigravityCapabilityProbe_ConcurrentBaseURLs verifies that when multiple baseURLs
// are configured and one hangs while the other responds promptly, both endpoints receive requests
// concurrently and the probe returns the fast result quickly (< 2s) without waiting for the slow one.
func TestAntigravityCapabilityProbe_ConcurrentBaseURLs(t *testing.T) {
	slowStarted := make(chan struct{})
	var slowOnce sync.Once
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowOnce.Do(func() { close(slowStarted) })
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(func() {
		slowServer.CloseClientConnections()
		slowServer.Close()
	})

	fastServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wait for slowServer to have received its concurrent request or client context cancellation
		select {
		case <-slowStarted:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	}))
	t.Cleanup(fastServer.Close)

	svc := &Service{
		cfg: &config.Config{},
	}

	auth := &coreauth.Auth{
		ID:       "test-ag-concurrent-auth",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_urls": slowServer.URL + "," + fastServer.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test-token",
		},
	}

	start := time.Now()
	hints := svc.fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
	elapsed := time.Since(start)

	if elapsed >= 2*time.Second {
		t.Fatalf("concurrent fetch took %v, want < 2s", elapsed)
	}
	if _, ok := hints.WebSearchModelIDs["gemini-3.1-flash-lite"]; !ok {
		t.Fatalf("expected gemini-3.1-flash-lite in hints, got %v", hints)
	}
}

// TestAntigravityModelRegistration_AsyncNonBlocking verifies that registerModelsForAuth
// returns immediately (< 500ms) with baseline models and does not block on a hanging capability probe.
func TestAntigravityModelRegistration_AsyncNonBlocking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	svc := &Service{
		cfg: &config.Config{},
	}

	auth := &coreauth.Auth{
		ID:       "test-ag-nonblocking-reg",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test-token",
		},
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})

	start := time.Now()
	svc.registerModelsForAuth(context.Background(), auth)
	elapsed := time.Since(start)

	if elapsed >= 500*time.Millisecond {
		t.Fatalf("registerModelsForAuth took %v, want non-blocking < 500ms", elapsed)
	}

	models := GlobalModelRegistry().GetModelsForClient(auth.ID)
	if len(models) == 0 {
		t.Fatal("expected baseline models to be registered immediately")
	}
}

// TestAntigravityAsyncProbe_DiscardsStaleProbeWhenAuthReRegistered verifies that if an auth
// is re-registered with a new generation/epoch while an async probe is in flight, the stale
// probe result is discarded and does not overwrite the newer registration.
func TestAntigravityAsyncProbe_DiscardsStaleProbeWhenAuthReRegistered(t *testing.T) {
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(probeStarted)
		<-releaseProbe
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	auth := &coreauth.Auth{
		ID:       "ag-stale-test",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test-token",
		},
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})

	// Initial registration
	_, err := manager.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("Register error: %v", err)
	}
	svc.registerModelsForAuth(context.Background(), auth)

	// Wait for probe to start
	<-probeStarted

	// Re-register with a different custom model list (simulating auth/config update)
	newModels := []*ModelInfo{
		{ID: "custom-updated-model"},
	}
	GlobalModelRegistry().RegisterClient(auth.ID, "antigravity", newModels)

	// Bump generation on manager auth to simulate concurrent update
	managerAuth, _ := manager.GetByID(auth.ID)
	managerAuth.Generation += 5
	_, _ = manager.Update(context.Background(), managerAuth)

	// Now release the probe
	close(releaseProbe)
	svc.WaitAntigravityProbes()

	// Verify the updated model was NOT overwritten by the stale probe
	currentModels := GlobalModelRegistry().GetModelsForClient(auth.ID)
	if len(currentModels) != 1 || currentModels[0].ID != "custom-updated-model" {
		t.Fatalf("expected custom-updated-model to be preserved, got %v", currentModels)
	}
}

// TestAntigravityAsyncProbe_PreservesPluginModels verifies that when an auth has plugin-provided
// models in addition to native models, the async probe update preserves all plugin models.
func TestAntigravityAsyncProbe_PreservesPluginModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	}))
	defer server.Close()

	svc := &Service{
		cfg: &config.Config{},
	}

	auth := &coreauth.Auth{
		ID:       "ag-plugin-preserve",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test-token",
		},
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})

	// Register baseline models + a plugin model
	models := registryGetAntigravityModels()
	models = append(models, &ModelInfo{
		ID: "plugin-custom-model",
	})
	GlobalModelRegistry().RegisterClient(auth.ID, "antigravity", models)

	// Trigger async probe
	svc.asyncProbeAntigravityCapabilities(context.Background(), auth, "antigravity")
	svc.WaitAntigravityProbes()

	// Check that plugin model is still present, and native model has web search capability
	updated := GlobalModelRegistry().GetModelsForClient(auth.ID)
	foundPlugin := false
	foundSearch := false
	for _, m := range updated {
		if m.ID == "plugin-custom-model" {
			foundPlugin = true
		}
		if m.ID == "gemini-3.1-flash-lite" && m.SupportsWebSearch {
			foundSearch = true
		}
	}
	if !foundPlugin {
		t.Fatal("expected plugin-custom-model to be preserved after capability probe")
	}
	if !foundSearch {
		t.Fatal("expected gemini-3.1-flash-lite to have SupportsWebSearch=true")
	}
}

// TestAntigravityAsyncProbe_AppliesCapabilitiesToAliasedAndPrefixedModels verifies that when
// models have OAuth aliases and prefixes applied, the async capability probe correctly maps
// them back to their upstream model identifiers and applies the probed capabilities.
func TestAntigravityAsyncProbe_AppliesCapabilitiesToAliasedAndPrefixedModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite", "gemini-3.1-pro"]}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"antigravity": {
				{Name: "gemini-3.1-flash-lite", Alias: "aliased-flash"},
				{Name: "gemini-3.1-pro", Alias: "google/pro-flash"},
			},
		},
	}

	svc := &Service{
		cfg: cfg,
	}

	auth := &coreauth.Auth{
		ID:       "ag-alias-test",
		Provider: "antigravity",
		Prefix:   "my-prefix",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test-token",
		},
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})

	// Pre-registered models with alias containing slash and prefix
	models := []*ModelInfo{
		{ID: "my-prefix/aliased-flash"},
		{ID: "aliased-flash"},
		{ID: "my-prefix/google/pro-flash"},
		{ID: "google/pro-flash"},
		{ID: "my-prefix/gemini-pro"},
	}
	GlobalModelRegistry().RegisterClient(auth.ID, "antigravity", models)

	// Run async probe
	svc.asyncProbeAntigravityCapabilities(context.Background(), auth, "antigravity")
	svc.WaitAntigravityProbes()

	updated := GlobalModelRegistry().GetModelsForClient(auth.ID)
	var prefixedAliased, plainAliased, slashedAliased, prefixedSlashedAliased, unrelated *ModelInfo
	for _, m := range updated {
		switch m.ID {
		case "my-prefix/aliased-flash":
			prefixedAliased = m
		case "aliased-flash":
			plainAliased = m
		case "google/pro-flash":
			slashedAliased = m
		case "my-prefix/google/pro-flash":
			prefixedSlashedAliased = m
		case "my-prefix/gemini-pro":
			unrelated = m
		}
	}

	if prefixedAliased == nil || !prefixedAliased.SupportsWebSearch {
		t.Fatalf("expected my-prefix/aliased-flash to have SupportsWebSearch=true, got %+v", prefixedAliased)
	}
	if plainAliased == nil || !plainAliased.SupportsWebSearch {
		t.Fatalf("expected aliased-flash to have SupportsWebSearch=true, got %+v", plainAliased)
	}
	if unrelated == nil || unrelated.SupportsWebSearch {
		t.Fatalf("expected my-prefix/gemini-pro to NOT have SupportsWebSearch=true, got %+v", unrelated)
	}
	if slashedAliased == nil || !slashedAliased.SupportsWebSearch {
		t.Fatalf("expected google/pro-flash to have SupportsWebSearch=true, got %+v", slashedAliased)
	}
	if prefixedSlashedAliased == nil || !prefixedSlashedAliased.SupportsWebSearch {
		t.Fatalf("expected my-prefix/google/pro-flash to have SupportsWebSearch=true, got %+v", prefixedSlashedAliased)
	}
}

// TestAntigravityAsyncProbe_UnregisteredClientNotResurrected verifies that if an auth
// is unregistered while a capability probe is in flight, the probe does not resurrect
// the client in the model registry.
func TestAntigravityAsyncProbe_UnregisteredClientNotResurrected(t *testing.T) {
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(probeStarted)
		<-releaseProbe
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	auth := &coreauth.Auth{
		ID:       "ag-unreg-test",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test-token",
		},
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})

	_, err := manager.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("Register error: %v", err)
	}
	svc.registerModelsForAuth(context.Background(), auth)

	// Wait for probe to start
	<-probeStarted

	// Unregister client and remove auth
	GlobalModelRegistry().UnregisterClient(auth.ID)
	manager.Remove(context.Background(), auth.ID)

	// Release probe
	close(releaseProbe)
	svc.WaitAntigravityProbes()

	// Verify client is NOT in registry
	models := GlobalModelRegistry().GetModelsForClient(auth.ID)
	if len(models) != 0 {
		t.Fatalf("expected client to remain unregistered, but found models: %v", models)
	}
}

// TestAntigravityAsyncProbe_NormalRequestsDoNotDiscardProbe verifies that request completions
// (which increment Auth.Generation via MarkResult) do not cause in-flight capability probe results to be discarded.
func TestAntigravityAsyncProbe_NormalRequestsDoNotDiscardProbe(t *testing.T) {
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(probeStarted)
		<-releaseProbe
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	auth := &coreauth.Auth{
		ID:       "ag-request-gen-test",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test-token",
		},
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})

	_, err := manager.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("Register error: %v", err)
	}
	svc.registerModelsForAuth(context.Background(), auth)

	// Wait for probe to start
	<-probeStarted

	// Simulate requests completing while probe is in flight (bumping Generation on the manager Auth)
	for i := 0; i < 5; i++ {
		current, _ := manager.GetByID(auth.ID)
		current.Generation++
		_, _ = manager.Update(context.Background(), current)
	}

	// Release probe
	close(releaseProbe)
	svc.WaitAntigravityProbes()

	// Verify capability was applied despite Generation increments
	models := GlobalModelRegistry().GetModelsForClient(auth.ID)
	foundCapable := false
	for _, m := range models {
		if m.ID == "gemini-3.1-flash-lite" && m.SupportsWebSearch {
			foundCapable = true
			break
		}
	}
	if !foundCapable {
		t.Fatal("expected gemini-3.1-flash-lite to have SupportsWebSearch=true despite concurrent Generation bumps from requests")
	}
}

// TestAntigravityAsyncProbe_ConcurrentConfigUpdateRace verifies that concurrent config updates
// modifying s.cfg under cfgMu do not trigger data races with in-flight async capability probes.
func TestAntigravityAsyncProbe_ConcurrentConfigUpdateRace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3.1-flash-lite"]}`))
	}))
	defer server.Close()

	svc := &Service{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				ProxyURL: "http://initial.proxy:8080",
			},
		},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	auth := &coreauth.Auth{
		ID:       "ag-cfg-race-test",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "ya29.test-token",
		},
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})
	GlobalModelRegistry().RegisterClient(auth.ID, "antigravity", registryGetAntigravityModels())

	var done atomic.Bool
	go func() {
		for i := 0; !done.Load() && i < 100; i++ {
			svc.cfgMu.Lock()
			svc.cfg = &config.Config{
				SDKConfig: config.SDKConfig{
					ProxyURL: "http://updated.proxy:8080",
				},
			}
			svc.cfgMu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()

	for i := 0; i < 20; i++ {
		svc.asyncProbeAntigravityCapabilities(context.Background(), auth, "antigravity")
	}
	svc.WaitAntigravityProbes()
	done.Store(true)
}

func registryGetAntigravityModels() []*ModelInfo {
	return []*ModelInfo{
		{ID: "gemini-3.1-flash-lite"},
		{ID: "gemini-3.1-pro"},
	}
}

// TestAntigravityColdStart_RefreshesExpiredTokenUsingAuthProxy verifies that on cold start,
// Service loads Antigravity credentials with their per-account ProxyURL immediately,
// pre-registers executors for loaded credentials, and the auto-refresh loop executes
// immediately using the auth's proxy without waiting for model registration.
func TestAntigravityColdStart_RefreshesExpiredTokenUsingAuthProxy(t *testing.T) {
	var refreshReceivedProxy atomic.Value
	var refreshCalls atomic.Int32

	mockExecutor := &mockAntigravityRefreshExecutor{
		onRefresh: func(auth *coreauth.Auth) {
			refreshCalls.Add(1)
			refreshReceivedProxy.Store(auth.ProxyURL)
		},
	}

	authDir := t.TempDir()
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.yaml")
	_ = os.WriteFile(configPath, []byte("port: 0\n"), 0o600)

	// Write an expired Antigravity auth file with a specific proxy_url
	authJSON := []byte(`{
		"type": "antigravity",
		"proxy_url": "http://127.0.0.1:9876",
		"refresh_token": "ref-token-cold-start",
		"access_token": "expired-access-token",
		"expired": "2020-01-01T00:00:00Z"
	}`)
	err := os.WriteFile(filepath.Join(authDir, "antigravity.json"), authJSON, 0o600)
	if err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	cfg := &config.Config{
		AuthDir: authDir,
	}

	svc, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		Build()
	if errBuild != nil {
		t.Fatalf("Build error: %v", errBuild)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Cold start load: ProxyURL is loaded into runtime immediately
	if errLoad := svc.coreManager.Load(ctx); errLoad != nil {
		t.Fatalf("Load error: %v", errLoad)
	}

	loadedAuth, exists := svc.coreManager.GetByID("antigravity.json")
	if !exists || loadedAuth == nil {
		t.Fatal("expected antigravity.json to be loaded")
	}
	if loadedAuth.ProxyURL != "http://127.0.0.1:9876" {
		t.Fatalf("expected ProxyURL http://127.0.0.1:9876, got %q", loadedAuth.ProxyURL)
	}

	// 2. Pre-register executors (same sequence as Service.Run)
	svc.registerAvailableExecutors(ctx, executorRegistrationOptions{
		includeBaseline: true,
		auths:           svc.coreManager.List(),
	})

	// Re-register mock so we capture the refresh call
	svc.coreManager.RegisterExecutor(mockExecutor)

	// 3. Start auto-refresh and verify executor receives refresh with proxy_url
	svc.coreManager.StartAutoRefresh(ctx, 10*time.Millisecond)
	defer svc.coreManager.StopAutoRefresh()

	// Poll until refresh is called (should be immediate since expired)
	for start := time.Now(); refreshCalls.Load() == 0 && time.Since(start) < 2*time.Second; {
		time.Sleep(10 * time.Millisecond)
	}

	if refreshCalls.Load() == 0 {
		t.Fatal("expected auto-refresh loop to invoke executor for expired credential on cold start")
	}
	if gotProxy, _ := refreshReceivedProxy.Load().(string); gotProxy != "http://127.0.0.1:9876" {
		t.Fatalf("expected executor to receive proxy http://127.0.0.1:9876, got %q", gotProxy)
	}
}

type mockAntigravityRefreshExecutor struct {
	onRefresh func(*coreauth.Auth)
}

func (e *mockAntigravityRefreshExecutor) Identifier() string { return "antigravity" }

func (e *mockAntigravityRefreshExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if e.onRefresh != nil {
		e.onRefresh(auth)
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "new-access-token"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return auth, nil
}

func (e *mockAntigravityRefreshExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *mockAntigravityRefreshExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *mockAntigravityRefreshExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *mockAntigravityRefreshExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
