// events.go implements fsnotify event handling for config and auth file changes.
// It normalizes paths, debounces noisy events, and triggers reload/update logic.
package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
	log "github.com/sirupsen/logrus"
)

func matchProvider(provider string, targets []string) (string, bool) {
	p := strings.ToLower(strings.TrimSpace(provider))
	for _, t := range targets {
		if strings.EqualFold(p, strings.TrimSpace(t)) {
			return p, true
		}
	}
	return p, false
}

func (w *Watcher) start(ctx context.Context) error {
	if errAddConfig := w.watcher.Add(w.configPath); errAddConfig != nil {
		log.Errorf("failed to watch config file %s: %v", w.configPath, errAddConfig)
		return errAddConfig
	}
	log.Debugf("watching config file: %s", w.configPath)

	if errAddAuthDir := w.watchAuthDirectoryTree(w.authDir); errAddAuthDir != nil {
		log.Errorf("failed to watch auth directory %s: %v", w.authDir, errAddAuthDir)
		return errAddAuthDir
	}
	log.Debugf("watching auth directory tree: %s", w.authDir)

	go w.processEvents(ctx)

	w.reloadClients(true, nil, false)
	return nil
}

func (w *Watcher) processEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case errWatch, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Errorf("file watcher error: %v", errWatch)
		}
	}
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	// Filter only relevant events: config, auth directories, or auth JSON files.
	configOps := fsnotify.Write | fsnotify.Create | fsnotify.Rename
	normalizedName := w.normalizeAuthPath(event.Name)
	normalizedConfigPath := w.normalizeAuthPath(w.configPath)
	normalizedAuthDir := w.normalizeAuthPath(w.authDir)
	isConfigEvent := normalizedName == normalizedConfigPath && event.Op&configOps != 0
	authOps := fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename
	// Keep root-level JSON events eligible for Watcher literals used by incremental
	// update paths; nested paths must be backed by a successfully installed watch.
	parentIsAuthRoot := w.normalizeWatchPath(filepath.Dir(normalizedName)) == w.normalizeWatchPath(normalizedAuthDir)
	parentWatched := parentIsAuthRoot || w.isWatchedAuthDirectory(filepath.Dir(normalizedName))
	isAuthJSON := parentWatched && strings.HasSuffix(strings.ToLower(normalizedName), ".json") && event.Op&authOps != 0
	info, errStat := os.Lstat(event.Name)
	isDirectoryCreate := errStat == nil && info.IsDir() && event.Op&(fsnotify.Create|fsnotify.Rename) != 0
	isWatchedDirectoryRemoval := w.isWatchedAuthDirectory(normalizedName) && event.Op&(fsnotify.Remove|fsnotify.Rename) != 0
	isAuthDirEvent := (isDirectoryCreate || isWatchedDirectoryRemoval) &&
		!isPrivateAuthPath(normalizedAuthDir, normalizedName) && (parentWatched || w.isWatchedAuthDirectory(normalizedName))
	if !isConfigEvent && !isAuthJSON && !isAuthDirEvent {
		// Ignore unrelated files (e.g., cookie snapshots *.cookie) and other noise.
		return
	}

	now := time.Now()
	log.Debugf("file system event detected: %s %s", event.Op.String(), event.Name)

	// Handle config file changes
	if isConfigEvent {
		log.Debugf("config file change details - operation: %s, timestamp: %s", event.Op.String(), now.Format("2006-01-02 15:04:05.000"))
		w.scheduleConfigReload()
		return
	}
	if isAuthDirEvent {
		w.authRescanMu.Lock()
		// A removed directory can already have been recreated when its queued event
		// reaches us. Retire the old inode's watches before registering that path again.
		if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 && w.isWatchedAuthDirectory(normalizedName) {
			w.removeAuthDirectoryTree(normalizedName)
		}
		if info, errStat := os.Lstat(event.Name); errStat == nil && info.IsDir() {
			if errWatch := w.watchAuthDirectoryTree(normalizedName); errWatch != nil {
				log.WithError(errWatch).Warnf("failed to watch auth directory %s", event.Name)
			}
		}
		w.authRescanMu.Unlock()
		// Directory events may include a tree moved into the auth directory.
		// Rescanning discovers its files after the new subdirectories are watched.
		w.reloadClients(true, nil, false)
		return
	}

	// Handle auth directory changes incrementally (.json only)
	w.authRescanMu.Lock()
	defer w.authRescanMu.Unlock()
	w.observeAuthFile(event.Name)

	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		if w.shouldDebounceRemove(normalizedName, now) {
			log.Debugf("debouncing remove event for %s", filepath.Base(event.Name))
			return
		}
		// Atomic replace on some platforms may surface as Rename (or Remove) before the new file is ready.
		// Wait briefly; if the path exists again, treat as an update instead of removal.
		time.Sleep(replaceCheckDelay)
		statSucceeded := false
		if _, statErr := os.Stat(event.Name); statErr == nil {
			statSucceeded = true
		} else if w.isKnownAuthFile(event.Name) {
			// On macOS (and under disk I/O pressure), atomic replace (e.g. os.Rename from temp file)
			// can trigger a NOTE_DELETE before the target inode is fully visible. Retry briefly
			// before treating a known auth file as permanently removed.
			for attempt := 0; attempt < 3; attempt++ {
				time.Sleep(25 * time.Millisecond)
				if _, retryErr := os.Stat(event.Name); retryErr == nil {
					statSucceeded = true
					break
				}
			}
		}
		if statSucceeded {
			if unchanged, errSame := w.authFileUnchanged(event.Name); errSame == nil && unchanged {
				log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(event.Name))
				return
			}
			log.Infof("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(event.Name))
			w.addOrUpdateClientLocked(event.Name)
			return
		}
		if !w.isKnownAuthFile(event.Name) {
			log.Debugf("ignoring remove for unknown auth file: %s", filepath.Base(event.Name))
			return
		}
		log.Infof("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(event.Name))
		w.removeClientLocked(event.Name)
		return
	}
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		if unchanged, errSame := w.authFileUnchanged(event.Name); errSame == nil && unchanged {
			log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(event.Name))
			return
		}
		log.Infof("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(event.Name))
		w.addOrUpdateClientLocked(event.Name)
	}
}

