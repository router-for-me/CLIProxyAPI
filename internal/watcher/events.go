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
	w.watchMu.Lock()
	if errAddConfig := w.watcher.Add(w.configPath); errAddConfig != nil {
		w.watchMu.Unlock()
		log.Errorf("failed to watch config file %s: %v", w.configPath, errAddConfig)
		return errAddConfig
	}
	configInfo, _ := os.Stat(w.configPath)
	configIsSymlink, configTarget := configSymlinkTarget(w.configPath)
	log.Debugf("watching config file: %s", w.configPath)

	authDir := w.currentAuthDir()
	if errAddAuthDir := w.watcher.Add(authDir); errAddAuthDir != nil {
		if errRemoveConfig := w.watcher.Remove(w.configPath); errRemoveConfig != nil {
			log.Debugf("failed to clean up config file watch %s: %v", w.configPath, errRemoveConfig)
		}
		w.watchMu.Unlock()
		log.Errorf("failed to watch auth directory %s: %v", authDir, errAddAuthDir)
		return errAddAuthDir
	}
	log.Debugf("watching auth directory: %s", authDir)

	watchedConfigDir := ""
	configDir := filepath.Dir(filepath.Clean(strings.TrimSpace(w.configPath)))
	if w.sameNormalizedAuthPath(configDir, authDir) {
		watchedConfigDir = authDir
	} else {
		if absConfigDir, errAbsConfigDir := filepath.Abs(configDir); errAbsConfigDir == nil {
			configDir = absConfigDir
		}
		if errAddConfigDir := w.watcher.Add(configDir); errAddConfigDir != nil {
			log.Warnf("failed to watch config directory %s for %s; falling back to file watch only: %v", configDir, w.configPath, errAddConfigDir)
		} else {
			watchedConfigDir = configDir
			log.Debugf("watching config directory %s for config file: %s", configDir, w.configPath)
		}
	}

	w.started = true
	w.watchedConfigFile = w.configPath
	w.watchedConfigTarget = configTarget
	w.watchedConfigIsSymlink = configIsSymlink
	w.watchedConfigInfo = configInfo
	w.watchedConfigDir = watchedConfigDir
	w.watchedAuthDir = authDir
	w.watchMu.Unlock()

	go w.processEvents(ctx)

	w.reloadClients(true, nil, false)
	return nil
}

func configSymlinkTarget(path string) (bool, string) {
	linfo, errLstat := os.Lstat(path)
	if errLstat != nil || linfo.Mode()&os.ModeSymlink == 0 {
		return false, ""
	}
	link, errReadlink := os.Readlink(path)
	if errReadlink != nil || strings.TrimSpace(link) == "" {
		return true, ""
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}
	return true, filepath.Clean(link)
}

func (w *Watcher) removeConfigFileWatchLocked() {
	if w.watchedConfigFile != "" {
		if errRemove := w.watcher.Remove(w.watchedConfigFile); errRemove != nil {
			log.Debugf("failed to remove previous config file watch %s: %v", w.watchedConfigFile, errRemove)
		}
	}
	if w.watchedConfigTarget != "" && !w.sameNormalizedAuthPath(w.watchedConfigTarget, w.watchedConfigFile) {
		if errRemoveTarget := w.watcher.Remove(w.watchedConfigTarget); errRemoveTarget != nil {
			log.Debugf("failed to remove previous config symlink target watch %s: %v", w.watchedConfigTarget, errRemoveTarget)
		}
	}
	w.watchedConfigFile = ""
	w.watchedConfigTarget = ""
	w.watchedConfigInfo = nil
}

