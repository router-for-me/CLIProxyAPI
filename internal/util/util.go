// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for logging configuration, file system operations,
// and other common utilities used throughout the application.
package util

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

var functionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.:-]`)

// SanitizeFunctionName ensures a function name matches the requirements for Gemini/Vertex AI.
// It replaces invalid characters with underscores, ensures it starts with a letter or underscore,
// and truncates it to 64 characters if necessary.
// Regex Rule: [^a-zA-Z0-9_.:-] replaced with _.
func SanitizeFunctionName(name string) string {
	if name == "" {
		return ""
	}

	// Replace invalid characters with underscore
	sanitized := functionNameSanitizer.ReplaceAllString(name, "_")

	// Ensure it starts with a letter or underscore
	// Re-reading requirements: Must start with a letter or an underscore.
	if len(sanitized) > 0 {
		first := sanitized[0]
		if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
			// If it starts with an allowed character but not allowed at the beginning (digit, dot, colon, dash),
			// we must prepend an underscore.

			// To stay within the 64-character limit while prepending, we must truncate first.
			if len(sanitized) >= 64 {
				sanitized = sanitized[:63]
			}
			sanitized = "_" + sanitized
		}
	} else {
		sanitized = "_"
	}

	// Truncate to 64 characters
	if len(sanitized) > 64 {
		sanitized = sanitized[:64]
	}
	return sanitized
}

// SetLogLevel configures the logrus log level based on the configuration.
// It sets the log level to DebugLevel if debug mode is enabled, otherwise to InfoLevel.
func SetLogLevel(cfg *config.Config) {
	currentLevel := log.GetLevel()
	var newLevel log.Level
	if cfg.Debug {
		newLevel = log.DebugLevel
	} else {
		newLevel = log.InfoLevel
	}

	if currentLevel != newLevel {
		log.SetLevel(newLevel)
		log.Infof("log level changed from %s to %s (debug=%t)", currentLevel, newLevel, cfg.Debug)
	}
}

// ResolveAuthDir normalizes the auth directory path for consistent reuse throughout the app.
// It expands a leading tilde (~) to the user's home directory and returns a cleaned path.
func ResolveAuthDir(authDir string) (string, error) {
	if authDir == "" {
		return "", nil
	}
	if strings.HasPrefix(authDir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve auth dir: %w", err)
		}
		remainder := strings.TrimPrefix(authDir, "~")
		remainder = strings.TrimLeft(remainder, "/\\")
		if remainder == "" {
			return filepath.Clean(home), nil
		}
		normalized := strings.ReplaceAll(remainder, "\\", "/")
		return filepath.Clean(filepath.Join(home, filepath.FromSlash(normalized))), nil
	}
	return filepath.Clean(authDir), nil
}

// CountAuthFiles returns the number of auth records available through the provided Store.
// For filesystem-backed stores, this reflects the number of JSON auth files under the configured directory.
func CountAuthFiles[T any](ctx context.Context, store interface {
	List(context.Context) ([]T, error)
}) int {
	if store == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	entries, err := store.List(ctx)
	if err != nil {
		log.Debugf("countAuthFiles: failed to list auth records: %v", err)
		return 0
	}
	return len(entries)
}

// WritablePath returns the cleaned WRITABLE_PATH environment variable when it is set.
// It accepts both uppercase and lowercase variants for compatibility with existing conventions.
func WritablePath() string {
	for _, key := range []string{"WRITABLE_PATH", "writable_path"} {
		if value, ok := os.LookupEnv(key); ok {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				return filepath.Clean(trimmed)
			}
		}
	}
	return ""
}
