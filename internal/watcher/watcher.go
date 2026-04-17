// Package watcher watches config/auth files and triggers hot reloads.
// It supports cross-platform fsnotify event handling.
package watcher

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"gopkg.in/yaml.v3"

	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

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
	configPath            string
	configDir             string
	loggingConfigPath     string
	authDir               string
	config                *config.Config
	clientsMutex          sync.RWMutex
	configReloadMu        sync.Mutex
	configReloadTimer     *time.Timer
	loggingReloadMu       sync.Mutex
	loggingReloadTimer    *time.Timer
	serverUpdateMu        sync.Mutex
	serverUpdateTimer     *time.Timer
	serverUpdateLast      time.Time
	serverUpdatePend      bool
	stopped               atomic.Bool
	reloadCallback        func(*config.Config)
	loggingReloadCallback func()
	watcher               *fsnotify.Watcher
	lastAuthHashes        map[string]string
	lastAuthContents      map[string]*coreauth.Auth
	fileAuthsByPath       map[string]map[string]*coreauth.Auth
	lastRemoveTimes       map[string]time.Time
	lastConfigHash        string
	lastLoggingConfigHash string
	authQueue             chan<- AuthUpdate
	currentAuths          map[string]*coreauth.Auth
	runtimeAuths          map[string]*coreauth.Auth
	dispatchMu            sync.Mutex
	dispatchCond          *sync.Cond
	pendingUpdates        map[string]AuthUpdate
	pendingOrder          []string
	dispatchCancel        context.CancelFunc
	storePersister        storePersister
	mirroredAuthDir       string
	oldConfigYaml         []byte
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
	serverUpdateDebounce     = 1 * time.Second
)

// NewWatcher creates a new file watcher instance
func NewWatcher(configPath, authDir string, reloadCallback func(*config.Config), loggingReloadCallback func()) (*Watcher, error) {
	watcher, errNewWatcher := fsnotify.NewWatcher()
	if errNewWatcher != nil {
		return nil, errNewWatcher
	}
	configDir := filepath.Dir(strings.TrimSpace(configPath))
	w := &Watcher{
		configPath:            configPath,
		configDir:             configDir,
		loggingConfigPath:     logging.ResolveLoggingConfigPath(configPath),
		authDir:               authDir,
		reloadCallback:        reloadCallback,
		loggingReloadCallback: loggingReloadCallback,
		watcher:               watcher,
		lastAuthHashes:        make(map[string]string),
		fileAuthsByPath:       make(map[string]map[string]*coreauth.Auth),
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
	return w.start(ctx)
}

// Stop stops the file watcher
func (w *Watcher) Stop() error {
	w.stopped.Store(true)
	w.stopDispatch()
	w.stopConfigReloadTimer()
	w.stopLoggingReloadTimer()
	w.stopServerUpdateTimer()
	return w.watcher.Close()
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
	w.setAuthUpdateQueue(queue)
}

// DispatchRuntimeAuthUpdate allows external runtime providers (e.g., websocket-driven auths)
// to push auth updates through the same queue used by file/config watchers.
// Returns true if the update was enqueued; false if no queue is configured.
func (w *Watcher) DispatchRuntimeAuthUpdate(update AuthUpdate) bool {
	return w.dispatchRuntimeAuthUpdate(update)
}

// SnapshotCoreAuths converts current clients snapshot into core auth entries.
func (w *Watcher) SnapshotCoreAuths() []*coreauth.Auth {
	w.clientsMutex.RLock()
	cfg := w.config
	w.clientsMutex.RUnlock()
	return snapshotCoreAuths(cfg, w.authDir)
}

// SnapshotAuths returns the watcher's current auth snapshot when available.
// Before the incremental queue is attached, this reflects the startup baseline
// captured by reloadClients without forcing a re-synthesis pass.
func (w *Watcher) SnapshotAuths() []*coreauth.Auth {
	w.clientsMutex.RLock()
	if len(w.currentAuths) > 0 {
		auths := make([]*coreauth.Auth, 0, len(w.currentAuths))
		for _, auth := range w.currentAuths {
			if auth != nil {
				auths = append(auths, auth.Clone())
			}
		}
		w.clientsMutex.RUnlock()
		return auths
	}
	cfg := w.config
	authDir := w.authDir
	w.clientsMutex.RUnlock()
	return snapshotCoreAuths(cfg, authDir)
}
