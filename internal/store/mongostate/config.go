package mongostate

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	ini "gopkg.in/ini.v1"
)

const ConfigFileName = "state-store.local.ini"

// RuntimeConfig captures runtime state persistence settings loaded from a config-specific state-store INI file.
type RuntimeConfig struct {
	Enabled                 bool
	URI                     string
	Database                string
	SnapshotCollection      string
	ConnectTimeoutSeconds   int
	OperationTimeoutSeconds int
	FlushIntervalSeconds    int
}

// DefaultRuntimeConfig returns the default MongoDB runtime state config.
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Database:                "cliproxy_state",
		SnapshotCollection:      "service_state_snapshots",
		ConnectTimeoutSeconds:   10,
		OperationTimeoutSeconds: 5,
		FlushIntervalSeconds:    30,
	}
}

// ResolveConfigPath resolves the runtime state config path next to the main YAML config file.
func ResolveConfigPath(configFilePath string) string {
	paths := ResolveConfigPaths(configFilePath)
	if len(paths) == 0 {
		return ConfigFileName
	}
	return paths[0]
}

func resolveConfigDir(configDir string) string {
	trimmed := strings.TrimSpace(configDir)
	if trimmed == "" {
		if wd, err := os.Getwd(); err == nil {
			trimmed = wd
		}
	}
	return trimmed
}

// ResolveConfigPaths resolves config-specific state-store INI candidates.
// Current project mapping:
//   - config.yaml => state-store.local.ini
//   - config-277.yaml => state-store.277.ini
//   - other config-<name>.yaml => state-store.<name>.ini
func ResolveConfigPaths(configFilePath string) []string {
	trimmed := strings.TrimSpace(configFilePath)
	dir := resolveConfigDir(filepath.Dir(trimmed))
	base := filepath.Base(trimmed)

	candidates := make([]string, 0, 1)
	addCandidate := func(name string) {
		if strings.TrimSpace(name) == "" {
			return
		}
		path := filepath.Join(dir, name)
		for _, existing := range candidates {
			if existing == path {
				return
			}
		}
		candidates = append(candidates, path)
	}

	switch {
	case base == "config.yaml":
		addCandidate("state-store.local.ini")
	case strings.HasPrefix(base, "config-") && strings.HasSuffix(base, ".yaml"):
		suffix := strings.TrimSuffix(strings.TrimPrefix(base, "config-"), ".yaml")
		if suffix != "" {
			addCandidate("state-store." + suffix + ".ini")
		}
	default:
		addCandidate(ConfigFileName)
	}

	if len(candidates) == 0 {
		addCandidate(ConfigFileName)
	}
	return candidates
}

// LoadRuntimeConfig loads runtime state persistence settings from the config-specific INI file.
// It returns found=false when the file does not exist.
func LoadRuntimeConfig(configFilePath string) (RuntimeConfig, string, bool, error) {
	paths := ResolveConfigPaths(configFilePath)
	if len(paths) == 0 {
		paths = []string{ConfigFileName}
	}

	lastPath := paths[0]
	path := paths[0]
	lastPath = path
	cfg, found, err := loadRuntimeConfigByPath(path)
	if err != nil {
		return cfg, path, found, err
	}
	if found {
		return cfg, path, true, nil
	}

	return DefaultRuntimeConfig(), lastPath, false, nil
}

