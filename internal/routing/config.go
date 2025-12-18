package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	log "github.com/sirupsen/logrus"
)

const (
	configFileName     = "config.json"
	configDirName      = ".korproxy"
	watchDebounceDelay = 100 * time.Millisecond
)

var (
	globalConfig   *RoutingConfig
	globalConfigMu sync.RWMutex
	configWatcher  *fsnotify.Watcher
	watcherOnce    sync.Once
	configPath     string
)

// GetConfigPath returns the path to the routing config file.
// Cross-platform: ~/.korproxy/config.json
func GetConfigPath() string {
	if configPath != "" {
		return configPath
	}

	var baseDir string
	switch runtime.GOOS {
	case "windows":
		baseDir = os.Getenv("LOCALAPPDATA")
		if baseDir == "" {
			baseDir = os.Getenv("APPDATA")
		}
		if baseDir == "" {
			baseDir = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
		}
	default:
		baseDir = os.Getenv("HOME")
		if baseDir == "" {
			baseDir = "~"
		}
	}

	configPath = filepath.Join(baseDir, configDirName, configFileName)
	return configPath
}

// SetConfigPath allows overriding the config path for testing.
func SetConfigPath(path string) {
	configPath = path
}

// LoadConfig reads the routing configuration from disk.
// Returns default config if file doesn't exist.
func LoadConfig() (*RoutingConfig, error) {
	path := GetConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultRoutingConfig()
			globalConfigMu.Lock()
			globalConfig = cfg
			globalConfigMu.Unlock()
			return cfg, nil
		}
		return nil, err
	}

	var cfg RoutingConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.WithError(err).Warn("Failed to parse routing config, using defaults")
		cfg := DefaultRoutingConfig()
		globalConfigMu.Lock()
		globalConfig = cfg
		globalConfigMu.Unlock()
		return cfg, nil
	}

	if err := cfg.Validate(); err != nil {
		log.WithError(err).Warn("Invalid routing config, using defaults")
		cfg := DefaultRoutingConfig()
		globalConfigMu.Lock()
		globalConfig = cfg
		globalConfigMu.Unlock()
		return cfg, nil
	}

	globalConfigMu.Lock()
	globalConfig = &cfg
	globalConfigMu.Unlock()

	return &cfg, nil
}

// GetConfig returns the current routing configuration.
// Thread-safe; loads from disk if not yet loaded.
func GetConfig() *RoutingConfig {
	globalConfigMu.RLock()
	cfg := globalConfig
	globalConfigMu.RUnlock()

	if cfg != nil {
		return cfg
	}

	cfg, err := LoadConfig()
	if err != nil {
		log.WithError(err).Error("Failed to load routing config")
		return DefaultRoutingConfig()
	}
	return cfg
}

// ConfigChangeCallback is called when the config file changes.
type ConfigChangeCallback func(*RoutingConfig)

var configCallbacks []ConfigChangeCallback
var callbacksMu sync.Mutex

// OnConfigChange registers a callback for config file changes.
func OnConfigChange(callback ConfigChangeCallback) {
	callbacksMu.Lock()
	configCallbacks = append(configCallbacks, callback)
	callbacksMu.Unlock()
}

// notifyCallbacks sends the new config to all registered callbacks.
func notifyCallbacks(cfg *RoutingConfig) {
	callbacksMu.Lock()
	callbacks := make([]ConfigChangeCallback, len(configCallbacks))
	copy(callbacks, configCallbacks)
	callbacksMu.Unlock()

	for _, cb := range callbacks {
		if cb != nil {
			cb(cfg)
		}
	}
}

// WatchConfig starts watching the config file for changes.
// Call StopWatching to clean up resources.
func WatchConfig() error {
	var err error
	watcherOnce.Do(func() {
		err = startWatcher()
	})
	return err
}

func startWatcher() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	configWatcher = watcher
	configDir := filepath.Dir(GetConfigPath())

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}

	if err := watcher.Add(configDir); err != nil {
		watcher.Close()
		return err
	}

	go watchLoop(watcher)
	log.WithField("path", configDir).Info("Started watching routing config directory")
	return nil
}

func watchLoop(watcher *fsnotify.Watcher) {
	var debounceTimer *time.Timer
	expectedFile := filepath.Base(GetConfigPath())

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			if filepath.Base(event.Name) != expectedFile {
				continue
			}

			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(watchDebounceDelay, func() {
				reloadConfig()
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.WithError(err).Error("Config watcher error")
		}
	}
}

func reloadConfig() {
	cfg, err := LoadConfig()
	if err != nil {
		log.WithError(err).Error("Failed to reload routing config")
		return
	}
	log.Info("Routing config reloaded")
	notifyCallbacks(cfg)
}

// StopWatching stops the config file watcher.
func StopWatching() {
	if configWatcher != nil {
		configWatcher.Close()
		configWatcher = nil
	}
}

// SaveConfig writes the routing configuration to disk.
// Ensures the directory exists and performs atomic write.
func SaveConfig(cfg *RoutingConfig) error {
	path := GetConfigPath()
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}

	globalConfigMu.Lock()
	globalConfig = cfg
	globalConfigMu.Unlock()

	return nil
}
