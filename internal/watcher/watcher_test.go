package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/diff"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/synthesizer"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

func TestApplyAuthExcludedModelsMeta_APIKey(t *testing.T) {
	auth := &coreauth.Auth{Attributes: map[string]string{}}
	cfg := &config.Config{}
	perKey := []string{" Model-1 ", "model-2"}

	synthesizer.ApplyAuthExcludedModelsMeta(auth, cfg, perKey, "apikey")

	expected := diff.ComputeExcludedModelsHash([]string{"model-1", "model-2"})
	if got := auth.Attributes["excluded_models_hash"]; got != expected {
		t.Fatalf("expected hash %s, got %s", expected, got)
	}
	if got := auth.Attributes["auth_kind"]; got != "apikey" {
		t.Fatalf("expected auth_kind=apikey, got %s", got)
	}
}

func TestApplyAuthExcludedModelsMeta_OAuthProvider(t *testing.T) {
	auth := &coreauth.Auth{
		Provider:   "TestProv",
		Attributes: map[string]string{},
	}
	cfg := &config.Config{
		OAuthExcludedModels: map[string][]string{
			"testprov": {"A", "b"},
		},
	}

	synthesizer.ApplyAuthExcludedModelsMeta(auth, cfg, nil, "oauth")

	expected := diff.ComputeExcludedModelsHash([]string{"a", "b"})
	if got := auth.Attributes["excluded_models_hash"]; got != expected {
		t.Fatalf("expected hash %s, got %s", expected, got)
	}
	if got := auth.Attributes["auth_kind"]; got != "oauth" {
		t.Fatalf("expected auth_kind=oauth, got %s", got)
	}
}

func TestBuildAPIKeyClientsCounts(t *testing.T) {
	cfg := &config.Config{
		GeminiKey: []config.GeminiKey{{APIKey: "g1"}, {APIKey: "g2"}},
		VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "v1"},
		},
		ClaudeKey: []config.ClaudeKey{{APIKey: "c1"}},
		CodexKey:  []config.CodexKey{{APIKey: "x1"}, {APIKey: "x2"}},
		OpenAICompatibility: []config.OpenAICompatibility{
			{APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "o1"}, {APIKey: "o2"}}},
		},
	}

	gemini, vertex, claude, codex, compat := BuildAPIKeyClients(cfg)
	if gemini != 2 || vertex != 1 || claude != 1 || codex != 2 || compat != 2 {
		t.Fatalf("unexpected counts: %d %d %d %d %d", gemini, vertex, claude, codex, compat)
	}
}

func TestNormalizeAuthStripsTemporalFields(t *testing.T) {
	now := time.Now()
	auth := &coreauth.Auth{
		CreatedAt:        now,
		UpdatedAt:        now,
		LastRefreshedAt:  now,
		NextRefreshAfter: now,
		Quota: coreauth.QuotaState{
			NextRecoverAt: now,
		},
		Runtime: map[string]any{"k": "v"},
	}

	normalized := normalizeAuth(auth)
	if !normalized.CreatedAt.IsZero() || !normalized.UpdatedAt.IsZero() || !normalized.LastRefreshedAt.IsZero() || !normalized.NextRefreshAfter.IsZero() {
		t.Fatal("expected time fields to be zeroed")
	}
	if normalized.Runtime != nil {
		t.Fatal("expected runtime to be nil")
	}
	if !normalized.Quota.NextRecoverAt.IsZero() {
		t.Fatal("expected quota.NextRecoverAt to be zeroed")
	}
}

func TestMatchProvider(t *testing.T) {
	if _, ok := matchProvider("OpenAI", []string{"openai", "claude"}); !ok {
		t.Fatal("expected match to succeed ignoring case")
	}
	if _, ok := matchProvider("missing", []string{"openai"}); ok {
		t.Fatal("expected match to fail for unknown provider")
	}
}