func (w *Watcher) refreshConfigFileWatch() bool {
	if w == nil || w.watcher == nil || w.stopped.Load() {
		return false
	}
	curInfo, errStat := os.Stat(w.configPath)
	curIsSymlink, curTarget := configSymlinkTarget(w.configPath)
	w.watchMu.Lock()
	defer w.watchMu.Unlock()
	if !w.started || w.stopped.Load() || w.watcher == nil {
		return errStat == nil
	}
	if errStat != nil {
		w.removeConfigFileWatchLocked()
		w.watchedConfigIsSymlink = curIsSymlink
		return false
	}
	if w.watchedConfigFile != "" && w.watchedConfigInfo != nil && os.SameFile(w.watchedConfigInfo, curInfo) {
		w.watchedConfigIsSymlink = curIsSymlink
		return true
	}
	w.removeConfigFileWatchLocked()
	if errAdd := w.watcher.Add(w.configPath); errAdd != nil {
		log.Debugf("failed to re-watch config file %s: %v", w.configPath, errAdd)
		w.watchedConfigIsSymlink = curIsSymlink
		return true
	}
	w.watchedConfigFile = w.configPath
	w.watchedConfigTarget = curTarget
	w.watchedConfigIsSymlink = curIsSymlink
	w.watchedConfigInfo = curInfo
	log.Debugf("re-attached config file watch: %s", w.configPath)
	return true
}

func (w *Watcher) configSymlinkChangedInDir(normalizedName string, op fsnotify.Op) bool {
	if w == nil || w.watcher == nil || w.stopped.Load() {
		return false
	}
	if op&(fsnotify.Create|fsnotify.Rename|fsnotify.Remove|fsnotify.Write) == 0 {
		return false
	}
	w.watchMu.Lock()
	isSymlink := w.watchedConfigIsSymlink
	configDir := w.watchedConfigDir
	prevInfo := w.watchedConfigInfo
	w.watchMu.Unlock()
	if !isSymlink || configDir == "" || !w.isPathInAuthDir(normalizedName, configDir) {
		return false
	}
	curInfo, errStat := os.Stat(w.configPath)
	if errStat != nil {
		return false
	}
	return prevInfo == nil || !os.SameFile(prevInfo, curInfo)
}

