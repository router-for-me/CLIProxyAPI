package auth

import (
	"path/filepath"
	"runtime"
	"strings"
)

// NormalizeFileAuthID returns the canonical auth ID for a file-backed auth entry.
func NormalizeFileAuthID(path, baseDir string) string {
	return normalizeFileAuthIDForOS(path, baseDir, runtime.GOOS)
}

func normalizeFileAuthIDForOS(path, baseDir, goos string) string {
	id := strings.TrimSpace(path)
	baseDir = strings.TrimSpace(baseDir)
	if id == "" {
		return ""
	}
	if baseDir != "" {
		if rel, errRel := filepath.Rel(baseDir, id); errRel == nil && rel != "" {
			id = rel
		}
	}
	if goos == "windows" {
		id = strings.ToLower(id)
	}
	return id
}