func TestSnapshotCoreAuths_ConfigAndAuthFiles(t *testing.T) {
	authDir := t.TempDir()
	metadata := map[string]any{
		"type":       "gemini",
		"email":      "user@example.com",
		"project_id": "proj-a, proj-b",
		"proxy_url":  "https://proxy",
	}
	authFile := filepath.Join(authDir, "gemini.json")
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}
	if err = os.WriteFile(authFile, data, 0o644); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	cfg := &config.Config{
		AuthDir: authDir,
		GeminiKey: []config.GeminiKey{
			{
				APIKey:         "g-key",
				BaseURL:        "https://gemini",
				ExcludedModels: []string{"Model-A", "model-b"},
				Headers:        map[string]string{"X-Req": "1"},
			},
		},
		OAuthExcludedModels: map[string][]string{
			"gemini-cli": {"Foo", "bar"},
		},
	}

	w := &Watcher{authDir: authDir}
	w.SetConfig(cfg)

	auths := w.SnapshotCoreAuths()
	if len(auths) != 4 {
		t.Fatalf("expected 4 auth entries (1 config + 1 primary + 2 virtual), got %d", len(auths))
	}

	var geminiAPIKeyAuth *coreauth.Auth
	var geminiPrimary *coreauth.Auth
	virtuals := make([]*coreauth.Auth, 0)
	for _, a := range auths {
		switch {
		case a.Provider == "gemini" && a.Attributes["api_key"] == "g-key":
			geminiAPIKeyAuth = a
		case a.Attributes["gemini_virtual_primary"] == "true":
			geminiPrimary = a
		case strings.TrimSpace(a.Attributes["gemini_virtual_parent"]) != "":
			virtuals = append(virtuals, a)
		}
	}
	if geminiAPIKeyAuth == nil {
		t.Fatal("expected synthesized Gemini API key auth")
	}
	expectedAPIKeyHash := diff.ComputeExcludedModelsHash([]string{"Model-A", "model-b"})
	if geminiAPIKeyAuth.Attributes["excluded_models_hash"] != expectedAPIKeyHash {
		t.Fatalf("expected API key excluded hash %s, got %s", expectedAPIKeyHash, geminiAPIKeyAuth.Attributes["excluded_models_hash"])
	}
	if geminiAPIKeyAuth.Attributes["auth_kind"] != "apikey" {
		t.Fatalf("expected auth_kind=apikey, got %s", geminiAPIKeyAuth.Attributes["auth_kind"])
	}

	if geminiPrimary == nil {
		t.Fatal("expected primary gemini-cli auth from file")
	}
	if !geminiPrimary.Disabled || geminiPrimary.Status != coreauth.StatusDisabled {
		t.Fatal("expected primary gemini-cli auth to be disabled when virtual auths are synthesized")
	}
	expectedOAuthHash := diff.ComputeExcludedModelsHash([]string{"Foo", "bar"})
	if geminiPrimary.Attributes["excluded_models_hash"] != expectedOAuthHash {
		t.Fatalf("expected OAuth excluded hash %s, got %s", expectedOAuthHash, geminiPrimary.Attributes["excluded_models_hash"])
	}
	if geminiPrimary.Attributes["auth_kind"] != "oauth" {
		t.Fatalf("expected auth_kind=oauth, got %s", geminiPrimary.Attributes["auth_kind"])
	}

	if len(virtuals) != 2 {
		t.Fatalf("expected 2 virtual auths, got %d", len(virtuals))
	}
	for _, v := range virtuals {
		if v.Attributes["gemini_virtual_parent"] != geminiPrimary.ID {
			t.Fatalf("virtual auth missing parent link to %s", geminiPrimary.ID)
		}
		if v.Attributes["excluded_models_hash"] != expectedOAuthHash {
			t.Fatalf("expected virtual excluded hash %s, got %s", expectedOAuthHash, v.Attributes["excluded_models_hash"])
		}
		if v.Status != coreauth.StatusActive {
			t.Fatalf("expected virtual auth to be active, got %s", v.Status)
		}
	}
}

func TestReloadConfigIfChanged_TriggersOnChangeAndSkipsUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	configPath := filepath.Join(tmpDir, "config.yaml")
	writeConfig := func(port int, allowRemote bool) {
		cfg := &config.Config{
			Port:    port,
			AuthDir: authDir,
			RemoteManagement: config.RemoteManagement{
				AllowRemote: allowRemote,
			},
		}
		data, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatalf("failed to marshal config: %v", err)
		}
		if err = os.WriteFile(configPath, data, 0o644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}
	}

	writeConfig(8080, false)

	reloads := 0
	w := &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		reloadCallback: func(*config.Config) { reloads++ },
	}

	w.reloadConfigIfChanged()
	if reloads != 1 {
		t.Fatalf("expected first reload to trigger callback once, got %d", reloads)
	}

	// Same content should be skipped by hash check.
	w.reloadConfigIfChanged()
	if reloads != 1 {
		t.Fatalf("expected unchanged config to be skipped, callback count %d", reloads)
	}

	writeConfig(9090, true)
	w.reloadConfigIfChanged()
	if reloads != 2 {
		t.Fatalf("expected changed config to trigger reload, callback count %d", reloads)
	}
	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	if w.config == nil || w.config.Port != 9090 || !w.config.RemoteManagement.AllowRemote {
		t.Fatalf("expected config to be updated after reload, got %+v", w.config)
	}
}

func TestStartAndStopSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir), 0o644); err != nil {
		t.Fatalf("failed to create config file: %v", err)
	}

	var reloads int32
	w, err := NewWatcher(configPath, authDir, func(*config.Config) {
		atomic.AddInt32(&reloads, 1)
	})
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	w.SetConfig(&config.Config{AuthDir: authDir})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("expected Start to succeed: %v", err)
	}
	cancel()
	if err := w.Stop(); err != nil {
		t.Fatalf("expected Stop to succeed: %v", err)
	}
	if got := atomic.LoadInt32(&reloads); got != 1 {
		t.Fatalf("expected one reload callback, got %d", got)
	}
}

func TestStartFailsWhenConfigMissing(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "missing-config.yaml")

	w, err := NewWatcher(configPath, authDir, nil)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer w.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := w.Start(ctx); err == nil {
		t.Fatal("expected Start to fail for missing config file")
	}
}

