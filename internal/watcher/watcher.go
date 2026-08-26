// Package watcher watches config/auth files and triggers hot reloads.
// It supports cross-platform fsnotify event handling.
package watcher

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	"gopkg.in/yaml.v3"

	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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
	configPath        string
	configTargets     []string
	authDir           string
	config            *config.Config
	clientsMutex      sync.RWMutex
	authRescanMu      sync.Mutex
	configReloadMu    sync.Mutex
	configReloadTimer *time.Timer
	reloadRunMu       sync.Mutex
	reloadTestHook    func()
	startTestHook     func()
	completion        configCompletion
	completionActive  atomic.Bool
	serverUpdateMu    sync.Mutex
	serverUpdateTimer *time.Timer
	serverUpdateLast  time.Time
	serverUpdatePend  bool
	stopped           atomic.Bool
	reloadCallback    func(*config.Config)
	watcher           *fsnotify.Watcher
	lastAuthHashes    map[string]string
	lastAuthContents  map[string]*coreauth.Auth
	fileAuthsByPath   map[string]map[string]*coreauth.Auth
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
	persistConfigWG   sync.WaitGroup
	pluginAuthParser  synthesizer.PluginAuthParser
	mirroredAuthDir   string
	oldConfigYaml     []byte
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
func NewWatcher(configPath, authDir string, reloadCallback func(*config.Config)) (*Watcher, error) {
	watcher, errNewWatcher := fsnotify.NewWatcher()
	if errNewWatcher != nil {
		return nil, errNewWatcher
	}
	configTargets := resolveConfigTargets(configPath)
	completionWatcher, errCompletionWatcher := newConfigCompletionWatcher(configTargets...)
	if errCompletionWatcher != nil {
		_ = watcher.Close()
		return nil, errCompletionWatcher
	}
	w := &Watcher{
		configPath:      configPath,
		configTargets:   configTargets,
		authDir:         authDir,
		reloadCallback:  reloadCallback,
		watcher:         watcher,
		completion:      completionWatcher,
		lastAuthHashes:  make(map[string]string),
		fileAuthsByPath: make(map[string]map[string]*coreauth.Auth),
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
	w.stopServerUpdateTimer()
	var completionErr error
	if w.completion != nil {
		completionErr = w.completion.Close()
	}
	watchErr := w.watcher.Close()
	w.reloadRunMu.Lock()
	w.reloadRunMu.Unlock()
	w.persistConfigWG.Wait()
	if completionErr != nil {
		return completionErr
	}
	return watchErr
}

// SetConfig updates the current configuration
func (w *Watcher) SetConfig(cfg *config.Config) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.config = cfg
	w.oldConfigYaml, _ = yaml.Marshal(cfg)
}

// SetPluginAuthParser updates the plugin auth parser used for file auth synthesis.
func (w *Watcher) SetPluginAuthParser(parser synthesizer.PluginAuthParser) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.pluginAuthParser = parser
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

// DispatchPersistedAuthUpdate pushes already-persisted file auth updates through the watcher queue.
// Returns true if the update was enqueued; false if no queue is configured.
func (w *Watcher) DispatchPersistedAuthUpdate(update AuthUpdate) bool {
	return w.dispatchPersistedAuthUpdate(update)
}

// SnapshotCoreAuths converts current clients snapshot into core auth entries.
func (w *Watcher) SnapshotCoreAuths() []*coreauth.Auth {
	w.clientsMutex.RLock()
	cfg := w.config
	authDir := w.authDir
	parser := w.pluginAuthParser
	w.clientsMutex.RUnlock()
	return snapshotCoreAuths(cfg, authDir, parser)
}
