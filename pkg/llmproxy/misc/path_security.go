package misc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveSafeFilePath validates and normalizes a file path, rejecting path traversal components.
func ResolveSafeFilePath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("path is empty")
	}
	if hasPathTraversalComponent(trimmed) {
		return "", fmt.Errorf("path traversal is not allowed")
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." {
		return "", fmt.Errorf("path is invalid")
	}
	return cleaned, nil
}

// ResolveSafeFilePathInDir resolves a file name inside baseDir and rejects paths that escape baseDir.
func ResolveSafeFilePathInDir(baseDir, fileName string) (string, error) {
	base := strings.TrimSpace(baseDir)
	if base == "" {
		return "", fmt.Errorf("base directory is empty")
	}
	name := strings.TrimSpace(fileName)
	if name == "" {
		return "", fmt.Errorf("file name is empty")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", fmt.Errorf("file name must not contain path separators")
	}
	if hasPathTraversalComponent(name) {
		return "", fmt.Errorf("file name must not contain traversal components")
	}
	cleanName := filepath.Clean(name)
	if cleanName == "." || cleanName == ".." {
		return "", fmt.Errorf("file name is invalid")
	}
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve base directory: %w", err)
	}
	resolved := filepath.Clean(filepath.Join(baseAbs, cleanName))
	rel, err := filepath.Rel(baseAbs, resolved)
	if err != nil {
		return "", fmt.Errorf("resolve relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("resolved path escapes base directory")
	}
	return resolved, nil
}

func hasPathTraversalComponent(path string) bool {
	normalized := strings.ReplaceAll(path, "\\", "/")
	for _, component := range strings.Split(normalized, "/") {
		if component == ".." {
			return true
		}
	}
	return false
}
