package watcher

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
)

var walkAuthDirTree = filepath.WalkDir

func (w *Watcher) watchAuthTree(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	normalizedRoot := w.normalizeAuthPath(root)

	return walkAuthDirTree(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if w.normalizeAuthPath(path) == normalizedRoot {
				return err
			}
			log.Warnf("skipping unreadable auth subdirectory %s: %v", path, err)
			return filepath.SkipDir
		}
		if d == nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if w.isWatchedAuthDir(path) {
			return nil
		}
		if errAdd := w.watcher.Add(path); errAdd != nil {
			if w.normalizeAuthPath(path) == normalizedRoot {
				return errAdd
			}
			log.Warnf("skipping auth subdirectory %s after watcher add failed: %v", path, errAdd)
			return filepath.SkipDir
		}
		w.trackWatchedAuthDir(path)
		log.Debugf("watching auth directory: %s", path)
		return nil
	})
}

func (w *Watcher) syncAuthSubtree(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	normalizedRoot := w.normalizeAuthPath(root)

	return walkAuthDirTree(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if w.normalizeAuthPath(path) == normalizedRoot {
				return err
			}
			log.Warnf("skipping unreadable auth subtree path %s: %v", path, err)
			return filepath.SkipDir
		}
		if d == nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			w.addOrUpdateClient(path)
		}
		return nil
	})
}

func (w *Watcher) removeAuthSubtree(root string) {
	normalizedRoot := w.normalizeAuthPath(root)
	if normalizedRoot == "" {
		return
	}

	watchedDirs := make([]string, 0)
	authPaths := make([]string, 0)

	w.clientsMutex.RLock()
	for normalizedDir, actualDir := range w.watchedAuthDirs {
		if w.pathWithinBase(normalizedDir, normalizedRoot) {
			watchedDirs = append(watchedDirs, actualDir)
		}
	}
	for normalizedPath := range w.lastAuthHashes {
		if w.pathWithinBase(normalizedPath, normalizedRoot) {
			authPaths = append(authPaths, normalizedPath)
		}
	}
	w.clientsMutex.RUnlock()

	sort.Slice(watchedDirs, func(i, j int) bool {
		return len(watchedDirs[i]) > len(watchedDirs[j])
	})
	for _, dir := range watchedDirs {
		w.unwatchAuthDir(dir)
	}

	sort.Strings(authPaths)
	for _, path := range authPaths {
		w.removeClient(path)
	}
}

func (w *Watcher) pathWithinBase(path, base string) bool {
	if path == "" || base == "" {
		return false
	}
	if path == base {
		return true
	}
	withSep := base
	if !strings.HasSuffix(withSep, string(filepath.Separator)) {
		withSep += string(filepath.Separator)
	}
	return strings.HasPrefix(path, withSep)
}

func (w *Watcher) isWatchedAuthDir(path string) bool {
	normalized := w.normalizeAuthPath(path)
	if normalized == "" {
		return false
	}
	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	_, ok := w.watchedAuthDirs[normalized]
	return ok
}

func (w *Watcher) trackWatchedAuthDir(path string) {
	normalized := w.normalizeAuthPath(path)
	if normalized == "" {
		return
	}
	w.clientsMutex.Lock()
	if w.watchedAuthDirs == nil {
		w.watchedAuthDirs = make(map[string]string)
	}
	w.watchedAuthDirs[normalized] = path
	w.clientsMutex.Unlock()
}

func (w *Watcher) unwatchAuthDir(path string) {
	normalized := w.normalizeAuthPath(path)
	if normalized == "" {
		return
	}

	actualPath := path
	w.clientsMutex.Lock()
	if w.watchedAuthDirs != nil {
		if storedPath, ok := w.watchedAuthDirs[normalized]; ok {
			actualPath = storedPath
			delete(w.watchedAuthDirs, normalized)
		}
	}
	w.clientsMutex.Unlock()

	if errRemove := w.watcher.Remove(actualPath); errRemove != nil {
		log.Debugf("failed to unwatch auth directory %s: %v", actualPath, errRemove)
	}
}
