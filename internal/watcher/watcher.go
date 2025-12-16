// Package watcher provides file system monitoring functionality for the CLI Proxy API.
// It watches configuration files and authentication directories for changes,
// automatically reloading clients and configuration when files are modified.
// The package handles cross-platform file system events and supports hot-reloading.
package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/geminicli"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/diff"
	"gopkg.in/yaml.v3"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
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

// storePersister captures persistence-capable token store methods used by the watcher.
type storePersister interface {
	PersistConfig(ctx context.Context) error
	PersistAuthFiles(ctx context.Context, message string, paths ...string) error
}

type authDirProvider interface {
	AuthDir() string
}

// Watcher manages file watching for configuration and authentication files
type Watcher struct {
	configPath        string
	authDir           string
	config            *config.Config
	clientsMutex      sync.RWMutex
	configReloadMu    sync.Mutex
	configReloadTimer *time.Timer
	reloadCallback    func(*config.Config)
	watcher           *fsnotify.Watcher
	lastAuthHashes    map[string]string
	lastRemoveTimes   map[string]time.Time
	lastConfigHash    string
	authQueue         chan<- AuthUpdate
	currentAuths      map[string]*coreauth.Auth
	runtimeAuths      map[string]*coreauth.Auth
	dispatchMu        sync.Mutex
	dispatchCond      *sync.Cond
	pendingUpdates    map[string]AuthUpdate
	pendingOrder      []string
	dispatchCancel    context.CancelFunc
	storePersister    storePersister
	mirroredAuthDir   string
	oldConfigYaml     []byte
}

type stableIDGenerator struct {
	counters map[string]int
}

func newStableIDGenerator() *stableIDGenerator {
	return &stableIDGenerator{counters: make(map[string]int)}
}

func (g *stableIDGenerator) next(kind string, parts ...string) (string, string) {
	if g == nil {
		return kind + ":000000000000", "000000000000"
	}
	hasher := sha256.New()
	hasher.Write([]byte(kind))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		hasher.Write([]byte{0})
		hasher.Write([]byte(trimmed))
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if len(digest) < 12 {
		digest = fmt.Sprintf("%012s", digest)
	}
	short := digest[:12]
	key := kind + ":" + short
	index := g.counters[key]
	g.counters[key] = index + 1
	if index > 0 {
		short = fmt.Sprintf("%s-%d", short, index)
	}
	return fmt.Sprintf("%s:%s", kind, short), short
}

// AuthUpdateAction represents the type of change detected in auth sources.
type AuthUpdateAction string

const (
	AuthUpdateActionAdd    AuthUpdateAction = "add"
	AuthUpdateActionModify AuthUpdateAction = "modify"
	AuthUpdateActionDelete AuthUpdateAction = "delete"
)

// AuthUpdate describes an incremental change to auth configuration.
type AuthUpdate struct {
	Action AuthUpdateAction
	ID     string
	Auth   *coreauth.Auth
}

const (
	// replaceCheckDelay is a short delay to allow atomic replace (rename) to settle
	// before deciding whether a Remove event indicates a real deletion.
	replaceCheckDelay        = 50 * time.Millisecond
	configReloadDebounce     = 150 * time.Millisecond
	authRemoveDebounceWindow = 1 * time.Second
)

// NewWatcher creates a new file watcher instance
func NewWatcher(configPath, authDir string, reloadCallback func(*config.Config)) (*Watcher, error) {
	watcher, errNewWatcher := fsnotify.NewWatcher()
	if errNewWatcher != nil {
		return nil, errNewWatcher
	}
	w := &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		reloadCallback: reloadCallback,
		watcher:        watcher,
		lastAuthHashes: make(map[string]string),
	}
	w.dispatchCond = sync.NewCond(&w.dispatchMu)
	if store := sdkAuth.GetTokenStore(); store != nil {
		if persister, ok := store.(storePersister); ok {
			w.storePersister = persister
			log.Debug("persistence-capable token store detected; watcher will propagate persisted changes")
		}
		if provider, ok := store.(authDirProvider); ok {
			if fixed := strings.TrimSpace(provider.AuthDir()); fixed != "" {
				w.mirroredAuthDir = fixed
				log.Debugf("mirrored auth directory locked to %s", fixed)
			}
		}
	}
	return w, nil
}

// Start begins watching the configuration file and authentication directory
func (w *Watcher) Start(ctx context.Context) error {
	// Watch the config file
	if errAddConfig := w.watcher.Add(w.configPath); errAddConfig != nil {
		log.Errorf("failed to watch config file %s: %v", w.configPath, errAddConfig)
		return errAddConfig
	}
	log.Debugf("watching config file: %s", w.configPath)

	// Watch the auth directory
	if errAddAuthDir := w.watcher.Add(w.authDir); errAddAuthDir != nil {
		log.Errorf("failed to watch auth directory %s: %v", w.authDir, errAddAuthDir)
		return errAddAuthDir
	}
	log.Debugf("watching auth directory: %s", w.authDir)

	// Watch Kiro IDE token file directory for automatic token updates
	w.watchKiroIDETokenFile()

	// Start the event processing goroutine
	go w.processEvents(ctx)

	// Perform an initial full reload based on current config and auth dir
	w.reloadClients(true, nil, false)
	return nil
}

// watchKiroIDETokenFile adds the Kiro IDE token file directory to the watcher.
// This enables automatic detection of token updates from Kiro IDE.
func (w *Watcher) watchKiroIDETokenFile() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Debugf("failed to get home directory for Kiro IDE token watch: %v", err)
		return
	}

	// Kiro IDE stores tokens in ~/.aws/sso/cache/
	kiroTokenDir := filepath.Join(homeDir, ".aws", "sso", "cache")

	// Check if directory exists
	if _, statErr := os.Stat(kiroTokenDir); os.IsNotExist(statErr) {
		log.Debugf("Kiro IDE token directory does not exist: %s", kiroTokenDir)
		return
	}

	if errAdd := w.watcher.Add(kiroTokenDir); errAdd != nil {
		log.Debugf("failed to watch Kiro IDE token directory %s: %v", kiroTokenDir, errAdd)
		return
	}
	log.Debugf("watching Kiro IDE token directory: %s", kiroTokenDir)
}

