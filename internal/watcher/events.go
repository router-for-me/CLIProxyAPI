// events.go implements fsnotify event handling for config and auth file changes.
// It normalizes paths, debounces noisy events, and triggers reload/update logic.
package watcher

import (
	"bytes"
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
	var fallbackSnapshot []byte
	if w.completion != nil {
		if errStartCompletion := w.completion.Start(ctx, w.notifyConfigComplete, w.configCompletionUnavailable, func() {
			w.completionActive.Store(true)
		}); errStartCompletion != nil {
			w.completionActive.Store(false)
			log.WithError(errStartCompletion).Warn("config completion watcher unavailable at startup; using fsnotify debounce fallback")
			_ = w.completion.Close()
			w.completion = nil
			fallbackSnapshot, errStartCompletion = os.ReadFile(w.configPath)
			if errStartCompletion != nil {
				log.Errorf("failed to access config file %s: %v", w.configPath, errStartCompletion)
				return errStartCompletion
			}
		}
	}
	closeCompletionOnError := func() {
		w.completionActive.Store(false)
		if w.completion != nil {
			_ = w.completion.Close()
		}
	}
	if w.startTestHook != nil {
		w.startTestHook()
	}
	if _, errStatConfig := os.Stat(w.configPath); errStatConfig != nil {
		closeCompletionOnError()
		log.Errorf("failed to access config file %s: %v", w.configPath, errStatConfig)
		return errStatConfig
	}
	configDir := filepath.Dir(w.configPath)
	if errAddConfig := w.watcher.Add(configDir); errAddConfig != nil {
		closeCompletionOnError()
		log.Errorf("failed to watch config directory %s: %v", configDir, errAddConfig)
		return errAddConfig
	}
	log.Debugf("watching config directory: %s", configDir)

	if w.normalizeAuthPath(configDir) != w.normalizeAuthPath(w.authDir) {
		if errAddAuthDir := w.watcher.Add(w.authDir); errAddAuthDir != nil {
			closeCompletionOnError()
			log.Errorf("failed to watch auth directory %s: %v", w.authDir, errAddAuthDir)
			return errAddAuthDir
		}
	}
	log.Debugf("watching auth directory: %s", w.authDir)

	if fallbackSnapshot != nil {
		if currentConfig, errReadCurrent := os.ReadFile(w.configPath); errReadCurrent != nil {
			log.WithError(errReadCurrent).Error("failed to reconcile config after fallback watcher admission")
		} else if !bytes.Equal(fallbackSnapshot, currentConfig) {
			w.scheduleConfigReload()
		}
	}

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
	isAuthJSON := filepath.Dir(normalizedName) == normalizedAuthDir && strings.HasSuffix(normalizedName, ".json") && event.Op&authOps != 0
	if !isConfigEvent && !isAuthJSON {
		// Ignore unrelated files (e.g., cookie snapshots *.cookie) and other noise.
		return
	}

	now := time.Now()
	log.Debugf("file system event detected: %s %s", event.Op.String(), event.Name)

	// Handle config file changes. Linux uses native close/move completion events;
	// other platforms retain fsnotify's quiet-window fallback.
	if isConfigEvent {
		log.Debugf("config file change details - operation: %s, timestamp: %s", event.Op.String(), now.Format("2006-01-02 15:04:05.000"))
		if !w.completionActive.Load() {
			w.scheduleConfigReload()
		}
		return
	}

	// Handle auth directory changes incrementally (.json only)
	w.authRescanMu.Lock()
	defer w.authRescanMu.Unlock()

	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
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
