package watcher

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

func (w *Watcher) watchAuthDirectoryTree(root string) error {
	return w.watchAuthDirectory(root, root, true)
}

func (w *Watcher) watchAuthDirectory(path, root string, required bool) error {
	normalized := w.normalizeWatchPath(path)
	root = w.normalizeWatchPath(root)
	if isPrivateAuthPath(root, normalized) {
		return nil
	}

	w.authWatchMu.Lock()
	if w.watchedAuthDirs == nil {
		w.watchedAuthDirs = make(map[string]struct{})
	}
	if _, exists := w.watchedAuthDirs[normalized]; exists {
		w.authWatchMu.Unlock()
		return nil
	}
	if errAdd := w.watcher.Add(path); errAdd != nil {
		w.authWatchMu.Unlock()
		if required {
			return errAdd
		}
		log.WithError(errAdd).Warnf("failed to watch nested auth directory %s", path)
		return nil
	}
	w.watchedAuthDirs[normalized] = struct{}{}
	w.authWatchMu.Unlock()

	entries, errRead := os.ReadDir(path)
	if errRead != nil {
		log.WithError(errRead).Warnf("failed to scan nested auth directory %s for watches", path)
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		childPath := filepath.Join(path, entry.Name())
		if err := w.watchAuthDirectory(childPath, root, false); err != nil {
			return fmt.Errorf("watch auth directory %s: %w", childPath, err)
		}
	}
	return nil
}

func isPrivateAuthPath(root, path string) bool {
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return true
	}
	if relative == "." {
		return false
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == ".git" || part == ".auth-publications" || strings.HasPrefix(part, ".auth-acquisition-") {
			return true
		}
	}
	return false
}

func (w *Watcher) isWatchedAuthDirectory(path string) bool {
	path = w.normalizeWatchPath(path)
	w.authWatchMu.RLock()
	_, exists := w.watchedAuthDirs[path]
	w.authWatchMu.RUnlock()
	return exists
}

func (w *Watcher) removeAuthDirectoryTree(path string) {
	path = w.normalizeWatchPath(path)
	w.authWatchMu.Lock()
	defer w.authWatchMu.Unlock()
	for candidate := range w.watchedAuthDirs {
		relative, err := filepath.Rel(path, candidate)
		if err != nil || (relative != "." && (relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
			continue
		}
		if err := w.watcher.Remove(candidate); err != nil {
			log.WithError(err).Debugf("failed to remove auth directory watch %s", candidate)
		}
		delete(w.watchedAuthDirs, candidate)
	}
}

func (w *Watcher) normalizeWatchPath(path string) string {
	path = w.normalizeAuthPath(path)
	absolute, err := filepath.Abs(path)
	if err == nil {
		return w.normalizeAuthPath(absolute)
	}
	return path
}