// Stop stops the file watcher
func (w *Watcher) Stop() error {
	w.stopDispatch()
	w.stopConfigReloadTimer()
	return w.watcher.Close()
}

func (w *Watcher) stopConfigReloadTimer() {
	w.configReloadMu.Lock()
	if w.configReloadTimer != nil {
		w.configReloadTimer.Stop()
		w.configReloadTimer = nil
	}
	w.configReloadMu.Unlock()
}

// SetConfig updates the current configuration
func (w *Watcher) SetConfig(cfg *config.Config) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.config = cfg
	w.oldConfigYaml, _ = yaml.Marshal(cfg)
}

// SetAuthUpdateQueue sets the queue used to emit auth updates.
func (w *Watcher) SetAuthUpdateQueue(queue chan<- AuthUpdate) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.authQueue = queue
	if w.dispatchCond == nil {
		w.dispatchCond = sync.NewCond(&w.dispatchMu)
	}
	if w.dispatchCancel != nil {
		w.dispatchCancel()
		if w.dispatchCond != nil {
			w.dispatchMu.Lock()
			w.dispatchCond.Broadcast()
			w.dispatchMu.Unlock()
		}
		w.dispatchCancel = nil
	}
	if queue != nil {
		ctx, cancel := context.WithCancel(context.Background())
		w.dispatchCancel = cancel
		go w.dispatchLoop(ctx)
	}
}

// DispatchRuntimeAuthUpdate allows external runtime providers (e.g., websocket-driven auths)
// to push auth updates through the same queue used by file/config watchers.
// Returns true if the update was enqueued; false if no queue is configured.
func (w *Watcher) DispatchRuntimeAuthUpdate(update AuthUpdate) bool {
	if w == nil {
		return false
	}
	w.clientsMutex.Lock()
	if w.runtimeAuths == nil {
		w.runtimeAuths = make(map[string]*coreauth.Auth)
	}
	switch update.Action {
	case AuthUpdateActionAdd, AuthUpdateActionModify:
		if update.Auth != nil && update.Auth.ID != "" {
			clone := update.Auth.Clone()
			w.runtimeAuths[clone.ID] = clone
			if w.currentAuths == nil {
				w.currentAuths = make(map[string]*coreauth.Auth)
			}
			w.currentAuths[clone.ID] = clone.Clone()
		}
	case AuthUpdateActionDelete:
		id := update.ID
		if id == "" && update.Auth != nil {
			id = update.Auth.ID
		}
		if id != "" {
			delete(w.runtimeAuths, id)
			if w.currentAuths != nil {
				delete(w.currentAuths, id)
			}
		}
	}
	w.clientsMutex.Unlock()
	if w.getAuthQueue() == nil {
		return false
	}
	w.dispatchAuthUpdates([]AuthUpdate{update})
	return true
}

func (w *Watcher) refreshAuthState(force bool) {
	auths := w.SnapshotCoreAuths()
	w.clientsMutex.Lock()
	if len(w.runtimeAuths) > 0 {
		for _, a := range w.runtimeAuths {
			if a != nil {
				auths = append(auths, a.Clone())
			}
		}
	}
	updates := w.prepareAuthUpdatesLocked(auths, force)
	w.clientsMutex.Unlock()
	w.dispatchAuthUpdates(updates)
}

func (w *Watcher) prepareAuthUpdatesLocked(auths []*coreauth.Auth, force bool) []AuthUpdate {
	newState := make(map[string]*coreauth.Auth, len(auths))
	for _, auth := range auths {
		if auth == nil || auth.ID == "" {
			continue
		}
		newState[auth.ID] = auth.Clone()
	}
	if w.currentAuths == nil {
		w.currentAuths = newState
		if w.authQueue == nil {
			return nil
		}
		updates := make([]AuthUpdate, 0, len(newState))
		for id, auth := range newState {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionAdd, ID: id, Auth: auth.Clone()})
		}
		return updates
	}
	if w.authQueue == nil {
		w.currentAuths = newState
		return nil
	}
	updates := make([]AuthUpdate, 0, len(newState)+len(w.currentAuths))
	for id, auth := range newState {
		if existing, ok := w.currentAuths[id]; !ok {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionAdd, ID: id, Auth: auth.Clone()})
		} else if force || !authEqual(existing, auth) {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionModify, ID: id, Auth: auth.Clone()})
		}
	}
	for id := range w.currentAuths {
		if _, ok := newState[id]; !ok {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionDelete, ID: id})
		}
	}
	w.currentAuths = newState
	return updates
}

func (w *Watcher) dispatchAuthUpdates(updates []AuthUpdate) {
	if len(updates) == 0 {
		return
	}
	queue := w.getAuthQueue()
	if queue == nil {
		return
	}
	baseTS := time.Now().UnixNano()
	w.dispatchMu.Lock()
	if w.pendingUpdates == nil {
		w.pendingUpdates = make(map[string]AuthUpdate)
	}
	for idx, update := range updates {
		key := w.authUpdateKey(update, baseTS+int64(idx))
		if _, exists := w.pendingUpdates[key]; !exists {
			w.pendingOrder = append(w.pendingOrder, key)
		}
		w.pendingUpdates[key] = update
	}
	if w.dispatchCond != nil {
		w.dispatchCond.Signal()
	}
	w.dispatchMu.Unlock()
}

func (w *Watcher) authUpdateKey(update AuthUpdate, ts int64) string {
	if update.ID != "" {
		return update.ID
	}
	return fmt.Sprintf("%s:%d", update.Action, ts)
}