func TestDispatchRuntimeAuthUpdateEnqueuesAndUpdatesState(t *testing.T) {
	queue := make(chan AuthUpdate, 4)
	w := &Watcher{}
	w.SetAuthUpdateQueue(queue)
	defer w.stopDispatch()

	auth := &coreauth.Auth{ID: "auth-1", Provider: "test"}
	if ok := w.DispatchRuntimeAuthUpdate(AuthUpdate{Action: AuthUpdateActionAdd, Auth: auth}); !ok {
		t.Fatal("expected DispatchRuntimeAuthUpdate to enqueue")
	}

	select {
	case update := <-queue:
		if update.Action != AuthUpdateActionAdd || update.Auth.ID != "auth-1" {
			t.Fatalf("unexpected update: %+v", update)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for auth update")
	}

	if ok := w.DispatchRuntimeAuthUpdate(AuthUpdate{Action: AuthUpdateActionDelete, ID: "auth-1"}); !ok {
		t.Fatal("expected delete update to enqueue")
	}
	select {
	case update := <-queue:
		if update.Action != AuthUpdateActionDelete || update.ID != "auth-1" {
			t.Fatalf("unexpected delete update: %+v", update)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delete update")
	}
	w.clientsMutex.RLock()
	if _, exists := w.runtimeAuths["auth-1"]; exists {
		w.clientsMutex.RUnlock()
		t.Fatal("expected runtime auth to be cleared after delete")
	}
	w.clientsMutex.RUnlock()
}

func TestAddOrUpdateClientSkipsUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "sample.json")
	if err := os.WriteFile(authFile, []byte(`{"type":"demo"}`), 0o644); err != nil {
		t.Fatalf("failed to create auth file: %v", err)
	}
	data, _ := os.ReadFile(authFile)
	sum := sha256.Sum256(data)

	var reloads int32
	w := &Watcher{
		authDir:        tmpDir,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) {
			atomic.AddInt32(&reloads, 1)
		},
	}
	w.SetConfig(&config.Config{AuthDir: tmpDir})
	// Use normalizeAuthPath to match how addOrUpdateClient stores the key
	w.lastAuthHashes[w.normalizeAuthPath(authFile)] = hexString(sum[:])

	w.addOrUpdateClient(authFile)
	if got := atomic.LoadInt32(&reloads); got != 0 {
		t.Fatalf("expected no reload for unchanged file, got %d", got)
	}
}

func TestAddOrUpdateClientTriggersReloadAndHash(t *testing.T) {
	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "sample.json")
	if err := os.WriteFile(authFile, []byte(`{"type":"demo","api_key":"k"}`), 0o644); err != nil {
		t.Fatalf("failed to create auth file: %v", err)
	}

	var reloads int32
	w := &Watcher{
		authDir:        tmpDir,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) {
			atomic.AddInt32(&reloads, 1)
		},
	}
	w.SetConfig(&config.Config{AuthDir: tmpDir})

	w.addOrUpdateClient(authFile)

	if got := atomic.LoadInt32(&reloads); got != 1 {
		t.Fatalf("expected reload callback once, got %d", got)
	}
	// Use normalizeAuthPath to match how addOrUpdateClient stores the key
	normalized := w.normalizeAuthPath(authFile)
	if _, ok := w.lastAuthHashes[normalized]; !ok {
		t.Fatalf("expected hash to be stored for %s", normalized)
	}
}

func TestRemoveClientRemovesHash(t *testing.T) {
	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "sample.json")
	var reloads int32

	w := &Watcher{
		authDir:        tmpDir,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) {
			atomic.AddInt32(&reloads, 1)
		},
	}
	w.SetConfig(&config.Config{AuthDir: tmpDir})
	// Use normalizeAuthPath to set up the hash with the correct key format
	w.lastAuthHashes[w.normalizeAuthPath(authFile)] = "hash"

	w.removeClient(authFile)
	if _, ok := w.lastAuthHashes[w.normalizeAuthPath(authFile)]; ok {
		t.Fatal("expected hash to be removed after deletion")
	}
	if got := atomic.LoadInt32(&reloads); got != 1 {
		t.Fatalf("expected reload callback once, got %d", got)
	}
}

func TestShouldDebounceRemove(t *testing.T) {
	w := &Watcher{}
	path := filepath.Clean("test.json")

	if w.shouldDebounceRemove(path, time.Now()) {
		t.Fatal("first call should not debounce")
	}
	if !w.shouldDebounceRemove(path, time.Now()) {
		t.Fatal("second call within window should debounce")
	}

	w.clientsMutex.Lock()
	w.lastRemoveTimes = map[string]time.Time{path: time.Now().Add(-2 * authRemoveDebounceWindow)}
	w.clientsMutex.Unlock()

	if w.shouldDebounceRemove(path, time.Now()) {
		t.Fatal("call after window should not debounce")
	}
}

func TestAuthFileUnchangedUsesHash(t *testing.T) {
	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "sample.json")
	content := []byte(`{"type":"demo"}`)
	if err := os.WriteFile(authFile, content, 0o644); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	w := &Watcher{lastAuthHashes: make(map[string]string)}
	unchanged, err := w.authFileUnchanged(authFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if unchanged {
		t.Fatal("expected first check to report changed")
	}

	sum := sha256.Sum256(content)
	// Use normalizeAuthPath to match how authFileUnchanged looks up the key
	w.lastAuthHashes[w.normalizeAuthPath(authFile)] = hexString(sum[:])

	unchanged, err = w.authFileUnchanged(authFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !unchanged {
		t.Fatal("expected hash match to report unchanged")
	}
}

func TestAuthFileUnchangedEmptyAndMissing(t *testing.T) {
	tmpDir := t.TempDir()
	emptyFile := filepath.Join(tmpDir, "empty.json")
	if err := os.WriteFile(emptyFile, []byte(""), 0o644); err != nil {
		t.Fatalf("failed to write empty auth file: %v", err)
	}

	w := &Watcher{lastAuthHashes: make(map[string]string)}
	unchanged, err := w.authFileUnchanged(emptyFile)
	if err != nil {
		t.Fatalf("unexpected error for empty file: %v", err)
	}
	if unchanged {
		t.Fatal("expected empty file to be treated as changed")
	}

	_, err = w.authFileUnchanged(filepath.Join(tmpDir, "missing.json"))
	if err == nil {
		t.Fatal("expected error for missing auth file")
	}
}

func TestReloadClientsCachesAuthHashes(t *testing.T) {
	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "one.json")
	if err := os.WriteFile(authFile, []byte(`{"type":"demo"}`), 0o644); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}
	w := &Watcher{
		authDir: tmpDir,
		config:  &config.Config{AuthDir: tmpDir},
	}

	w.reloadClients(true, nil, false)

	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	if len(w.lastAuthHashes) != 1 {
		t.Fatalf("expected hash cache for one auth file, got %d", len(w.lastAuthHashes))
	}
}

