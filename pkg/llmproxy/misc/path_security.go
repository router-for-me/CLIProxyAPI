package misc

import (
	"fmt"
<<<<<<< HEAD
	"net/url"
=======
>>>>>>> archive/pr-234-head-20260223
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
<<<<<<< HEAD
	normalized, err := normalizePathForTraversalCheck(trimmed)
	if err != nil {
		return "", fmt.Errorf("path contains invalid encoding: %w", err)
	}
	if hasPathTraversalComponent(normalized) {
		return "", fmt.Errorf("path traversal is not allowed")
	}
	cleaned := filepath.Clean(normalized)
=======
	if hasPathTraversalComponent(trimmed) {
		return "", fmt.Errorf("path traversal is not allowed")
	}
	cleaned := filepath.Clean(trimmed)
>>>>>>> archive/pr-234-head-20260223
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
<<<<<<< HEAD
	normalized, err := normalizePathForTraversalCheck(name)
	if err != nil {
		return "", fmt.Errorf("file name contains invalid encoding: %w", err)
	}
	if strings.ContainsAny(normalized, `/\`) {
		return "", fmt.Errorf("file name must not contain path separators")
	}
	if hasPathTraversalComponent(normalized) {
		return "", fmt.Errorf("file name must not contain traversal components")
	}
	cleanName := filepath.Clean(normalized)
=======
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", fmt.Errorf("file name must not contain path separators")
	}
	if hasPathTraversalComponent(name) {
		return "", fmt.Errorf("file name must not contain traversal components")
	}
	cleanName := filepath.Clean(name)
>>>>>>> archive/pr-234-head-20260223
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
<<<<<<< HEAD

func normalizePathForTraversalCheck(path string) (string, error) {
	normalized := path
	for i := 0; i < 4; i++ {
		decoded, err := url.PathUnescape(normalized)
		if err != nil {
			return "", err
		}
		if decoded == normalized {
			break
		}
		normalized = decoded
	}
	return strings.ReplaceAll(normalized, "\\", "/"), nil
}
=======
>>>>>>> archive/pr-234-head-20260223