func (w *Watcher) dispatchLoop(ctx context.Context) {
	for {
		batch, ok := w.nextPendingBatch(ctx)
		if !ok {
			return
		}
		queue := w.getAuthQueue()
		if queue == nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for _, update := range batch {
			select {
			case queue <- update:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (w *Watcher) nextPendingBatch(ctx context.Context) ([]AuthUpdate, bool) {
	w.dispatchMu.Lock()
	defer w.dispatchMu.Unlock()
	for len(w.pendingOrder) == 0 {
		if ctx.Err() != nil {
			return nil, false
		}
		w.dispatchCond.Wait()
		if ctx.Err() != nil {
			return nil, false
		}
	}
	batch := make([]AuthUpdate, 0, len(w.pendingOrder))
	for _, key := range w.pendingOrder {
		batch = append(batch, w.pendingUpdates[key])
		delete(w.pendingUpdates, key)
	}
	w.pendingOrder = w.pendingOrder[:0]
	return batch, true
}

func (w *Watcher) getAuthQueue() chan<- AuthUpdate {
	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	return w.authQueue
}

func (w *Watcher) stopDispatch() {
	if w.dispatchCancel != nil {
		w.dispatchCancel()
		w.dispatchCancel = nil
	}
	w.dispatchMu.Lock()
	w.pendingOrder = nil
	w.pendingUpdates = nil
	if w.dispatchCond != nil {
		w.dispatchCond.Broadcast()
	}
	w.dispatchMu.Unlock()
	w.clientsMutex.Lock()
	w.authQueue = nil
	w.clientsMutex.Unlock()
}

func (w *Watcher) persistConfigAsync() {
	if w == nil || w.storePersister == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := w.storePersister.PersistConfig(ctx); err != nil {
			log.Errorf("failed to persist config change: %v", err)
		}
	}()
}

func (w *Watcher) persistAuthAsync(message string, paths ...string) {
	if w == nil || w.storePersister == nil {
		return
	}
	filtered := make([]string, 0, len(paths))
	for _, p := range paths {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	if len(filtered) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := w.storePersister.PersistAuthFiles(ctx, message, filtered...); err != nil {
			log.Errorf("failed to persist auth changes: %v", err)
		}
	}()
}

func authEqual(a, b *coreauth.Auth) bool {
	return reflect.DeepEqual(normalizeAuth(a), normalizeAuth(b))
}

func normalizeAuth(a *coreauth.Auth) *coreauth.Auth {
	if a == nil {
		return nil
	}
	clone := a.Clone()
	clone.CreatedAt = time.Time{}
	clone.UpdatedAt = time.Time{}
	clone.LastRefreshedAt = time.Time{}
	clone.NextRefreshAfter = time.Time{}
	clone.Runtime = nil
	clone.Quota.NextRecoverAt = time.Time{}
	return clone
}

func applyAuthExcludedModelsMeta(auth *coreauth.Auth, cfg *config.Config, perKey []string, authKind string) {
	if auth == nil || cfg == nil {
		return
	}
	authKindKey := strings.ToLower(strings.TrimSpace(authKind))
	seen := make(map[string]struct{})
	add := func(list []string) {
		for _, entry := range list {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				key := strings.ToLower(trimmed)
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
			}
		}
	}
	if authKindKey == "apikey" {
		add(perKey)
	} else if cfg.OAuthExcludedModels != nil {
		providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
		add(cfg.OAuthExcludedModels[providerKey])
	}
	combined := make([]string, 0, len(seen))
	for k := range seen {
		combined = append(combined, k)
	}
	sort.Strings(combined)
	hash := diff.ComputeExcludedModelsHash(combined)
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if hash != "" {
		auth.Attributes["excluded_models_hash"] = hash
	}
	if authKind != "" {
		auth.Attributes["auth_kind"] = authKind
	}
}

// SetClients sets the file-based clients.
// SetClients removed
// SetAPIKeyClients removed

// processEvents handles file system events
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

// handleEvent processes individual file system events
func (w *Watcher) handleEvent(event fsnotify.Event) {
	// Filter only relevant events: config file or auth-dir JSON files.
	configOps := fsnotify.Write | fsnotify.Create | fsnotify.Rename
	normalizedName := w.normalizeAuthPath(event.Name)
	normalizedConfigPath := w.normalizeAuthPath(w.configPath)
	normalizedAuthDir := w.normalizeAuthPath(w.authDir)
	isConfigEvent := normalizedName == normalizedConfigPath && event.Op&configOps != 0
	authOps := fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename
	isAuthJSON := strings.HasPrefix(normalizedName, normalizedAuthDir) && strings.HasSuffix(normalizedName, ".json") && event.Op&authOps != 0

	// Check for Kiro IDE token file changes
	isKiroIDEToken := w.isKiroIDETokenFile(event.Name) && event.Op&authOps != 0

	if !isConfigEvent && !isAuthJSON && !isKiroIDEToken {
		// Ignore unrelated files (e.g., cookie snapshots *.cookie) and other noise.
		return
	}

	// Handle Kiro IDE token file changes
	if isKiroIDEToken {
		w.handleKiroIDETokenChange(event)
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
			w.addOrUpdateClient(event.Name)
			return
		}
		if !w.isKnownAuthFile(event.Name) {
			log.Debugf("ignoring remove for unknown auth file: %s", filepath.Base(event.Name))
			return
		}
		log.Infof("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(event.Name))
		w.removeClient(event.Name)
		return
	}
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		if unchanged, errSame := w.authFileUnchanged(event.Name); errSame == nil && unchanged {
			log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(event.Name))
			return
		}
		log.Infof("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(event.Name))
		w.addOrUpdateClient(event.Name)
	}
}

func (w *Watcher) scheduleConfigReload() {
	w.configReloadMu.Lock()
	defer w.configReloadMu.Unlock()
	if w.configReloadTimer != nil {
		w.configReloadTimer.Stop()
	}
	w.configReloadTimer = time.AfterFunc(configReloadDebounce, func() {
		w.configReloadMu.Lock()
		w.configReloadTimer = nil
		w.configReloadMu.Unlock()
		w.reloadConfigIfChanged()
	})
}

// isKiroIDETokenFile checks if the given path is the Kiro IDE token file.
func (w *Watcher) isKiroIDETokenFile(path string) bool {
	// Check if it's the kiro-auth-token.json file in ~/.aws/sso/cache/
	// Use filepath.ToSlash to ensure consistent separators across platforms (Windows uses backslashes)
	normalized := filepath.ToSlash(path)
	return strings.HasSuffix(normalized, "kiro-auth-token.json") && strings.Contains(normalized, ".aws/sso/cache")
}

// handleKiroIDETokenChange processes changes to the Kiro IDE token file.
// When the token file is updated by Kiro IDE, this triggers a reload of Kiro auth.
func (w *Watcher) handleKiroIDETokenChange(event fsnotify.Event) {
	log.Debugf("Kiro IDE token file event detected: %s %s", event.Op.String(), event.Name)

	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		// Token file removed - wait briefly for potential atomic replace
		time.Sleep(replaceCheckDelay)
		if _, statErr := os.Stat(event.Name); statErr != nil {
			log.Debugf("Kiro IDE token file removed: %s", event.Name)
			return
		}
	}

	// Try to load the updated token
	tokenData, err := kiroauth.LoadKiroIDEToken()
	if err != nil {
		log.Debugf("failed to load Kiro IDE token after change: %v", err)
		return
	}

	log.Infof("Kiro IDE token file updated, access token refreshed (provider: %s)", tokenData.Provider)

	// Trigger auth state refresh to pick up the new token
	w.refreshAuthState(true)

	// Notify callback if set
	w.clientsMutex.RLock()
	cfg := w.config
	w.clientsMutex.RUnlock()

	if w.reloadCallback != nil && cfg != nil {
		log.Debugf("triggering server update callback after Kiro IDE token change")
		w.reloadCallback(cfg)
	}
}

