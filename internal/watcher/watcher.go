// Package watcher watches config/auth files and triggers hot reloads.
// It supports cross-platform fsnotify event handling.
package watcher

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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
	configPath        string
	authDir           string
	config            *config.Config
	clientsMutex      sync.RWMutex
	configReloadMu    sync.Mutex
	configReloadTimer *time.Timer
	reloadCallback    func(*config.Config)
	watcher           *fsnotify.Watcher
	lastAuthHashes    map[string]string
	lastAuthContents  map[string]*coreauth.Auth
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
	return w.start(ctx)
}

// Stop stops the file watcher
func (w *Watcher) Stop() error {
	w.stopDispatch()
	w.stopConfigReloadTimer()
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

// NotifyTokenRefreshed 处理后台刷新器的 token 更新通知
// 当后台刷新器成功刷新 token 后调用此方法，更新内存中的 Auth 对象
// tokenID: token 文件名（如 kiro-xxx.json）
// accessToken: 新的 access token
// refreshToken: 新的 refresh token
// expiresAt: 新的过期时间
func (w *Watcher) NotifyTokenRefreshed(tokenID, accessToken, refreshToken, expiresAt string) {
	if w == nil {
		return
	}

	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()

	// 遍历 currentAuths，找到匹配的 Auth 并更新
	updated := false
	for id, auth := range w.currentAuths {
		if auth == nil || auth.Metadata == nil {
			continue
		}

		// 检查是否是 kiro 类型的 auth
		authType, _ := auth.Metadata["type"].(string)
		if authType != "kiro" {
			continue
		}

		// 多种匹配方式，解决不同来源的 auth 对象字段差异
		matched := false

		// 1. 通过 auth.ID 匹配（ID 可能包含文件名）
		if !matched && auth.ID != "" {
			if auth.ID == tokenID || strings.HasSuffix(auth.ID, "/"+tokenID) || strings.HasSuffix(auth.ID, "\\"+tokenID) {
				matched = true
			}
			// ID 可能是 "kiro-xxx" 格式（无扩展名），tokenID 是 "kiro-xxx.json"
			if !matched && strings.TrimSuffix(tokenID, ".json") == auth.ID {
				matched = true
			}
		}

		// 2. 通过 auth.Attributes["path"] 匹配
		if !matched && auth.Attributes != nil {
			if authPath := auth.Attributes["path"]; authPath != "" {
				// 提取文件名部分进行比较
				pathBase := authPath
				if idx := strings.LastIndexAny(authPath, "/\\"); idx >= 0 {
					pathBase = authPath[idx+1:]
				}
				if pathBase == tokenID || strings.TrimSuffix(pathBase, ".json") == strings.TrimSuffix(tokenID, ".json") {
					matched = true
				}
			}
		}

		// 3. 通过 auth.FileName 匹配（原有逻辑）
		if !matched && auth.FileName != "" {
			if auth.FileName == tokenID || strings.HasSuffix(auth.FileName, "/"+tokenID) || strings.HasSuffix(auth.FileName, "\\"+tokenID) {
				matched = true
			}
		}

		if matched {
			// 更新内存中的 token
			auth.Metadata["access_token"] = accessToken
			auth.Metadata["refresh_token"] = refreshToken
			auth.Metadata["expires_at"] = expiresAt
			auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
			auth.UpdatedAt = time.Now()
			auth.LastRefreshedAt = time.Now()

			log.Infof("watcher: updated in-memory auth for token %s (auth ID: %s)", tokenID, id)
			updated = true

			// 同时更新 runtimeAuths 中的副本（如果存在）
			if w.runtimeAuths != nil {
				if runtimeAuth, ok := w.runtimeAuths[id]; ok && runtimeAuth != nil {
					if runtimeAuth.Metadata == nil {
						runtimeAuth.Metadata = make(map[string]any)
					}
					runtimeAuth.Metadata["access_token"] = accessToken
					runtimeAuth.Metadata["refresh_token"] = refreshToken
					runtimeAuth.Metadata["expires_at"] = expiresAt
					runtimeAuth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
					runtimeAuth.UpdatedAt = time.Now()
					runtimeAuth.LastRefreshedAt = time.Now()
				}
			}

			// 发送更新通知到 authQueue
			if w.authQueue != nil {
				go func(authClone *coreauth.Auth) {
					update := AuthUpdate{
						Action: AuthUpdateActionModify,
						ID:     authClone.ID,
						Auth:   authClone,
					}
					w.dispatchAuthUpdates([]AuthUpdate{update})
				}(auth.Clone())
			}
		}
	}

	if !updated {
		log.Debugf("watcher: no matching auth found for token %s, will be picked up on next file scan", tokenID)
	}
}