func TestReloadClientsLogsConfigDiffs(t *testing.T) {
	tmpDir := t.TempDir()
	oldCfg := &config.Config{AuthDir: tmpDir, Port: 1, Debug: false}
	newCfg := &config.Config{AuthDir: tmpDir, Port: 2, Debug: true}

	w := &Watcher{
		authDir: tmpDir,
		config:  oldCfg,
	}
	w.SetConfig(oldCfg)
	w.oldConfigYaml, _ = yaml.Marshal(oldCfg)

	w.clientsMutex.Lock()
	w.config = newCfg
	w.clientsMutex.Unlock()

	w.reloadClients(false, nil, false)
}

func TestReloadClientsHandlesNilConfig(t *testing.T) {
	w := &Watcher{}
	w.reloadClients(true, nil, false)
}

func TestReloadClientsFiltersProvidersWithNilCurrentAuths(t *testing.T) {
	tmp := t.TempDir()
	w := &Watcher{
		authDir: tmp,
		config:  &config.Config{AuthDir: tmp},
	}
	w.reloadClients(false, []string{"match"}, false)
	if w.currentAuths != nil && len(w.currentAuths) != 0 {
		t.Fatalf("expected currentAuths to be nil or empty, got %d", len(w.currentAuths))
	}
}

func TestSetAuthUpdateQueueNilResetsDispatch(t *testing.T) {
	w := &Watcher{}
	queue := make(chan AuthUpdate, 1)
	w.SetAuthUpdateQueue(queue)
	if w.dispatchCond == nil || w.dispatchCancel == nil {
		t.Fatal("expected dispatch to be initialized")
	}
	w.SetAuthUpdateQueue(nil)
	if w.dispatchCancel != nil {
		t.Fatal("expected dispatch cancel to be cleared when queue nil")
	}
}

func TestPersistAsyncEarlyReturns(t *testing.T) {
	var nilWatcher *Watcher
	nilWatcher.persistConfigAsync()
	nilWatcher.persistAuthAsync("msg", "a")

	w := &Watcher{}
	w.persistConfigAsync()
	w.persistAuthAsync("msg", "   ", "")
}

type errorPersister struct {
	configCalls int32
	authCalls   int32
}

func (p *errorPersister) PersistConfig(context.Context) error {
	atomic.AddInt32(&p.configCalls, 1)
	return fmt.Errorf("persist config error")
}

func (p *errorPersister) PersistAuthFiles(context.Context, string, ...string) error {
	atomic.AddInt32(&p.authCalls, 1)
	return fmt.Errorf("persist auth error")
}

func TestPersistAsyncErrorPaths(t *testing.T) {
	p := &errorPersister{}
	w := &Watcher{storePersister: p}
	w.persistConfigAsync()
	w.persistAuthAsync("msg", "a")
	time.Sleep(30 * time.Millisecond)
	if atomic.LoadInt32(&p.configCalls) != 1 {
		t.Fatalf("expected PersistConfig to be called once, got %d", p.configCalls)
	}
	if atomic.LoadInt32(&p.authCalls) != 1 {
		t.Fatalf("expected PersistAuthFiles to be called once, got %d", p.authCalls)
	}
}

func TestStopConfigReloadTimerSafeWhenNil(t *testing.T) {
	w := &Watcher{}
	w.stopConfigReloadTimer()
	w.configReloadMu.Lock()
	w.configReloadTimer = time.AfterFunc(10*time.Millisecond, func() {})
	w.configReloadMu.Unlock()
	time.Sleep(1 * time.Millisecond)
	w.stopConfigReloadTimer()
}

func TestHandleEventRemovesAuthFile(t *testing.T) {
	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "remove.json")
	if err := os.WriteFile(authFile, []byte(`{"type":"demo"}`), 0o644); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}
	if err := os.Remove(authFile); err != nil {
		t.Fatalf("failed to remove auth file pre-check: %v", err)
	}

	var reloads int32
	w := &Watcher{
		authDir:        tmpDir,
		config:         &config.Config{AuthDir: tmpDir},
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) {
			atomic.AddInt32(&reloads, 1)
		},
	}
	// Use normalizeAuthPath to set up the hash with the correct key format
	w.lastAuthHashes[w.normalizeAuthPath(authFile)] = "hash"

	w.handleEvent(fsnotify.Event{Name: authFile, Op: fsnotify.Remove})

	if atomic.LoadInt32(&reloads) != 1 {
		t.Fatalf("expected reload callback once, got %d", reloads)
	}
	if _, ok := w.lastAuthHashes[w.normalizeAuthPath(authFile)]; ok {
		t.Fatal("expected hash entry to be removed")
	}
}

func TestDispatchAuthUpdatesFlushesQueue(t *testing.T) {
	queue := make(chan AuthUpdate, 4)
	w := &Watcher{}
	w.SetAuthUpdateQueue(queue)
	defer w.stopDispatch()

	w.dispatchAuthUpdates([]AuthUpdate{
		{Action: AuthUpdateActionAdd, ID: "a"},
		{Action: AuthUpdateActionModify, ID: "b"},
	})

	got := make([]AuthUpdate, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case u := <-queue:
			got = append(got, u)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for update %d", i)
		}
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("unexpected updates order/content: %+v", got)
	}
}