func (w *Watcher) reloadConfigIfChanged() {
	data, err := os.ReadFile(w.configPath)
	if err != nil {
		log.Errorf("failed to read config file for hash check: %v", err)
		return
	}
	if len(data) == 0 {
		log.Debugf("ignoring empty config file write event")
		return
	}
	sum := sha256.Sum256(data)
	newHash := hex.EncodeToString(sum[:])

	w.clientsMutex.RLock()
	currentHash := w.lastConfigHash
	w.clientsMutex.RUnlock()

	if currentHash != "" && currentHash == newHash {
		log.Debugf("config file content unchanged (hash match), skipping reload")
		return
	}
	log.Infof("config file changed, reloading: %s", w.configPath)
	if w.reloadConfig() {
		finalHash := newHash
		if updatedData, errRead := os.ReadFile(w.configPath); errRead == nil && len(updatedData) > 0 {
			sumUpdated := sha256.Sum256(updatedData)
			finalHash = hex.EncodeToString(sumUpdated[:])
		} else if errRead != nil {
			log.WithError(errRead).Debug("failed to compute updated config hash after reload")
		}
		w.clientsMutex.Lock()
		w.lastConfigHash = finalHash
		w.clientsMutex.Unlock()
		w.persistConfigAsync()
	}
}

// reloadConfig reloads the configuration and triggers a full reload
func (w *Watcher) reloadConfig() bool {
	log.Debug("=========================== CONFIG RELOAD ============================")
	log.Debugf("starting config reload from: %s", w.configPath)

	newConfig, errLoadConfig := config.LoadConfig(w.configPath)
	if errLoadConfig != nil {
		log.Errorf("failed to reload config: %v", errLoadConfig)
		return false
	}

	if w.mirroredAuthDir != "" {
		newConfig.AuthDir = w.mirroredAuthDir
	} else {
		if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(newConfig.AuthDir); errResolveAuthDir != nil {
			log.Errorf("failed to resolve auth directory from config: %v", errResolveAuthDir)
		} else {
			newConfig.AuthDir = resolvedAuthDir
		}
	}

	w.clientsMutex.Lock()
	var oldConfig *config.Config
	_ = yaml.Unmarshal(w.oldConfigYaml, &oldConfig)
	w.oldConfigYaml, _ = yaml.Marshal(newConfig)
	w.config = newConfig
	w.clientsMutex.Unlock()

	var affectedOAuthProviders []string
	if oldConfig != nil {
		_, affectedOAuthProviders = diff.DiffOAuthExcludedModelChanges(oldConfig.OAuthExcludedModels, newConfig.OAuthExcludedModels)
	}

	// Always apply the current log level based on the latest config.
	// This ensures logrus reflects the desired level even if change detection misses.
	util.SetLogLevel(newConfig)
	// Additional debug for visibility when the flag actually changes.
	if oldConfig != nil && oldConfig.Debug != newConfig.Debug {
		log.Debugf("log level updated - debug mode changed from %t to %t", oldConfig.Debug, newConfig.Debug)
	}

	// Log configuration changes in debug mode, only when there are material diffs
	if oldConfig != nil {
		details := diff.BuildConfigChangeDetails(oldConfig, newConfig)
		if len(details) > 0 {
			log.Debugf("config changes detected:")
			for _, d := range details {
				log.Debugf("  %s", d)
			}
		} else {
			log.Debugf("no material config field changes detected")
		}
	}

	authDirChanged := oldConfig == nil || oldConfig.AuthDir != newConfig.AuthDir
	forceAuthRefresh := oldConfig != nil && oldConfig.ForceModelPrefix != newConfig.ForceModelPrefix

	log.Infof("config successfully reloaded, triggering client reload")
	// Reload clients with new config
	w.reloadClients(authDirChanged, affectedOAuthProviders, forceAuthRefresh)
	return true
}

