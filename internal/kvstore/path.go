package kvstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// ResolvePath normalizes the configured bbolt database file path into an absolute file path.
// Empty values default to config.DefaultKVStorePath ("kv.db") in the current working directory.
func ResolvePath(raw string) (string, error) {
	resolved := strings.TrimSpace(raw)
	if resolved == "" {
		resolved = config.DefaultKVStorePath
	}
	if strings.HasPrefix(resolved, "~") {
		homeDir, errUserHomeDir := os.UserHomeDir()
		if errUserHomeDir != nil {
			return "", fmt.Errorf("kvstore: resolve user home directory: %w", errUserHomeDir)
		}
		remainder := strings.TrimPrefix(resolved, "~")
		remainder = strings.TrimLeft(remainder, "/\\")
		if remainder == "" {
			resolved = filepath.Clean(homeDir)
		} else {
			normalized := strings.ReplaceAll(remainder, "\\", "/")
			resolved = filepath.Join(homeDir, filepath.FromSlash(normalized))
		}
	}
	resolved = filepath.Clean(resolved)
	if !filepath.IsAbs(resolved) {
		absPath, errAbs := filepath.Abs(resolved)
		if errAbs != nil {
			return "", fmt.Errorf("kvstore: resolve absolute path: %w", errAbs)
		}
		resolved = filepath.Clean(absPath)
	}
	return resolved, nil
}

// ResolvePathWithAuthDir resolves rawPath when non-empty, or falls back to
// "<auth-dir>/kv.db" (using config.DefaultAuthDir when authDir is empty).
func ResolvePathWithAuthDir(rawPath, authDir string) (string, error) {
	return ResolvePath((config.KVStoreConfig{Path: rawPath}).EffectivePathForAuthDir(authDir))
}
