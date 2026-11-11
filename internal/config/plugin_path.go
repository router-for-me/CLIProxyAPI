package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultPluginsDir = "plugins"

// ResolvePluginsDir normalizes the plugin directory for consistent use throughout the app.
// It expands a leading tilde (~) to the user's home directory and defaults empty values to plugins.
func ResolvePluginsDir(pluginsDir string) (string, error) {
	pluginsDir = strings.TrimSpace(pluginsDir)
	if pluginsDir == "" {
		pluginsDir = defaultPluginsDir
	}
	resolved, errResolve := resolveHomePath(pluginsDir)
	if errResolve != nil {
		return "", fmt.Errorf("resolve plugins directory: %w", errResolve)
	}
	return resolved, nil
}

// resolveHomePath expands a leading tilde (~) to the user's home directory and cleans the path.
func resolveHomePath(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return filepath.Clean(path), nil
	}
	homeDir, errUserHomeDir := os.UserHomeDir()
	if errUserHomeDir != nil {
		return "", errUserHomeDir
	}
	remainder := strings.TrimLeft(strings.TrimPrefix(path, "~"), "/\\")
	if remainder == "" {
		return filepath.Clean(homeDir), nil
	}
	normalized := strings.ReplaceAll(remainder, "\\", "/")
	return filepath.Clean(filepath.Join(homeDir, filepath.FromSlash(normalized))), nil
}

// ResolvePluginsDir resolves and stores the effective plugin directory.
func (cfg *Config) ResolvePluginsDir() error {
	if cfg == nil {
		return nil
	}
	pluginsDir, errResolvePluginsDir := ResolvePluginsDir(cfg.Plugins.Dir)
	if errResolvePluginsDir != nil {
		return errResolvePluginsDir
	}
	cfg.Plugins.Dir = pluginsDir
	return nil
}