// reloadClients performs a full scan and reload of all clients.
func (w *Watcher) reloadClients(rescanAuth bool, affectedOAuthProviders []string, forceAuthRefresh bool) {
	log.Debugf("starting full client load process")

	w.clientsMutex.RLock()
	cfg := w.config
	w.clientsMutex.RUnlock()

	if cfg == nil {
		log.Error("config is nil, cannot reload clients")
		return
	}

	if len(affectedOAuthProviders) > 0 {
		w.clientsMutex.Lock()
		if w.currentAuths != nil {
			filtered := make(map[string]*coreauth.Auth, len(w.currentAuths))
			for id, auth := range w.currentAuths {
				if auth == nil {
					continue
				}
				provider := strings.ToLower(strings.TrimSpace(auth.Provider))
				if _, match := matchProvider(provider, affectedOAuthProviders); match {
					continue
				}
				filtered[id] = auth
			}
			w.currentAuths = filtered
			log.Debugf("applying oauth-excluded-models to providers %v", affectedOAuthProviders)
		} else {
			w.currentAuths = nil
		}
		w.clientsMutex.Unlock()
	}

	// Unregister all old API key clients before creating new ones
	// no legacy clients to unregister

	// Create new API key clients based on the new config
	geminiAPIKeyCount, vertexCompatAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, openAICompatCount := BuildAPIKeyClients(cfg)
	totalAPIKeyClients := geminiAPIKeyCount + vertexCompatAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + openAICompatCount
	log.Debugf("loaded %d API key clients", totalAPIKeyClients)

	var authFileCount int
	if rescanAuth {
		// Load file-based clients when explicitly requested (startup or authDir change)
		authFileCount = w.loadFileClients(cfg)
		log.Debugf("loaded %d file-based clients", authFileCount)
	} else {
		// Preserve existing auth hashes and only report current known count to avoid redundant scans.
		w.clientsMutex.RLock()
		authFileCount = len(w.lastAuthHashes)
		w.clientsMutex.RUnlock()
		log.Debugf("skipping auth directory rescan; retaining %d existing auth files", authFileCount)
	}

	// no legacy file-based clients to unregister

	// Update client maps
	if rescanAuth {
		w.clientsMutex.Lock()

		// Rebuild auth file hash cache for current clients
		w.lastAuthHashes = make(map[string]string)
		if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir); errResolveAuthDir != nil {
			log.Errorf("failed to resolve auth directory for hash cache: %v", errResolveAuthDir)
		} else if resolvedAuthDir != "" {
			_ = filepath.Walk(resolvedAuthDir, func(path string, info fs.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".json") {
					if data, errReadFile := os.ReadFile(path); errReadFile == nil && len(data) > 0 {
						sum := sha256.Sum256(data)
						normalizedPath := w.normalizeAuthPath(path)
						w.lastAuthHashes[normalizedPath] = hex.EncodeToString(sum[:])
					}
				}
				return nil
			})
		}
		w.clientsMutex.Unlock()
	}

	totalNewClients := authFileCount + geminiAPIKeyCount + vertexCompatAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + openAICompatCount

	// Ensure consumers observe the new configuration before auth updates dispatch.
	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback before auth refresh")
		w.reloadCallback(cfg)
	}

	w.refreshAuthState(forceAuthRefresh)

	log.Infof("full client load complete - %d clients (%d auth files + %d Gemini API keys + %d Vertex API keys + %d Claude API keys + %d Codex keys + %d OpenAI-compat)",
		totalNewClients,
		authFileCount,
		geminiAPIKeyCount,
		vertexCompatAPIKeyCount,
		claudeAPIKeyCount,
		codexAPIKeyCount,
		openAICompatCount,
	)
}

// createClientFromFile creates a single client instance from a given token file path.
// createClientFromFile removed (legacy)

// addOrUpdateClient handles the addition or update of a single client.
func (w *Watcher) addOrUpdateClient(path string) {
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		log.Errorf("failed to read auth file %s: %v", filepath.Base(path), errRead)
		return
	}
	if len(data) == 0 {
		log.Debugf("ignoring empty auth file: %s", filepath.Base(path))
		return
	}

	sum := sha256.Sum256(data)
	curHash := hex.EncodeToString(sum[:])
	normalized := w.normalizeAuthPath(path)

	w.clientsMutex.Lock()

	cfg := w.config
	if cfg == nil {
		log.Error("config is nil, cannot add or update client")
		w.clientsMutex.Unlock()
		return
	}
	if prev, ok := w.lastAuthHashes[normalized]; ok && prev == curHash {
		log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(path))
		w.clientsMutex.Unlock()
		return
	}

	// Update hash cache
	w.lastAuthHashes[normalized] = curHash

	w.clientsMutex.Unlock() // Unlock before the callback

	w.refreshAuthState(false)

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback after add/update")
		w.reloadCallback(cfg)
	}
	w.persistAuthAsync(fmt.Sprintf("Sync auth %s", filepath.Base(path)), path)
}

// removeClient handles the removal of a single client.
func (w *Watcher) removeClient(path string) {
	normalized := w.normalizeAuthPath(path)
	w.clientsMutex.Lock()

	cfg := w.config
	delete(w.lastAuthHashes, normalized)

	w.clientsMutex.Unlock() // Release the lock before the callback

	w.refreshAuthState(false)

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback after removal")
		w.reloadCallback(cfg)
	}
	w.persistAuthAsync(fmt.Sprintf("Remove auth %s", filepath.Base(path)), path)
}

// SnapshotCombinedClients returns a snapshot of current combined clients.
// SnapshotCombinedClients removed

