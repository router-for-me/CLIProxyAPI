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

func (w *Watcher) start(ctx context.Context) error {
	if errAddConfig := w.watcher.Add(w.configPath); errAddConfig != nil {
		log.Errorf("failed to watch config file %s: %v", w.configPath, errAddConfig)
		return errAddConfig
	}
	log.Debugf("watching config file: %s", w.configPath)

	if errAddAuthDir := w.watcher.Add(w.authDir); errAddAuthDir != nil {
		log.Errorf("failed to watch auth directory %s: %v", w.authDir, errAddAuthDir)
		return errAddAuthDir
	}
	log.Debugf("watching auth directory: %s", w.authDir)

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
	normalizedAuthDir := w.normalizeAuthPath(w.authDir)
	isConfigEvent := normalizedName == normalizedConfigPath && event.Op&configOps != 0
	authOps := fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename
	isAuthJSON := strings.HasPrefix(normalizedName, normalizedAuthDir) && strings.HasSuffix(normalizedName, ".json") && event.Op&authOps != 0
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
	if w.isAuthPathSuppressed(normalizedName, now) {
		log.Debugf("suppressing auth event for %s", filepath.Base(event.Name))
		return
	}

	// Handle auth directory changes incrementally (.json only)
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		w.cancelPendingAuthWrite(normalizedName)
		if w.shouldDebounceRemove(normalizedName, now) {
			log.Debugf("debouncing remove event for %s", filepath.Base(event.Name))
			return
		}
		// Atomic replace on some platforms may surface as Rename (or Remove) before the new file is ready.
		// Wait briefly; if the path exists again, treat as an update instead of removal.
		time.Sleep(replaceCheckDelay)
		if _, statErr := os.Stat(event.Name); statErr == nil {
			if unchanged, errSame := w.authFileUnchanged(event.Name); errSame == nil && unchanged {
				log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(event.Name))
				return
			}
			w.logIncrementalAuthEvent(event.Op, event.Name)
			w.addOrUpdateClient(event.Name)
			return
		}
		if !w.isKnownAuthFile(event.Name) {
			log.Debugf("ignoring remove for unknown auth file: %s", filepath.Base(event.Name))
			return
		}
		w.logIncrementalAuthEvent(event.Op, event.Name)
		w.removeClient(event.Name)
		return
	}
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		w.scheduleAuthWrite(normalizedName, event.Name)
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

func (w *Watcher) logIncrementalAuthEvent(op fsnotify.Op, path string) {
	log.Debugf("auth file changed (%s): %s, processing incrementally", op.String(), filepath.Base(path))
}

func (w *Watcher) isAuthPathSuppressed(normalizedPath string, now time.Time) bool {
	if w == nil || normalizedPath == "" {
		return false
	}
	w.eventMu.Lock()
	defer w.eventMu.Unlock()
	if len(w.suppressedAuth) == 0 {
		return false
	}
	for path, until := range w.suppressedAuth {
		if !until.After(now) {
			delete(w.suppressedAuth, path)
		}
	}
	until, ok := w.suppressedAuth[normalizedPath]
	return ok && until.After(now)
}

func (w *Watcher) scheduleAuthWrite(normalizedPath, path string) {
	if w == nil || normalizedPath == "" {
		return
	}
	w.eventMu.Lock()
	if w.pendingAuthWrites == nil {
		w.pendingAuthWrites = make(map[string]*pendingAuthWrite)
	}
	if existing, ok := w.pendingAuthWrites[normalizedPath]; ok {
		existing.path = path
		if existing.timer != nil {
			existing.timer.Stop()
		}
		existing.timer = time.AfterFunc(authWriteDebounceWindow, func() {
			w.flushPendingAuthWrite(normalizedPath)
		})
		w.pendingAuthWrites[normalizedPath] = existing
		w.eventMu.Unlock()
		return
	}
	entry := &pendingAuthWrite{path: path}
	entry.timer = time.AfterFunc(authWriteDebounceWindow, func() {
		w.flushPendingAuthWrite(normalizedPath)
	})
	w.pendingAuthWrites[normalizedPath] = entry
	w.eventMu.Unlock()
}

func (w *Watcher) flushPendingAuthWrite(normalizedPath string) {
	if w == nil || normalizedPath == "" || w.stopped.Load() {
		return
	}
	w.eventMu.Lock()
	entry, ok := w.pendingAuthWrites[normalizedPath]
	if !ok {
		w.eventMu.Unlock()
		return
	}
	path := entry.path
	delete(w.pendingAuthWrites, normalizedPath)
	w.eventMu.Unlock()

	now := time.Now()
	if w.isAuthPathSuppressed(normalizedPath, now) {
		log.Debugf("suppressing auth write for %s", filepath.Base(path))
		return
	}
	if unchanged, errSame := w.authFileUnchanged(path); errSame == nil && unchanged {
		log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(path))
		return
	}
	w.logIncrementalAuthEvent(fsnotify.Write, path)
	w.addOrUpdateClient(path)
}

func (w *Watcher) cancelPendingAuthWrite(normalizedPath string) {
	if w == nil || normalizedPath == "" {
		return
	}
	w.eventMu.Lock()
	defer w.eventMu.Unlock()
	entry, ok := w.pendingAuthWrites[normalizedPath]
	if !ok {
		return
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	delete(w.pendingAuthWrites, normalizedPath)
}

func (w *Watcher) stopPendingAuthWrites() {
	if w == nil {
		return
	}
	w.eventMu.Lock()
	defer w.eventMu.Unlock()
	for key, entry := range w.pendingAuthWrites {
		if entry != nil && entry.timer != nil {
			entry.timer.Stop()
		}
		delete(w.pendingAuthWrites, key)
	}
}