func loadRuntimeConfigByPath(path string) (RuntimeConfig, bool, error) {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	cfg := DefaultRuntimeConfig()
	if cleanPath == "." || cleanPath == "" {
		return cfg, false, nil
	}

	data, err := os.ReadFile(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, false, nil
		}
		return cfg, false, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return cfg, true, fmt.Errorf("state-store config %s is empty", cleanPath)
	}

	iniCfg, err := ini.LoadSources(ini.LoadOptions{
		Insensitive:         true,
		IgnoreInlineComment: true,
	}, cleanPath)
	if err != nil {
		return cfg, true, err
	}

	mongoSec := iniCfg.Section("mongo")
	cfg.Enabled = mongoSec.Key("enabled").MustBool(cfg.Enabled)
	cfg.URI = mongoSec.Key("uri").MustString(cfg.URI)
	cfg.Database = mongoSec.Key("database").MustString(cfg.Database)
	cfg.SnapshotCollection = mongoSec.Key("snapshot_collection").MustString(cfg.SnapshotCollection)
	cfg.ConnectTimeoutSeconds = mongoSec.Key("connect_timeout_seconds").MustInt(cfg.ConnectTimeoutSeconds)
	cfg.OperationTimeoutSeconds = mongoSec.Key("operation_timeout_seconds").MustInt(cfg.OperationTimeoutSeconds)
	cfg.FlushIntervalSeconds = mongoSec.Key("flush_interval_seconds").MustInt(cfg.FlushIntervalSeconds)
	cfg.Normalize()

	return cfg, true, nil
}

// ApplyEnvOverrides overlays MONGOSTATE_* environment variables onto the runtime config.
func ApplyEnvOverrides(cfg *RuntimeConfig) {
	if cfg == nil {
		return
	}

	if v := strings.TrimSpace(os.Getenv("MONGOSTATE_ENABLED")); v != "" {
		cfg.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := strings.TrimSpace(os.Getenv("MONGOSTATE_URI")); v != "" {
		cfg.URI = v
	}
	if v := strings.TrimSpace(os.Getenv("MONGOSTATE_DATABASE")); v != "" {
		cfg.Database = v
	}
	if v := strings.TrimSpace(os.Getenv("MONGOSTATE_SNAPSHOT_COLLECTION")); v != "" {
		cfg.SnapshotCollection = v
	}
	if v := strings.TrimSpace(os.Getenv("MONGOSTATE_CONNECT_TIMEOUT_SECONDS")); v != "" {
		if sec, err := strconv.Atoi(v); err == nil {
			cfg.ConnectTimeoutSeconds = sec
		}
	}
	if v := strings.TrimSpace(os.Getenv("MONGOSTATE_OPERATION_TIMEOUT_SECONDS")); v != "" {
		if sec, err := strconv.Atoi(v); err == nil {
			cfg.OperationTimeoutSeconds = sec
		}
	}
	if v := strings.TrimSpace(os.Getenv("MONGOSTATE_FLUSH_INTERVAL_SECONDS")); v != "" {
		if sec, err := strconv.Atoi(v); err == nil {
			cfg.FlushIntervalSeconds = sec
		}
	}

	cfg.Normalize()
}

// Normalize applies defaults and trims string fields.
func (c *RuntimeConfig) Normalize() {
	if c == nil {
		return
	}

	c.URI = strings.TrimSpace(c.URI)
	c.Database = strings.TrimSpace(c.Database)
	c.SnapshotCollection = strings.TrimSpace(c.SnapshotCollection)

	if c.Database == "" {
		c.Database = "cliproxy_state"
	}
	if c.SnapshotCollection == "" {
		c.SnapshotCollection = "service_state_snapshots"
	}
	if c.ConnectTimeoutSeconds <= 0 {
		c.ConnectTimeoutSeconds = 10
	}
	if c.OperationTimeoutSeconds <= 0 {
		c.OperationTimeoutSeconds = 5
	}
	if c.FlushIntervalSeconds <= 0 {
		c.FlushIntervalSeconds = 30
	}
}

// ToStoreConfig converts the runtime config to the Mongo store config used by the manager.
func (c RuntimeConfig) ToStoreConfig(instanceID string) StoreConfig {
	return NewStoreConfig(
		c.URI,
		c.Database,
		c.SnapshotCollection,
		c.ConnectTimeoutSeconds,
		c.OperationTimeoutSeconds,
		c.FlushIntervalSeconds,
		instanceID,
	)
}
