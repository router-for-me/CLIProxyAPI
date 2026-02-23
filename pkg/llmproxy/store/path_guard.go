package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ensurePathWithinDir(path, baseDir, scope string) (string, error) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return "", fmt.Errorf("%s: path is empty", scope)
	}
	trimmedBase := strings.TrimSpace(baseDir)
	if trimmedBase == "" {
		return "", fmt.Errorf("%s: base directory is not configured", scope)
	}

	absBase, err := filepath.Abs(trimmedBase)
	if err != nil {
		return "", fmt.Errorf("%s: resolve base directory: %w", scope, err)
	}
	absPath, err := filepath.Abs(trimmedPath)
	if err != nil {
		return "", fmt.Errorf("%s: resolve path: %w", scope, err)
	}
	cleanBase := filepath.Clean(absBase)
	cleanPath := filepath.Clean(absPath)

	rel, err := filepath.Rel(cleanBase, cleanPath)
	if err != nil {
		return "", fmt.Errorf("%s: compute relative path: %w", scope, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%s: path escapes managed directory", scope)
	}
	return cleanPath, nil
}
