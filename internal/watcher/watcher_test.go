package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/diff"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/synthesizer"
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

func hexString(data []byte) string {
	return strings.ToLower(fmt.Sprintf("%x", data))
}