// SnapshotCoreAuths converts current clients snapshot into core auth entries.
func (w *Watcher) SnapshotCoreAuths() []*coreauth.Auth {
	out := make([]*coreauth.Auth, 0, 32)
	now := time.Now()
	idGen := newStableIDGenerator()
	// Also synthesize auth entries for OpenAI-compatibility providers directly from config
	w.clientsMutex.RLock()
	cfg := w.config
	w.clientsMutex.RUnlock()
	if cfg != nil {
		// Gemini official API keys -> synthesize auths
		for i := range cfg.GeminiKey {
			entry := cfg.GeminiKey[i]
			key := strings.TrimSpace(entry.APIKey)
			if key == "" {
				continue
			}
			prefix := strings.TrimSpace(entry.Prefix)
			base := strings.TrimSpace(entry.BaseURL)
			proxyURL := strings.TrimSpace(entry.ProxyURL)
			id, token := idGen.next("gemini:apikey", key, base)
			attrs := map[string]string{
				"source":  fmt.Sprintf("config:gemini[%s]", token),
				"api_key": key,
			}
			if base != "" {
				attrs["base_url"] = base
			}
			addConfigHeadersToAttrs(entry.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   "gemini",
				Label:      "gemini-apikey",
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				ProxyURL:   proxyURL,
				Attributes: attrs,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			applyAuthExcludedModelsMeta(a, cfg, entry.ExcludedModels, "apikey")
			out = append(out, a)
		}

		// Claude API keys -> synthesize auths
		for i := range cfg.ClaudeKey {
			ck := cfg.ClaudeKey[i]
			key := strings.TrimSpace(ck.APIKey)
			if key == "" {
				continue
			}
			prefix := strings.TrimSpace(ck.Prefix)
			base := strings.TrimSpace(ck.BaseURL)
			id, token := idGen.next("claude:apikey", key, base)
			attrs := map[string]string{
				"source":  fmt.Sprintf("config:claude[%s]", token),
				"api_key": key,
			}
			if base != "" {
				attrs["base_url"] = base
			}
			if hash := diff.ComputeClaudeModelsHash(ck.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(ck.Headers, attrs)
			proxyURL := strings.TrimSpace(ck.ProxyURL)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   "claude",
				Label:      "claude-apikey",
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				ProxyURL:   proxyURL,
				Attributes: attrs,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			applyAuthExcludedModelsMeta(a, cfg, ck.ExcludedModels, "apikey")
			out = append(out, a)
		}
		// Codex API keys -> synthesize auths
		for i := range cfg.CodexKey {
			ck := cfg.CodexKey[i]
			key := strings.TrimSpace(ck.APIKey)
			if key == "" {
				continue
			}
			prefix := strings.TrimSpace(ck.Prefix)
			id, token := idGen.next("codex:apikey", key, ck.BaseURL)
			attrs := map[string]string{
				"source":  fmt.Sprintf("config:codex[%s]", token),
				"api_key": key,
			}
			if ck.BaseURL != "" {
				attrs["base_url"] = ck.BaseURL
			}
			addConfigHeadersToAttrs(ck.Headers, attrs)
			proxyURL := strings.TrimSpace(ck.ProxyURL)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   "codex",
				Label:      "codex-apikey",
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				ProxyURL:   proxyURL,
				Attributes: attrs,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			applyAuthExcludedModelsMeta(a, cfg, ck.ExcludedModels, "apikey")
			out = append(out, a)
		}
		// Kiro (AWS CodeWhisperer) -> synthesize auths
		var kAuth *kiroauth.KiroAuth
		if len(cfg.KiroKey) > 0 {
			kAuth = kiroauth.NewKiroAuth(cfg)
		}
		for i := range cfg.KiroKey {
			kk := cfg.KiroKey[i]
			var accessToken, profileArn, refreshToken string

			// Try to load from token file first
			if kk.TokenFile != "" && kAuth != nil {
				tokenData, err := kAuth.LoadTokenFromFile(kk.TokenFile)
				if err != nil {
					log.Warnf("failed to load kiro token file %s: %v", kk.TokenFile, err)
				} else {
					accessToken = tokenData.AccessToken
					profileArn = tokenData.ProfileArn
					refreshToken = tokenData.RefreshToken
				}
			}

			// Override with direct config values if provided
			if kk.AccessToken != "" {
				accessToken = kk.AccessToken
			}
			if kk.ProfileArn != "" {
				profileArn = kk.ProfileArn
			}
			if kk.RefreshToken != "" {
				refreshToken = kk.RefreshToken
			}

			if accessToken == "" {
				log.Warnf("kiro config[%d] missing access_token, skipping", i)
				continue
			}

			// profileArn is optional for AWS Builder ID users
			id, token := idGen.next("kiro:token", accessToken, profileArn)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:kiro[%s]", token),
				"access_token": accessToken,
			}
			if profileArn != "" {
				attrs["profile_arn"] = profileArn
			}
			if kk.Region != "" {
				attrs["region"] = kk.Region
			}
			if kk.AgentTaskType != "" {
				attrs["agent_task_type"] = kk.AgentTaskType
			}
			if kk.PreferredEndpoint != "" {
				attrs["preferred_endpoint"] = kk.PreferredEndpoint
			} else if cfg.KiroPreferredEndpoint != "" {
				// Apply global default if not overridden by specific key
				attrs["preferred_endpoint"] = cfg.KiroPreferredEndpoint
			}
			if refreshToken != "" {
				attrs["refresh_token"] = refreshToken
			}
			proxyURL := strings.TrimSpace(kk.ProxyURL)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   "kiro",
				Label:      "kiro-token",
				Status:     coreauth.StatusActive,
				ProxyURL:   proxyURL,
				Attributes: attrs,
				CreatedAt:  now,
				UpdatedAt:  now,
			}

			if refreshToken != "" {
				if a.Metadata == nil {
					a.Metadata = make(map[string]any)
				}
				a.Metadata["refresh_token"] = refreshToken
			}

			out = append(out, a)
		}
		for i := range cfg.OpenAICompatibility {
			compat := &cfg.OpenAICompatibility[i]
			prefix := strings.TrimSpace(compat.Prefix)
			providerName := strings.ToLower(strings.TrimSpace(compat.Name))
			if providerName == "" {
				providerName = "openai-compatibility"
			}
			base := strings.TrimSpace(compat.BaseURL)

			// Handle new APIKeyEntries format (preferred)
			createdEntries := 0
			for j := range compat.APIKeyEntries {
				entry := &compat.APIKeyEntries[j]
				key := strings.TrimSpace(entry.APIKey)
				proxyURL := strings.TrimSpace(entry.ProxyURL)
				idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
				id, token := idGen.next(idKind, key, base, proxyURL)
				attrs := map[string]string{
					"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
					"base_url":     base,
					"compat_name":  compat.Name,
					"provider_key": providerName,
				}
				if key != "" {
					attrs["api_key"] = key
				}
				if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
					attrs["models_hash"] = hash
				}
				addConfigHeadersToAttrs(compat.Headers, attrs)
				a := &coreauth.Auth{
					ID:         id,
					Provider:   providerName,
					Label:      compat.Name,
					Prefix:     prefix,
					Status:     coreauth.StatusActive,
					ProxyURL:   proxyURL,
					Attributes: attrs,
					CreatedAt:  now,
					UpdatedAt:  now,
				}
				out = append(out, a)
				createdEntries++
			}
			if createdEntries == 0 {
				idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
				id, token := idGen.next(idKind, base)
				attrs := map[string]string{
					"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
					"base_url":     base,
					"compat_name":  compat.Name,
					"provider_key": providerName,
				}
				if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
					attrs["models_hash"] = hash
				}
				addConfigHeadersToAttrs(compat.Headers, attrs)
				a := &coreauth.Auth{
					ID:         id,
					Provider:   providerName,
					Label:      compat.Name,
					Prefix:     prefix,
					Status:     coreauth.StatusActive,
					Attributes: attrs,
					CreatedAt:  now,
					UpdatedAt:  now,
				}
				out = append(out, a)
			}
		}
	}

	// Process Vertex API key providers (Vertex-compatible endpoints)
	for i := range cfg.VertexCompatAPIKey {
		compat := &cfg.VertexCompatAPIKey[i]
		providerName := "vertex"
		base := strings.TrimSpace(compat.BaseURL)

		key := strings.TrimSpace(compat.APIKey)
		prefix := strings.TrimSpace(compat.Prefix)
		proxyURL := strings.TrimSpace(compat.ProxyURL)
		idKind := "vertex:apikey"
		id, token := idGen.next(idKind, key, base, proxyURL)
		attrs := map[string]string{
			"source":       fmt.Sprintf("config:vertex-apikey[%s]", token),
			"base_url":     base,
			"provider_key": providerName,
		}
		if key != "" {
			attrs["api_key"] = key
		}
		if hash := diff.ComputeVertexCompatModelsHash(compat.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(compat.Headers, attrs)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   providerName,
			Label:      "vertex-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		applyAuthExcludedModelsMeta(a, cfg, nil, "apikey")
		out = append(out, a)
	}

	// Also synthesize auth entries directly from auth files (for OAuth/file-backed providers)
	log.Debugf("SnapshotCoreAuths: scanning auth directory: %s", w.authDir)
	entries, readErr := os.ReadDir(w.authDir)
	if readErr != nil {
		log.Errorf("SnapshotCoreAuths: failed to read auth directory %s: %v", w.authDir, readErr)
	}
	log.Debugf("SnapshotCoreAuths: found %d entries in auth directory", len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		full := filepath.Join(w.authDir, name)
		data, err := os.ReadFile(full)
		if err != nil || len(data) == 0 {
			continue
		}
		var metadata map[string]any
		if err = json.Unmarshal(data, &metadata); err != nil {
			continue
		}
		t, _ := metadata["type"].(string)

		// Detect Kiro auth files by auth_method field (they don't have "type" field)
		if t == "" {
			if authMethod, _ := metadata["auth_method"].(string); authMethod == "builder-id" || authMethod == "social" {
				t = "kiro"
				log.Debugf("SnapshotCoreAuths: detected Kiro auth by auth_method: %s", name)
			}
		}

		if t == "" {
			log.Debugf("SnapshotCoreAuths: skipping file without type: %s", name)
			continue
		}
		log.Debugf("SnapshotCoreAuths: processing auth file: %s (type=%s)", name, t)
		provider := strings.ToLower(t)
		if provider == "gemini" {
			provider = "gemini-cli"
		}
		label := provider
		if email, _ := metadata["email"].(string); email != "" {
			label = email
		}
		// For Kiro, use provider field as label if available
		if provider == "kiro" {
			if kiroProvider, _ := metadata["provider"].(string); kiroProvider != "" {
				label = fmt.Sprintf("kiro-%s", strings.ToLower(kiroProvider))
			}
		}
		// Use relative path under authDir as ID to stay consistent with the file-based token store
		id := full
		if rel, errRel := filepath.Rel(w.authDir, full); errRel == nil && rel != "" {
			id = rel
		}

		proxyURL := ""
		if p, ok := metadata["proxy_url"].(string); ok {
			proxyURL = p
		}

		prefix := ""
		if rawPrefix, ok := metadata["prefix"].(string); ok {
			trimmed := strings.TrimSpace(rawPrefix)
			trimmed = strings.Trim(trimmed, "/")
			if trimmed != "" && !strings.Contains(trimmed, "/") {
				prefix = trimmed
			}
		}

		a := &coreauth.Auth{
			ID:       id,
			Provider: provider,
			Label:    label,
			Prefix:   prefix,
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"source": full,
				"path":   full,
			},
			ProxyURL:  proxyURL,
			Metadata:  metadata,
			CreatedAt: now,
			UpdatedAt: now,
		}
		// Set NextRefreshAfter for Kiro auth based on expires_at
		if provider == "kiro" {
			if expiresAtStr, ok := metadata["expires_at"].(string); ok && expiresAtStr != "" {
				if expiresAt, parseErr := time.Parse(time.RFC3339, expiresAtStr); parseErr == nil {
					// Refresh 30 minutes before expiry
					a.NextRefreshAfter = expiresAt.Add(-30 * time.Minute)
				}
			}

			// Apply global preferred endpoint setting if not present in metadata
			if cfg.KiroPreferredEndpoint != "" {
				// Check if already set in metadata (which takes precedence in executor)
				if _, hasMeta := metadata["preferred_endpoint"]; !hasMeta {
					if a.Attributes == nil {
						a.Attributes = make(map[string]string)
					}
					a.Attributes["preferred_endpoint"] = cfg.KiroPreferredEndpoint
				}
			}
		}

		applyAuthExcludedModelsMeta(a, cfg, nil, "oauth")
		if provider == "gemini-cli" {
			if virtuals := synthesizeGeminiVirtualAuths(a, metadata, now); len(virtuals) > 0 {
				for _, v := range virtuals {
					applyAuthExcludedModelsMeta(v, cfg, nil, "oauth")
				}
				out = append(out, a)
				out = append(out, virtuals...)
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

func synthesizeGeminiVirtualAuths(primary *coreauth.Auth, metadata map[string]any, now time.Time) []*coreauth.Auth {
	if primary == nil || metadata == nil {
		return nil
	}
	projects := splitGeminiProjectIDs(metadata)
	if len(projects) <= 1 {
		return nil
	}
	email, _ := metadata["email"].(string)
	shared := geminicli.NewSharedCredential(primary.ID, email, metadata, projects)
	primary.Disabled = true
	primary.Status = coreauth.StatusDisabled
	primary.Runtime = shared
	if primary.Attributes == nil {
		primary.Attributes = make(map[string]string)
	}
	primary.Attributes["gemini_virtual_primary"] = "true"
	primary.Attributes["virtual_children"] = strings.Join(projects, ",")
	source := primary.Attributes["source"]
	authPath := primary.Attributes["path"]
	originalProvider := primary.Provider
	if originalProvider == "" {
		originalProvider = "gemini-cli"
	}
	label := primary.Label
	if label == "" {
		label = originalProvider
	}
	virtuals := make([]*coreauth.Auth, 0, len(projects))
	for _, projectID := range projects {
		attrs := map[string]string{
			"runtime_only":           "true",
			"gemini_virtual_parent":  primary.ID,
			"gemini_virtual_project": projectID,
		}
		if source != "" {
			attrs["source"] = source
		}
		if authPath != "" {
			attrs["path"] = authPath
		}
		metadataCopy := map[string]any{
			"email":             email,
			"project_id":        projectID,
			"virtual":           true,
			"virtual_parent_id": primary.ID,
			"type":              metadata["type"],
		}
		proxy := strings.TrimSpace(primary.ProxyURL)
		if proxy != "" {
			metadataCopy["proxy_url"] = proxy
		}
		virtual := &coreauth.Auth{
			ID:         buildGeminiVirtualID(primary.ID, projectID),
			Provider:   originalProvider,
			Label:      fmt.Sprintf("%s [%s]", label, projectID),
			Status:     coreauth.StatusActive,
			Attributes: attrs,
			Metadata:   metadataCopy,
			ProxyURL:   primary.ProxyURL,
			Prefix:     primary.Prefix,
			CreatedAt:  now,
			UpdatedAt:  now,
			Runtime:    geminicli.NewVirtualCredential(projectID, shared),
		}
		virtuals = append(virtuals, virtual)
	}
	return virtuals
}

func splitGeminiProjectIDs(metadata map[string]any) []string {
	raw, _ := metadata["project_id"].(string)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func buildGeminiVirtualID(baseID, projectID string) string {
	project := strings.TrimSpace(projectID)
	if project == "" {
		project = "project"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_")
	return fmt.Sprintf("%s::%s", baseID, replacer.Replace(project))
}

// buildCombinedClientMap merges file-based clients with API key clients from the cache.
// buildCombinedClientMap removed

// unregisterClientWithReason attempts to call client-specific unregister hooks with context.
// unregisterClientWithReason removed

// loadFileClients scans the auth directory and creates clients from .json files.
func (w *Watcher) loadFileClients(cfg *config.Config) int {
	authFileCount := 0
	successfulAuthCount := 0

	authDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir)
	if errResolveAuthDir != nil {
		log.Errorf("failed to resolve auth directory: %v", errResolveAuthDir)
		return 0
	}
	if authDir == "" {
		return 0
	}

	errWalk := filepath.Walk(authDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			log.Debugf("error accessing path %s: %v", path, err)
			return err
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".json") {
			authFileCount++
			log.Debugf("processing auth file %d: %s", authFileCount, filepath.Base(path))
			// Count readable JSON files as successful auth entries
			if data, errCreate := os.ReadFile(path); errCreate == nil && len(data) > 0 {
				successfulAuthCount++
			}
		}
		return nil
	})

	if errWalk != nil {
		log.Errorf("error walking auth directory: %v", errWalk)
	}
	log.Debugf("auth directory scan complete - found %d .json files, %d readable", authFileCount, successfulAuthCount)
	return authFileCount
}

func BuildAPIKeyClients(cfg *config.Config) (int, int, int, int, int) {
	geminiAPIKeyCount := 0
	vertexCompatAPIKeyCount := 0
	claudeAPIKeyCount := 0
	codexAPIKeyCount := 0
	openAICompatCount := 0

	if len(cfg.GeminiKey) > 0 {
		// Stateless executor handles Gemini API keys; avoid constructing legacy clients.
		geminiAPIKeyCount += len(cfg.GeminiKey)
	}
	if len(cfg.VertexCompatAPIKey) > 0 {
		vertexCompatAPIKeyCount += len(cfg.VertexCompatAPIKey)
	}
	if len(cfg.ClaudeKey) > 0 {
		claudeAPIKeyCount += len(cfg.ClaudeKey)
	}
	if len(cfg.CodexKey) > 0 {
		codexAPIKeyCount += len(cfg.CodexKey)
	}
	if len(cfg.OpenAICompatibility) > 0 {
		// Do not construct legacy clients for OpenAI-compat providers; these are handled by the stateless executor.
		for _, compatConfig := range cfg.OpenAICompatibility {
			openAICompatCount += len(compatConfig.APIKeyEntries)
		}
	}
	return geminiAPIKeyCount, vertexCompatAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, openAICompatCount
}

func addConfigHeadersToAttrs(headers map[string]string, attrs map[string]string) {
	if len(headers) == 0 || attrs == nil {
		return
	}
	for hk, hv := range headers {
		key := strings.TrimSpace(hk)
		val := strings.TrimSpace(hv)
		if key == "" || val == "" {
			continue
		}
		attrs["header:"+key] = val
	}
}