func TestDispatchLoopExitsOnContextDoneWhileSending(t *testing.T) {
	queue := make(chan AuthUpdate) // unbuffered to block sends
	w := &Watcher{
		authQueue: queue,
		pendingUpdates: map[string]AuthUpdate{
			"k": {Action: AuthUpdateActionAdd, ID: "k"},
		},
		pendingOrder: []string{"k"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.dispatchLoop(ctx)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected dispatchLoop to exit after ctx canceled while blocked on send")
	}
}

func TestProcessEventsHandlesEventErrorAndChannelClose(t *testing.T) {
	w := &Watcher{
		watcher: &fsnotify.Watcher{
			Events: make(chan fsnotify.Event, 2),
			Errors: make(chan error, 2),
		},
		configPath: "config.yaml",
		authDir:    "auth",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.processEvents(ctx)
		close(done)
	}()

	w.watcher.Events <- fsnotify.Event{Name: "unrelated.txt", Op: fsnotify.Write}
	w.watcher.Errors <- fmt.Errorf("watcher error")

	time.Sleep(20 * time.Millisecond)
	close(w.watcher.Events)
	close(w.watcher.Errors)

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("processEvents did not exit after channels closed")
	}
}

func TestProcessEventsReturnsWhenErrorsChannelClosed(t *testing.T) {
	w := &Watcher{
		watcher: &fsnotify.Watcher{
			Events: nil,
			Errors: make(chan error),
		},
	}

	close(w.watcher.Errors)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.processEvents(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("processEvents did not exit after errors channel closed")
	}
}

func TestHandleEventIgnoresUnrelatedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	var reloads int32
	w := &Watcher{
		authDir:        authDir,
		configPath:     configPath,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) { atomic.AddInt32(&reloads, 1) },
	}
	w.SetConfig(&config.Config{AuthDir: authDir})

	w.handleEvent(fsnotify.Event{Name: filepath.Join(tmpDir, "note.txt"), Op: fsnotify.Write})
	if atomic.LoadInt32(&reloads) != 0 {
		t.Fatalf("expected no reloads for unrelated file, got %d", reloads)
	}
}

func TestHandleEventConfigChangeSchedulesReload(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	var reloads int32
	w := &Watcher{
		authDir:        authDir,
		configPath:     configPath,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) { atomic.AddInt32(&reloads, 1) },
	}
	w.SetConfig(&config.Config{AuthDir: authDir})

	w.handleEvent(fsnotify.Event{Name: configPath, Op: fsnotify.Write})

	time.Sleep(400 * time.Millisecond)
	if atomic.LoadInt32(&reloads) != 1 {
		t.Fatalf("expected config change to trigger reload once, got %d", reloads)
	}
}

func TestHandleEventAuthWriteTriggersUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	authFile := filepath.Join(authDir, "a.json")
	if err := os.WriteFile(authFile, []byte(`{"type":"demo"}`), 0o644); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	var reloads int32
	w := &Watcher{
		authDir:        authDir,
		configPath:     configPath,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) { atomic.AddInt32(&reloads, 1) },
	}
	w.SetConfig(&config.Config{AuthDir: authDir})

	w.handleEvent(fsnotify.Event{Name: authFile, Op: fsnotify.Write})
	if atomic.LoadInt32(&reloads) != 1 {
		t.Fatalf("expected auth write to trigger reload callback, got %d", reloads)
	}
}

func TestHandleEventRemoveDebounceSkips(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	authFile := filepath.Join(authDir, "remove.json")

	var reloads int32
	w := &Watcher{
		authDir:        authDir,
		configPath:     configPath,
		lastAuthHashes: make(map[string]string),
		lastRemoveTimes: map[string]time.Time{
			filepath.Clean(authFile): time.Now(),
		},
		reloadCallback: func(*config.Config) { atomic.AddInt32(&reloads, 1) },
	}
	w.SetConfig(&config.Config{AuthDir: authDir})

	w.handleEvent(fsnotify.Event{Name: authFile, Op: fsnotify.Remove})
	if atomic.LoadInt32(&reloads) != 0 {
		t.Fatalf("expected remove to be debounced, got %d", reloads)
	}
}

func TestHandleEventAtomicReplaceUnchangedSkips(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	authFile := filepath.Join(authDir, "same.json")
	content := []byte(`{"type":"demo"}`)
	if err := os.WriteFile(authFile, content, 0o644); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}
	sum := sha256.Sum256(content)

	var reloads int32
	w := &Watcher{
		authDir:        authDir,
		configPath:     configPath,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) { atomic.AddInt32(&reloads, 1) },
	}
	w.SetConfig(&config.Config{AuthDir: authDir})
	w.lastAuthHashes[w.normalizeAuthPath(authFile)] = hexString(sum[:])

	w.handleEvent(fsnotify.Event{Name: authFile, Op: fsnotify.Rename})
	if atomic.LoadInt32(&reloads) != 0 {
		t.Fatalf("expected unchanged atomic replace to be skipped, got %d", reloads)
	}
}

func TestHandleEventAtomicReplaceChangedTriggersUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	authFile := filepath.Join(authDir, "change.json")
	oldContent := []byte(`{"type":"demo","v":1}`)
	newContent := []byte(`{"type":"demo","v":2}`)
	if err := os.WriteFile(authFile, newContent, 0o644); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}
	oldSum := sha256.Sum256(oldContent)

	var reloads int32
	w := &Watcher{
		authDir:        authDir,
		configPath:     configPath,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) { atomic.AddInt32(&reloads, 1) },
	}
	w.SetConfig(&config.Config{AuthDir: authDir})
	w.lastAuthHashes[w.normalizeAuthPath(authFile)] = hexString(oldSum[:])

	w.handleEvent(fsnotify.Event{Name: authFile, Op: fsnotify.Rename})
	if atomic.LoadInt32(&reloads) != 1 {
		t.Fatalf("expected changed atomic replace to trigger update, got %d", reloads)
	}
}

func TestHandleEventRemoveUnknownFileIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	authFile := filepath.Join(authDir, "unknown.json")

	var reloads int32
	w := &Watcher{
		authDir:        authDir,
		configPath:     configPath,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) { atomic.AddInt32(&reloads, 1) },
	}
	w.SetConfig(&config.Config{AuthDir: authDir})

	w.handleEvent(fsnotify.Event{Name: authFile, Op: fsnotify.Remove})
	if atomic.LoadInt32(&reloads) != 0 {
		t.Fatalf("expected unknown remove to be ignored, got %d", reloads)
	}
}

func TestHandleEventRemoveKnownFileDeletes(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	authFile := filepath.Join(authDir, "known.json")

	var reloads int32
	w := &Watcher{
		authDir:        authDir,
		configPath:     configPath,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) { atomic.AddInt32(&reloads, 1) },
	}
	w.SetConfig(&config.Config{AuthDir: authDir})
	w.lastAuthHashes[w.normalizeAuthPath(authFile)] = "hash"

	w.handleEvent(fsnotify.Event{Name: authFile, Op: fsnotify.Remove})
	if atomic.LoadInt32(&reloads) != 1 {
		t.Fatalf("expected known remove to trigger reload, got %d", reloads)
	}
	if _, ok := w.lastAuthHashes[w.normalizeAuthPath(authFile)]; ok {
		t.Fatal("expected known auth hash to be deleted")
	}
}

func TestNormalizeAuthPathAndDebounceCleanup(t *testing.T) {
	w := &Watcher{}
	if got := w.normalizeAuthPath("   "); got != "" {
		t.Fatalf("expected empty normalize result, got %q", got)
	}
	if got := w.normalizeAuthPath("  a/../b  "); got != filepath.Clean("a/../b") {
		t.Fatalf("unexpected normalize result: %q", got)
	}

	w.clientsMutex.Lock()
	w.lastRemoveTimes = make(map[string]time.Time, 140)
	old := time.Now().Add(-3 * authRemoveDebounceWindow)
	for i := 0; i < 129; i++ {
		w.lastRemoveTimes[fmt.Sprintf("old-%d", i)] = old
	}
	w.clientsMutex.Unlock()

	w.shouldDebounceRemove("new-path", time.Now())

	w.clientsMutex.Lock()
	gotLen := len(w.lastRemoveTimes)
	w.clientsMutex.Unlock()
	if gotLen >= 129 {
		t.Fatalf("expected debounce cleanup to shrink map, got %d", gotLen)
	}
}

func TestRefreshAuthStateDispatchesRuntimeAuths(t *testing.T) {
	queue := make(chan AuthUpdate, 8)
	w := &Watcher{
		authDir:        t.TempDir(),
		lastAuthHashes: make(map[string]string),
	}
	w.SetConfig(&config.Config{AuthDir: w.authDir})
	w.SetAuthUpdateQueue(queue)
	defer w.stopDispatch()

	w.clientsMutex.Lock()
	w.runtimeAuths = map[string]*coreauth.Auth{
		"nil": nil,
		"r1":  {ID: "r1", Provider: "runtime"},
	}
	w.clientsMutex.Unlock()

	w.refreshAuthState(false)

	select {
	case u := <-queue:
		if u.Action != AuthUpdateActionAdd || u.ID != "r1" {
			t.Fatalf("unexpected auth update: %+v", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime auth update")
	}
}

func TestAddOrUpdateClientEdgeCases(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := tmpDir
	authFile := filepath.Join(tmpDir, "edge.json")
	if err := os.WriteFile(authFile, []byte(`{"type":"demo"}`), 0o644); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}
	emptyFile := filepath.Join(tmpDir, "empty.json")
	if err := os.WriteFile(emptyFile, []byte(""), 0o644); err != nil {
		t.Fatalf("failed to write empty auth file: %v", err)
	}

	var reloads int32
	w := &Watcher{
		authDir:        authDir,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) { atomic.AddInt32(&reloads, 1) },
	}

	w.addOrUpdateClient(filepath.Join(tmpDir, "missing.json"))
	w.addOrUpdateClient(emptyFile)
	if atomic.LoadInt32(&reloads) != 0 {
		t.Fatalf("expected no reloads for missing/empty file, got %d", reloads)
	}

	w.addOrUpdateClient(authFile) // config nil -> should not panic or update
	if len(w.lastAuthHashes) != 0 {
		t.Fatalf("expected no hash entries without config, got %d", len(w.lastAuthHashes))
	}
}

func TestLoadFileClientsWalkError(t *testing.T) {
	tmpDir := t.TempDir()
	noAccessDir := filepath.Join(tmpDir, "0noaccess")
	if err := os.MkdirAll(noAccessDir, 0o755); err != nil {
		t.Fatalf("failed to create noaccess dir: %v", err)
	}
	if err := os.Chmod(noAccessDir, 0); err != nil {
		t.Skipf("chmod not supported: %v", err)
	}
	defer func() { _ = os.Chmod(noAccessDir, 0o755) }()

	cfg := &config.Config{AuthDir: tmpDir}
	w := &Watcher{}
	w.SetConfig(cfg)

	count := w.loadFileClients(cfg)
	if count != 0 {
		t.Fatalf("expected count 0 due to walk error, got %d", count)
	}
}

