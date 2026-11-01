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

func (w *Watcher) currentAuthDir() string {
	if w == nil {
		return ""
	}
	w.clientsMutex.RLock()
	authDir := w.authDir
	w.clientsMutex.RUnlock()
	return authDir
}

func sameDirectoryIdentity(a, b string) bool {
	infoA, errA := os.Stat(a)
	if errA != nil {
		return false
	}
	infoB, errB := os.Stat(b)
	if errB != nil {
		return false
	}
	return os.SameFile(infoA, infoB)
}

func (w *Watcher) matchesAuthDir(dir, targetAuthDir string) bool {
	return w.sameNormalizedAuthPath(dir, targetAuthDir) || sameDirectoryIdentity(dir, targetAuthDir)
}

func (w *Watcher) isCurrentAuthDir(dir string) bool {
	if w == nil {
		return false
	}
	w.clientsMutex.RLock()
	authDir := w.authDir
	watchedDir := w.watchedAuthDir
	w.clientsMutex.RUnlock()
	if w.matchesAuthDir(dir, authDir) {
		return true
	}
	if watchedDir != "" && w.matchesAuthDir(authDir, watchedDir) && w.matchesAuthDir(dir, watchedDir) {
		return true
	}
	return false
}

func (w *Watcher) rewatchAuthDir(nextAuthDir string) {
	if w == nil || w.watcher == nil || w.stopped.Load() {
		return
	}
	nextTrimmed := strings.TrimSpace(nextAuthDir)
	if nextTrimmed == "" {
		return
	}
	w.clientsMutex.RLock()
	prevWatched := strings.TrimSpace(w.watchedAuthDir)
	w.clientsMutex.RUnlock()
	if prevWatched == "" || prevWatched == nextTrimmed {
		return
	}
	if errMkdir := os.MkdirAll(nextTrimmed, 0o700); errMkdir != nil {
		log.Debugf("failed to ensure updated auth directory %s: %v", nextTrimmed, errMkdir)
	}
	if w.matchesAuthDir(prevWatched, nextTrimmed) {
		// Retain prevWatched as the registered fsnotify path spelling so a future
		// transition to a distinct directory removes the exact registered watch,
		// while isCurrentAuthDir accepts events emitted under either spelling.
		return
	}
	if errAdd := w.watcher.Add(nextTrimmed); errAdd != nil {
		log.Errorf("failed to watch updated auth directory %s: %v", nextTrimmed, errAdd)
		return
	}
	if !w.matchesAuthDir(prevWatched, w.configPath) {
		if errRemove := w.watcher.Remove(prevWatched); errRemove != nil {
			log.Debugf("failed to remove old auth directory watch %s: %v", prevWatched, errRemove)
		}
	}
	w.clientsMutex.Lock()
	w.watchedAuthDir = nextTrimmed
	w.clientsMutex.Unlock()
	log.Debugf("watching updated auth directory: %s", nextTrimmed)
}

func (w *Watcher) start(ctx context.Context) error {
	if errAddConfig := w.watcher.Add(w.configPath); errAddConfig != nil {
		log.Errorf("failed to watch config file %s: %v", w.configPath, errAddConfig)
		return errAddConfig
	}
	log.Debugf("watching config file: %s", w.configPath)

	authDir := w.currentAuthDir()
	if errAddAuthDir := w.watcher.Add(authDir); errAddAuthDir != nil {
		log.Errorf("failed to watch auth directory %s: %v", authDir, errAddAuthDir)
		return errAddAuthDir
	}
	w.clientsMutex.Lock()
	w.watchedAuthDir = strings.TrimSpace(authDir)
	w.clientsMutex.Unlock()
	log.Debugf("watching auth directory: %s", authDir)

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
	// Filter only relevant events: config file or auth-dir JSON files.
	configOps := fsnotify.Write | fsnotify.Create | fsnotify.Rename
	normalizedName := w.normalizeAuthPath(event.Name)
	normalizedConfigPath := w.normalizeAuthPath(w.configPath)
	isConfigEvent := normalizedName == normalizedConfigPath && event.Op&configOps != 0
	authOps := fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename
	isAuthJSON := w.isCurrentAuthDir(filepath.Dir(normalizedName)) && strings.HasSuffix(normalizedName, ".json") && event.Op&authOps != 0
	if !isConfigEvent && !isAuthJSON {
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

	// Handle auth directory changes incrementally (.json only)
	w.authRescanMu.Lock()
	defer w.authRescanMu.Unlock()
	if !w.isCurrentAuthDir(filepath.Dir(normalizedName)) {
		return
	}
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
	data, errRead := os.ReadFile(path)
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
