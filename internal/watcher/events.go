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

	authDir := w.effectiveAuthDir()
	if errAddAuthDir := w.watcher.Add(authDir); errAddAuthDir != nil {
		log.Errorf("failed to watch auth directory %s: %v", authDir, errAddAuthDir)
		return errAddAuthDir
	}
	log.Debugf("watching auth directory: %s", authDir)

	authCtx, cancel := context.WithCancel(ctx)
	w.authEventMu.Lock()
	if w.authEventCancel != nil {
		w.authEventCancel()
	}
	w.authEventCtx = authCtx
	w.authEventCancel = cancel
	w.authEventMu.Unlock()

	go w.processEvents(authCtx)

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
	normalizedAuthDir := w.normalizeAuthPath(w.effectiveAuthDir())
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

	w.enqueueAuthEvent(authPathEvent{
		path:       event.Name,
		normalized: normalizedName,
		op:         event.Op & authOps,
	})
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

func (w *Watcher) enqueueAuthEvent(event authPathEvent) {
	if w == nil || event.normalized == "" {
		return
	}
	w.authEventMu.Lock()
	ctx := w.authEventCtx
	w.authEventMu.Unlock()
	if ctx == nil {
		w.processAuthEvent(event)
		return
	}
	worker, _ := w.getOrCreateAuthWorker(event.normalized)
	worker.mu.Lock()
	if worker.hasPending {
		worker.pending.op |= event.op
		if strings.TrimSpace(event.path) != "" {
			worker.pending.path = event.path
		}
	} else {
		worker.pending = event
		worker.hasPending = true
	}
	worker.mu.Unlock()

	select {
	case worker.signal <- struct{}{}:
	default:
	}
}

func (w *Watcher) getOrCreateAuthWorker(normalized string) (*authEventWorker, context.Context) {
	w.authEventMu.Lock()
	defer w.authEventMu.Unlock()
	if w.authEventWorkers == nil {
		w.authEventWorkers = make(map[string]*authEventWorker)
	}
	if worker, ok := w.authEventWorkers[normalized]; ok && worker != nil {
		return worker, w.authEventCtx
	}
	worker := &authEventWorker{signal: make(chan struct{}, 1)}
	ctx := w.authEventCtx
	if ctx == nil {
		ctx = context.Background()
	}
	w.authEventWorkers[normalized] = worker
	go w.runAuthEventWorker(ctx, normalized, worker)
	return worker, ctx
}

func (w *Watcher) runAuthEventWorker(ctx context.Context, normalized string, worker *authEventWorker) {
	idle := time.NewTimer(authWorkerIdleTimeout)
	defer idle.Stop()
	defer w.removeAuthWorker(normalized, worker)

	for {
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
			return
		case <-worker.signal:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			for {
				event, ok := worker.takePending()
				if !ok {
					break
				}
				w.processAuthEvent(event)
			}
			idle.Reset(authWorkerIdleTimeout)
		}
	}
}

func (w *Watcher) removeAuthWorker(normalized string, worker *authEventWorker) {
	w.authEventMu.Lock()
	defer w.authEventMu.Unlock()
	if existing, ok := w.authEventWorkers[normalized]; ok && existing == worker {
		delete(w.authEventWorkers, normalized)
	}
}

func (w *Watcher) processAuthEvent(event authPathEvent) {
	now := time.Now()
	if event.op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		if w.shouldDebounceRemove(event.normalized, now) {
			log.Debugf("debouncing remove event for %s", filepath.Base(event.path))
			return
		}
		time.Sleep(replaceCheckDelay)
		if _, statErr := os.Stat(event.path); statErr == nil {
			if unchanged, errSame := w.authFileUnchanged(event.path); errSame == nil && unchanged {
				log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(event.path))
				return
			}
			log.Infof("auth file changed (%s): %s, processing incrementally", event.op.String(), filepath.Base(event.path))
			w.addOrUpdateClient(event.path)
			return
		}
		if !w.isKnownAuthFile(event.path) {
			log.Debugf("ignoring remove for unknown auth file: %s", filepath.Base(event.path))
			return
		}
		log.Infof("auth file changed (%s): %s, processing incrementally", event.op.String(), filepath.Base(event.path))
		w.removeClient(event.path)
		return
	}
	if event.op&(fsnotify.Create|fsnotify.Write) != 0 {
		if unchanged, errSame := w.authFileUnchanged(event.path); errSame == nil && unchanged {
			log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(event.path))
			return
		}
		log.Infof("auth file changed (%s): %s, processing incrementally", event.op.String(), filepath.Base(event.path))
		w.addOrUpdateClient(event.path)
	}
}

func (w *authEventWorker) takePending() (authPathEvent, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.hasPending {
		return authPathEvent{}, false
	}
	event := w.pending
	w.pending = authPathEvent{}
	w.hasPending = false
	return event, true
}
