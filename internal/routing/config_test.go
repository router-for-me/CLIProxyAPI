package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestGetConfigPath(t *testing.T) {
	// Reset cached path
	configPath = ""
	
	path := GetConfigPath()
	if path == "" {
		t.Error("GetConfigPath() returned empty string")
	}
	
	// Should contain .korproxy directory
	if !contains(path, ".korproxy") && !contains(path, "korproxy") {
		t.Errorf("GetConfigPath() = %q, should contain .korproxy or korproxy", path)
	}
	
	// Should end with config.json
	if filepath.Base(path) != "config.json" {
		t.Errorf("GetConfigPath() = %q, should end with config.json", path)
	}
}

func TestSetConfigPath(t *testing.T) {
	// Save and restore original
	original := configPath
	defer func() { configPath = original }()
	
	testPath := "/custom/path/config.json"
	SetConfigPath(testPath)
	
	if GetConfigPath() != testPath {
		t.Errorf("GetConfigPath() = %q, want %q", GetConfigPath(), testPath)
	}
}

func TestLoadConfig_FileNotExists(t *testing.T) {
	// Reset global state
	globalConfigMu.Lock()
	globalConfig = nil
	globalConfigMu.Unlock()
	
	// Use temp dir with non-existent file
	tempDir := t.TempDir()
	testPath := filepath.Join(tempDir, "nonexistent", "config.json")
	SetConfigPath(testPath)
	defer func() { configPath = "" }()
	
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	
	// Should return default config
	if cfg.Version != 1 {
		t.Errorf("LoadConfig().Version = %d, want 1", cfg.Version)
	}
	if len(cfg.ModelFamilies.Chat) == 0 {
		t.Error("LoadConfig() should have default model families")
	}
}

func TestLoadConfig_ValidFile(t *testing.T) {
	// Reset global state
	globalConfigMu.Lock()
	globalConfig = nil
	globalConfigMu.Unlock()
	
	tempDir := t.TempDir()
	testPath := filepath.Join(tempDir, "config.json")
	
	profileID := "test-profile"
	testConfig := &RoutingConfig{
		Version:         1,
		ActiveProfileID: &profileID,
		Profiles: []Profile{
			{ID: profileID, Name: "Test Profile", Color: "#FF0000"},
		},
		ProviderGroups: []ProviderGroup{
			{ID: "group1", Name: "Group 1", SelectionStrategy: SelectionRoundRobin},
		},
		ModelFamilies: ModelFamilies{
			Chat:       []string{"gpt-4*"},
			Completion: []string{"code-*"},
			Embedding:  []string{"text-embedding-*"},
		},
	}
	
	data, _ := json.MarshalIndent(testConfig, "", "  ")
	if err := os.WriteFile(testPath, data, 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}
	
	SetConfigPath(testPath)
	defer func() { configPath = "" }()
	
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	
	if cfg.ActiveProfileID == nil || *cfg.ActiveProfileID != profileID {
		t.Errorf("LoadConfig().ActiveProfileID = %v, want %q", cfg.ActiveProfileID, profileID)
	}
	if len(cfg.Profiles) != 1 {
		t.Errorf("LoadConfig().Profiles length = %d, want 1", len(cfg.Profiles))
	}
	if cfg.Profiles[0].Name != "Test Profile" {
		t.Errorf("LoadConfig().Profiles[0].Name = %q, want 'Test Profile'", cfg.Profiles[0].Name)
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	// Reset global state
	globalConfigMu.Lock()
	globalConfig = nil
	globalConfigMu.Unlock()
	
	tempDir := t.TempDir()
	testPath := filepath.Join(tempDir, "config.json")
	
	// Write invalid JSON
	if err := os.WriteFile(testPath, []byte("not valid json"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}
	
	SetConfigPath(testPath)
	defer func() { configPath = "" }()
	
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() should not error, got %v", err)
	}
	
	// Should return default config on invalid JSON
	if cfg.Version != 1 {
		t.Errorf("LoadConfig().Version = %d, want 1 (default)", cfg.Version)
	}
}

func TestGetConfig(t *testing.T) {
	// Reset global state
	globalConfigMu.Lock()
	globalConfig = nil
	globalConfigMu.Unlock()
	
	tempDir := t.TempDir()
	testPath := filepath.Join(tempDir, "config.json")
	SetConfigPath(testPath)
	defer func() { configPath = "" }()
	
	cfg := GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig() returned nil")
	}
	
	// Second call should return cached config
	cfg2 := GetConfig()
	if cfg != cfg2 {
		t.Error("GetConfig() should return cached config")
	}
}

