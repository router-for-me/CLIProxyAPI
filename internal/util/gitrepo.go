package util

import (
	"os"
	"path/filepath"
	"strings"
)

// FindGitRepoRoot walks upward from the provided path looking for a ".git"
// directory or file and returns the matching repository root.
func FindGitRepoRoot(path string) (string, bool) {
	start := strings.TrimSpace(path)
	if start == "" {
		return "", false
	}
	start = filepath.Clean(start)
	if info, err := os.Stat(start); err == nil && info != nil && !info.IsDir() {
		start = filepath.Dir(start)
	}

	dir := start
	for {
		if dir == "" || dir == "." {
			return "", false
		}
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
