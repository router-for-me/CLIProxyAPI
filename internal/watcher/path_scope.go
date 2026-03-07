package watcher

import (
	"path/filepath"
	"runtime"
	"strings"
)

// pathBelongsToDir reports whether path resolves to dir or one of its descendants.
func pathBelongsToDir(path, dir string) bool {
	normalizedPath, okPath := normalizeAbsolutePath(path)
	if !okPath {
		return false
	}
	normalizedDir, okDir := normalizeAbsolutePath(dir)
	if !okDir {
		return false
	}

	relPath, errRel := filepath.Rel(normalizedDir, normalizedPath)
	if errRel != nil {
		return false
	}
	relPath = filepath.Clean(relPath)
	if relPath == "." {
		return true
	}

	parentPrefix := ".." + string(filepath.Separator)
	return relPath != ".." && !strings.HasPrefix(relPath, parentPrefix)
}

func normalizeAbsolutePath(path string) (string, bool) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", false
	}
	if runtime.GOOS == "windows" {
		trimmed = strings.TrimPrefix(trimmed, `\\?\`)
	}

	normalizedPath := trimmed
	if resolvedPath, errEval := filepath.EvalSymlinks(trimmed); errEval == nil {
		normalizedPath = resolvedPath
	}

	absolutePath, errAbs := filepath.Abs(normalizedPath)
	if errAbs != nil {
		return "", false
	}
	cleaned := filepath.Clean(absolutePath)
	if runtime.GOOS == "windows" {
		cleaned = strings.TrimPrefix(cleaned, `\\?\`)
		cleaned = strings.ToLower(cleaned)
	}
	return cleaned, true
}