func TestReloadConfigIfChangedHandlesMissingAndEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	w := &Watcher{
		configPath: filepath.Join(tmpDir, "missing.yaml"),
		authDir:    authDir,
	}
	w.reloadConfigIfChanged() // missing file -> log + return

	emptyPath := filepath.Join(tmpDir, "empty.yaml")
	if err := os.WriteFile(emptyPath, []byte(""), 0o644); err != nil {
		t.Fatalf("failed to write empty config: %v", err)
	}
	w.configPath = emptyPath
	w.reloadConfigIfChanged() // empty file -> early return
}

func TestReloadConfigUsesMirroredAuthDir(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+filepath.Join(tmpDir, "other")+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	w := &Watcher{
		configPath:      configPath,
		authDir:         authDir,
		mirroredAuthDir: authDir,
		lastAuthHashes:  make(map[string]string),
	}
	w.SetConfig(&config.Config{AuthDir: authDir})

	if ok := w.reloadConfig(); !ok {
		t.Fatal("expected reloadConfig to succeed")
	}

	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	if w.config == nil || w.config.AuthDir != authDir {
		t.Fatalf("expected AuthDir to be overridden by mirroredAuthDir %s, got %+v", authDir, w.config)
	}
}

func TestReloadConfigFiltersAffectedOAuthProviders(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Ensure SnapshotCoreAuths yields a provider that is NOT affected, so we can assert it survives.
	if err := os.WriteFile(filepath.Join(authDir, "provider-b.json"), []byte(`{"type":"provider-b","email":"b@example.com"}`), 0o644); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	oldCfg := &config.Config{
		AuthDir: authDir,
		OAuthExcludedModels: map[string][]string{
			"provider-a": {"m1"},
		},
	}
	newCfg := &config.Config{
		AuthDir: authDir,
		OAuthExcludedModels: map[string][]string{
			"provider-a": {"m2"},
		},
	}
	data, err := yaml.Marshal(newCfg)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}
	if err = os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	w := &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		lastAuthHashes: make(map[string]string),
		currentAuths: map[string]*coreauth.Auth{
			"a": {ID: "a", Provider: "provider-a"},
		},
	}
	w.SetConfig(oldCfg)

	if ok := w.reloadConfig(); !ok {
		t.Fatal("expected reloadConfig to succeed")
	}

	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	for _, auth := range w.currentAuths {
		if auth != nil && auth.Provider == "provider-a" {
			t.Fatal("expected affected provider auth to be filtered")
		}
	}
	foundB := false
	for _, auth := range w.currentAuths {
		if auth != nil && auth.Provider == "provider-b" {
			foundB = true
			break
		}
	}
	if !foundB {
		t.Fatal("expected unaffected provider auth to remain")
	}
}

func TestStartFailsWhenAuthDirMissing(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+filepath.Join(tmpDir, "missing-auth")+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	authDir := filepath.Join(tmpDir, "missing-auth")

	w, err := NewWatcher(configPath, authDir, nil)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer w.Stop()
	w.SetConfig(&config.Config{AuthDir: authDir})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := w.Start(ctx); err == nil {
		t.Fatal("expected Start to fail for missing auth dir")
	}
}

func TestDispatchRuntimeAuthUpdateReturnsFalseWithoutQueue(t *testing.T) {
	w := &Watcher{}
	if ok := w.DispatchRuntimeAuthUpdate(AuthUpdate{Action: AuthUpdateActionAdd, Auth: &coreauth.Auth{ID: "a"}}); ok {
		t.Fatal("expected DispatchRuntimeAuthUpdate to return false when no queue configured")
	}
	if ok := w.DispatchRuntimeAuthUpdate(AuthUpdate{Action: AuthUpdateActionDelete, Auth: &coreauth.Auth{ID: "a"}}); ok {
		t.Fatal("expected DispatchRuntimeAuthUpdate delete to return false when no queue configured")
	}
}

func TestNormalizeAuthNil(t *testing.T) {
	if normalizeAuth(nil) != nil {
		t.Fatal("expected normalizeAuth(nil) to return nil")
	}
}

// stubStore implements coreauth.Store plus watcher-specific persistence helpers.
type stubStore struct {
	authDir         string
	cfgPersisted    int32
	authPersisted   int32
	lastAuthMessage string
	lastAuthPaths   []string
}