func TestSaveConfig(t *testing.T) {
	tempDir := t.TempDir()
	testPath := filepath.Join(tempDir, "subdir", "config.json")
	SetConfigPath(testPath)
	defer func() { configPath = "" }()
	
	profileID := "saved-profile"
	cfg := &RoutingConfig{
		Version:         1,
		ActiveProfileID: &profileID,
		Profiles: []Profile{
			{ID: profileID, Name: "Saved Profile"},
		},
	}
	
	err := SaveConfig(cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	
	// Verify file was written
	data, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("Failed to read saved config: %v", err)
	}
	
	var loaded RoutingConfig
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Failed to parse saved config: %v", err)
	}
	
	if loaded.ActiveProfileID == nil || *loaded.ActiveProfileID != profileID {
		t.Errorf("Saved config ActiveProfileID = %v, want %q", loaded.ActiveProfileID, profileID)
	}
}

func TestSaveConfig_AtomicWrite(t *testing.T) {
	tempDir := t.TempDir()
	testPath := filepath.Join(tempDir, "config.json")
	SetConfigPath(testPath)
	defer func() { configPath = "" }()
	
	// Write initial config
	cfg1 := &RoutingConfig{Version: 1}
	if err := SaveConfig(cfg1); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	
	// Overwrite with new config
	profileID := "new-profile"
	cfg2 := &RoutingConfig{
		Version:         1,
		ActiveProfileID: &profileID,
	}
	if err := SaveConfig(cfg2); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	
	// Verify temp file doesn't exist (atomic write completed)
	tmpPath := testPath + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("Temp file should not exist after atomic write")
	}
	
	// Verify new content
	data, _ := os.ReadFile(testPath)
	var loaded RoutingConfig
	json.Unmarshal(data, &loaded)
	
	if loaded.ActiveProfileID == nil || *loaded.ActiveProfileID != profileID {
		t.Error("Atomic write should have new content")
	}
}

func TestOnConfigChange(t *testing.T) {
	// Reset callbacks
	callbacksMu.Lock()
	configCallbacks = nil
	callbacksMu.Unlock()
	
	called := make(chan *RoutingConfig, 1)
	OnConfigChange(func(cfg *RoutingConfig) {
		called <- cfg
	})
	
	// Trigger callback
	testCfg := &RoutingConfig{Version: 1}
	notifyCallbacks(testCfg)
	
	select {
	case received := <-called:
		if received != testCfg {
			t.Error("Callback received different config")
		}
	case <-time.After(time.Second):
		t.Error("Callback was not called")
	}
}

func TestConfigWatcher_Integration(t *testing.T) {
	// Reset watcher state
	if configWatcher != nil {
		configWatcher.Close()
		configWatcher = nil
	}
	watcherOnce = sync.Once{}
	
	// Reset callbacks
	callbacksMu.Lock()
	configCallbacks = nil
	callbacksMu.Unlock()
	
	tempDir := t.TempDir()
	testPath := filepath.Join(tempDir, "config.json")
	SetConfigPath(testPath)
	defer func() { 
		configPath = ""
		StopWatching()
	}()
	
	// Write initial config
	initialCfg := &RoutingConfig{Version: 1}
	data, _ := json.MarshalIndent(initialCfg, "", "  ")
	os.WriteFile(testPath, data, 0644)
	
	// Set up change callback
	changed := make(chan struct{}, 1)
	OnConfigChange(func(cfg *RoutingConfig) {
		changed <- struct{}{}
	})
	
	// Start watching
	if err := WatchConfig(); err != nil {
		t.Fatalf("WatchConfig() error = %v", err)
	}
	
	// Modify config
	time.Sleep(200 * time.Millisecond) // Let watcher initialize
	
	profileID := "modified"
	modifiedCfg := &RoutingConfig{
		Version:         1,
		ActiveProfileID: &profileID,
	}
	data, _ = json.MarshalIndent(modifiedCfg, "", "  ")
	os.WriteFile(testPath, data, 0644)
	
	// Wait for change notification (with timeout)
	select {
	case <-changed:
		// Success
	case <-time.After(2 * time.Second):
		t.Error("Config change was not detected within timeout")
	}
}

func TestConfigConcurrentAccess(t *testing.T) {
	// Reset global state
	globalConfigMu.Lock()
	globalConfig = nil
	globalConfigMu.Unlock()
	
	tempDir := t.TempDir()
	testPath := filepath.Join(tempDir, "config.json")
	SetConfigPath(testPath)
	defer func() { configPath = "" }()
	
	// Write test config
	cfg := &RoutingConfig{Version: 1}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(testPath, data, 0644)
	
	// Concurrent reads
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := GetConfig()
			if cfg == nil {
				t.Error("GetConfig returned nil during concurrent access")
			}
		}()
	}
	wg.Wait()
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || 
		len(s) > 0 && (s[0:len(substr)] == substr || 
		contains(s[1:], substr)))
}