// observeAuthFile invalidates in-flight scans even if hash or content deduplication
// suppresses an update. It must not invalidate an otherwise valid queued auth event.
func (w *Watcher) observeAuthFile(path string) {
	normalized := w.normalizeAuthPath(path)
	if normalized == "" {
		return
	}
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	if w.fileObservations == nil {
		w.fileObservations = make(map[string]uint64)
	}
	w.fileObservations[normalized]++
	if w.activeAuthScans > 0 {
		// A reload may cache a hash before publishing its auth. Force the event
		// to parse it even if the scan finishes first, retaining the known path.
		if _, known := w.lastAuthHashes[normalized]; known {
			w.lastAuthHashes[normalized] = ""
		}
	}
}

func (w *Watcher) authFileUnchanged(path string) (bool, error) {
	data, errRead := authpublication.ReadFile(path)
	if errRead != nil {
		return false, errRead
	}
	if len(data) == 0 {
		return false, nil
	}
	sum := sha256.Sum256(data)
	curHash := hex.EncodeToString(sum[:])

	normalized := w.normalizeAuthPath(path)
	w.clientsMutex.RLock()
	prevHash, ok := w.lastAuthHashes[normalized]
	w.clientsMutex.RUnlock()
	if ok && prevHash == curHash {
		return true, nil
	}
	return false, nil
}

func (w *Watcher) isKnownAuthFile(path string) bool {
	normalized := w.normalizeAuthPath(path)
	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	_, ok := w.lastAuthHashes[normalized]
	return ok
}

func (w *Watcher) normalizeAuthPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	if runtime.GOOS == "windows" {
		cleaned = strings.TrimPrefix(cleaned, `\\?\`)
		cleaned = strings.ToLower(cleaned)
	}
	return cleaned
}

func (w *Watcher) shouldDebounceRemove(normalizedPath string, now time.Time) bool {
	if normalizedPath == "" {
		return false
	}
	w.clientsMutex.Lock()
	if w.lastRemoveTimes == nil {
		w.lastRemoveTimes = make(map[string]time.Time)
	}
	if last, ok := w.lastRemoveTimes[normalizedPath]; ok {
		if now.Sub(last) < authRemoveDebounceWindow {
			w.clientsMutex.Unlock()
			return true
		}
	}
	w.lastRemoveTimes[normalizedPath] = now
	if len(w.lastRemoveTimes) > 128 {
		cutoff := now.Add(-2 * authRemoveDebounceWindow)
		for p, t := range w.lastRemoveTimes {
			if t.Before(cutoff) {
				delete(w.lastRemoveTimes, p)
			}
		}
	}
	w.clientsMutex.Unlock()
	return false
}