func (w *Watcher) syncAuthDirWatch() {
	if w == nil || w.watcher == nil || w.stopped.Load() {
		return
	}
	w.watchMu.Lock()
	defer w.watchMu.Unlock()
	if !w.started || w.stopped.Load() {
		return
	}
	target := strings.TrimSpace(w.currentAuthDir())
	oldWatched := strings.TrimSpace(w.watchedAuthDir)
	if oldWatched == target {
		return
	}
	if oldWatched != "" && !w.sameNormalizedAuthPath(oldWatched, w.watchedConfigDir) && !w.sameNormalizedAuthPath(oldWatched, w.configPath) && !w.sameNormalizedAuthPath(oldWatched, target) {
		if errRemove := w.watcher.Remove(oldWatched); errRemove != nil {
			log.Debugf("failed to remove previous auth directory watch %s: %v", oldWatched, errRemove)
		}
		w.watchedAuthDir = ""
	}
	if target == "" {
		w.watchedAuthDir = ""
		return
	}
	if w.watchedAuthDir != "" && w.sameNormalizedAuthPath(w.watchedAuthDir, target) {
		w.watchedAuthDir = target
		return
	}
	if errAdd := w.watcher.Add(target); errAdd != nil {
		log.Errorf("failed to watch auth directory %s: %v", target, errAdd)
		return
	}
	w.watchedAuthDir = target
	log.Debugf("watching auth directory: %s", target)
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
	activeAuthDir := w.currentAuthDir()
	isConfigPath := w.isConfigEventPath(normalizedName, normalizedConfigPath)
	isConfigEvent := isConfigPath && event.Op&configOps != 0
	isConfigRemove := isConfigPath && event.Op&fsnotify.Remove != 0
	authOps := fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename
	isAuthJSON := w.isPathInAuthDir(normalizedName, activeAuthDir) && strings.HasSuffix(normalizedName, ".json") && event.Op&authOps != 0
	if !isConfigEvent && !isConfigRemove && !isAuthJSON {
		if w.configSymlinkChangedInDir(normalizedName, event.Op) {
			isConfigEvent = true
		} else {
			// Ignore unrelated files (e.g., cookie snapshots *.cookie) and other noise.
			return
		}
	}

	now := time.Now()
	log.Debugf("file system event detected: %s %s", event.Op.String(), event.Name)

	// Handle config file changes
	if isConfigEvent || isConfigRemove {
		exists := w.refreshConfigFileWatch()
		if isConfigEvent || exists {
			log.Debugf("config file change details - operation: %s, timestamp: %s", event.Op.String(), now.Format("2006-01-02 15:04:05.000"))
			w.scheduleConfigReload()
		}
		return
	}

	// Handle auth directory changes incrementally (.json only)
	w.authRescanMu.Lock()
	defer w.authRescanMu.Unlock()
	currentAuthDir := w.currentAuthDir()
	if !w.isPathInAuthDir(normalizedName, currentAuthDir) {
		return
	}
	authEventPath := event.Name
	if strings.TrimSpace(currentAuthDir) != "" {
		authEventPath = filepath.Join(currentAuthDir, filepath.Base(normalizedName))
	}
	normalizedAuthPath := w.normalizeAuthPath(authEventPath)
	w.observeAuthFile(authEventPath)

	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		if w.shouldDebounceRemove(normalizedAuthPath, now) {
			log.Debugf("debouncing remove event for %s", filepath.Base(authEventPath))
			return
		}
		// Atomic replace on some platforms may surface as Rename (or Remove) before the new file is ready.
		// Wait briefly; if the path exists again, treat as an update instead of removal.
		time.Sleep(replaceCheckDelay)
		if !w.isPathInAuthDir(normalizedAuthPath, w.currentAuthDir()) {
			return
		}
		statSucceeded := false
		if _, statErr := os.Stat(authEventPath); statErr == nil {
			statSucceeded = true
		} else if w.isKnownAuthFile(authEventPath) {
			// On macOS (and under disk I/O pressure), atomic replace (e.g. os.Rename from temp file)
			// can trigger a NOTE_DELETE before the target inode is fully visible. Retry briefly
			// before treating a known auth file as permanently removed.
			for attempt := 0; attempt < 3; attempt++ {
				time.Sleep(25 * time.Millisecond)
				if !w.isPathInAuthDir(normalizedAuthPath, w.currentAuthDir()) {
					return
				}
				if _, retryErr := os.Stat(authEventPath); retryErr == nil {
					statSucceeded = true
					break
				}
			}
		}
		if statSucceeded {
			if unchanged, errSame := w.authFileUnchanged(authEventPath); errSame == nil && unchanged {
				log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(authEventPath))
				return
			}
			log.Infof("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(authEventPath))
			w.addOrUpdateClientLocked(authEventPath)
			return
		}
		if !w.isKnownAuthFile(authEventPath) {
			log.Debugf("ignoring remove for unknown auth file: %s", filepath.Base(authEventPath))
			return
		}
		log.Infof("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(authEventPath))
		w.removeClientLocked(authEventPath)
		return
	}
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		if unchanged, errSame := w.authFileUnchanged(authEventPath); errSame == nil && unchanged {
			log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(authEventPath))
			return
		}
		log.Infof("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(authEventPath))
		w.addOrUpdateClientLocked(authEventPath)
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
	if !w.isPathInCurrentAuthDirLocked(normalized) {
		return
	}
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

func (w *Watcher) isConfigEventPath(normalizedName, normalizedConfigPath string) bool {
	if normalizedName == "" || normalizedConfigPath == "" {
		return false
	}
	if normalizedName == normalizedConfigPath {
		return true
	}
	if filepath.Base(normalizedName) == filepath.Base(normalizedConfigPath) {
		return w.sameNormalizedAuthPath(normalizedName, normalizedConfigPath)
	}
	if !strings.EqualFold(filepath.Base(normalizedName), filepath.Base(normalizedConfigPath)) {
		return false
	}
	if !w.sameNormalizedAuthPath(filepath.Dir(normalizedName), filepath.Dir(normalizedConfigPath)) {
		return false
	}
	eventInfo, errEventStat := os.Stat(normalizedName)
	if errEventStat != nil {
		return false
	}
	configInfo, errConfigStat := os.Stat(w.configPath)
	if errConfigStat != nil {
		return false
	}
	return os.SameFile(eventInfo, configInfo)
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