func (s *stubStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (s *stubStore) Save(context.Context, *coreauth.Auth) (string, error) {
	return "", nil
}
func (s *stubStore) Delete(context.Context, string) error { return nil }
func (s *stubStore) PersistConfig(context.Context) error {
	atomic.AddInt32(&s.cfgPersisted, 1)
	return nil
}
func (s *stubStore) PersistAuthFiles(_ context.Context, message string, paths ...string) error {
	atomic.AddInt32(&s.authPersisted, 1)
	s.lastAuthMessage = message
	s.lastAuthPaths = paths
	return nil
}
func (s *stubStore) AuthDir() string { return s.authDir }

func TestNewWatcherDetectsPersisterAndAuthDir(t *testing.T) {
	tmp := t.TempDir()
	store := &stubStore{authDir: tmp}
	orig := sdkAuth.GetTokenStore()
	sdkAuth.RegisterTokenStore(store)
	defer sdkAuth.RegisterTokenStore(orig)

	w, err := NewWatcher("config.yaml", "auth", nil)
	if err != nil {
		t.Fatalf("NewWatcher failed: %v", err)
	}
	if w.storePersister == nil {
		t.Fatal("expected storePersister to be set from token store")
	}
	if w.mirroredAuthDir != tmp {
		t.Fatalf("expected mirroredAuthDir %s, got %s", tmp, w.mirroredAuthDir)
	}
}

func TestPersistConfigAndAuthAsyncInvokePersister(t *testing.T) {
	w := &Watcher{
		storePersister: &stubStore{},
	}

	w.persistConfigAsync()
	w.persistAuthAsync("msg", " a ", "", "b ")

	time.Sleep(30 * time.Millisecond)
	store := w.storePersister.(*stubStore)
	if atomic.LoadInt32(&store.cfgPersisted) != 1 {
		t.Fatalf("expected PersistConfig to be called once, got %d", store.cfgPersisted)
	}
	if atomic.LoadInt32(&store.authPersisted) != 1 {
		t.Fatalf("expected PersistAuthFiles to be called once, got %d", store.authPersisted)
	}
	if store.lastAuthMessage != "msg" {
		t.Fatalf("unexpected auth message: %s", store.lastAuthMessage)
	}
	if len(store.lastAuthPaths) != 2 || store.lastAuthPaths[0] != "a" || store.lastAuthPaths[1] != "b" {
		t.Fatalf("unexpected filtered paths: %#v", store.lastAuthPaths)
	}
}

func TestScheduleConfigReloadDebounces(t *testing.T) {
	tmp := t.TempDir()
	authDir := tmp
	cfgPath := tmp + "/config.yaml"
	if err := os.WriteFile(cfgPath, []byte("auth_dir: "+authDir+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	var reloads int32
	w := &Watcher{
		configPath:     cfgPath,
		authDir:        authDir,
		reloadCallback: func(*config.Config) { atomic.AddInt32(&reloads, 1) },
	}
	w.SetConfig(&config.Config{AuthDir: authDir})

	w.scheduleConfigReload()
	w.scheduleConfigReload()

	time.Sleep(400 * time.Millisecond)

	if atomic.LoadInt32(&reloads) != 1 {
		t.Fatalf("expected single debounced reload, got %d", reloads)
	}
	if w.lastConfigHash == "" {
		t.Fatal("expected lastConfigHash to be set after reload")
	}
}

func TestPrepareAuthUpdatesLockedForceAndDelete(t *testing.T) {
	w := &Watcher{
		currentAuths: map[string]*coreauth.Auth{
			"a": {ID: "a", Provider: "p1"},
		},
		authQueue: make(chan AuthUpdate, 4),
	}

	updates := w.prepareAuthUpdatesLocked([]*coreauth.Auth{{ID: "a", Provider: "p2"}}, false)
	if len(updates) != 1 || updates[0].Action != AuthUpdateActionModify || updates[0].ID != "a" {
		t.Fatalf("unexpected modify updates: %+v", updates)
	}

	updates = w.prepareAuthUpdatesLocked([]*coreauth.Auth{{ID: "a", Provider: "p2"}}, true)
	if len(updates) != 1 || updates[0].Action != AuthUpdateActionModify {
		t.Fatalf("expected force modify, got %+v", updates)
	}

	updates = w.prepareAuthUpdatesLocked([]*coreauth.Auth{}, false)
	if len(updates) != 1 || updates[0].Action != AuthUpdateActionDelete || updates[0].ID != "a" {
		t.Fatalf("expected delete for missing auth, got %+v", updates)
	}
}

func TestAuthEqualIgnoresTemporalFields(t *testing.T) {
	now := time.Now()
	a := &coreauth.Auth{ID: "x", CreatedAt: now}
	b := &coreauth.Auth{ID: "x", CreatedAt: now.Add(5 * time.Second)}
	if !authEqual(a, b) {
		t.Fatal("expected authEqual to ignore temporal differences")
	}
}

func TestDispatchLoopExitsWhenQueueNilAndContextCanceled(t *testing.T) {
	w := &Watcher{
		dispatchCond:   nil,
		pendingUpdates: map[string]AuthUpdate{"k": {ID: "k"}},
		pendingOrder:   []string{"k"},
	}
	w.dispatchCond = sync.NewCond(&w.dispatchMu)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.dispatchLoop(ctx)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	w.dispatchMu.Lock()
	w.dispatchCond.Broadcast()
	w.dispatchMu.Unlock()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("dispatchLoop did not exit after context cancel")
	}
}

func TestReloadClientsFiltersOAuthProvidersWithoutRescan(t *testing.T) {
	tmp := t.TempDir()
	w := &Watcher{
		authDir: tmp,
		config:  &config.Config{AuthDir: tmp},
		currentAuths: map[string]*coreauth.Auth{
			"a": {ID: "a", Provider: "Match"},
			"b": {ID: "b", Provider: "other"},
		},
		lastAuthHashes: map[string]string{"cached": "hash"},
	}

	w.reloadClients(false, []string{"match"}, false)

	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	if _, ok := w.currentAuths["a"]; ok {
		t.Fatal("expected filtered provider to be removed")
	}
	if len(w.lastAuthHashes) != 1 {
		t.Fatalf("expected existing hash cache to be retained, got %d", len(w.lastAuthHashes))
	}
}

func TestScheduleProcessEventsStopsOnContextDone(t *testing.T) {
	w := &Watcher{
		watcher: &fsnotify.Watcher{
			Events: make(chan fsnotify.Event, 1),
			Errors: make(chan error, 1),
		},
		configPath: "config.yaml",
		authDir:    "auth",
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.processEvents(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("processEvents did not exit on context cancel")
	}
}

func hexString(data []byte) string {
	return strings.ToLower(fmt.Sprintf("%x", data))
}
