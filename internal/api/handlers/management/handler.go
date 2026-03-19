// Package management provides the management API handlers and middleware
// for configuring the server and managing auth files.
package management

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

type attemptInfo struct {
	count        int
	blockedUntil time.Time
	lastActivity time.Time // track last activity for cleanup
}

// attemptCleanupInterval controls how often stale IP entries are purged
const attemptCleanupInterval = 1 * time.Hour

// attemptMaxIdleTime controls how long an IP can be idle before cleanup
const attemptMaxIdleTime = 2 * time.Hour

// Handler aggregates config reference, persistence path and helpers.
type Handler struct {
	cfg                 *config.Config
	configFilePath      string
	mu                  sync.Mutex
	stateMu             sync.RWMutex
	attemptsMu          sync.Mutex
	failedAttempts      map[string]*attemptInfo // keyed by client IP
	authManager         *coreauth.Manager
	usageStats          *usage.RequestStatistics
	tokenStore          coreauth.Store
	localPassword       string
	allowRemoteOverride bool
	envSecret           string
	logDir              string
	postAuthHook        coreauth.PostAuthHook
}

type runtimeStateSnapshot struct {
	cfg                 *config.Config
	configFilePath      string
	authManager         *coreauth.Manager
	usageStats          *usage.RequestStatistics
	tokenStore          coreauth.Store
	localPassword       string
	allowRemoteOverride bool
	envSecret           string
	logDir              string
	postAuthHook        coreauth.PostAuthHook
}

type authDirProvider interface {
	AuthDir() string
}

// ResolveEffectiveAuthDir returns the runtime auth directory used by file-based flows.
// Stores with mirrored workspaces may override the configured auth dir.
func ResolveEffectiveAuthDir(configAuthDir string, store coreauth.Store) string {
	if provider, ok := store.(authDirProvider); ok {
		if dir := strings.TrimSpace(provider.AuthDir()); dir != "" {
			return dir
		}
	}
	return strings.TrimSpace(configAuthDir)
}

// NewHandler creates a new management handler instance.
func NewHandler(cfg *config.Config, configFilePath string, manager *coreauth.Manager) *Handler {
	envSecret, _ := os.LookupEnv("MANAGEMENT_PASSWORD")
	envSecret = strings.TrimSpace(envSecret)

	h := &Handler{
		cfg:                 cfg,
		configFilePath:      configFilePath,
		failedAttempts:      make(map[string]*attemptInfo),
		authManager:         manager,
		usageStats:          usage.GetRequestStatistics(),
		tokenStore:          sdkAuth.GetTokenStore(),
		allowRemoteOverride: envSecret != "",
		envSecret:           envSecret,
	}
	h.startAttemptCleanup()
	return h
}

// startAttemptCleanup launches a background goroutine that periodically
// removes stale IP entries from failedAttempts to prevent memory leaks.
func (h *Handler) startAttemptCleanup() {
	go func() {
		ticker := time.NewTicker(attemptCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			h.purgeStaleAttempts()
		}
	}()
}

// purgeStaleAttempts removes IP entries that have been idle beyond attemptMaxIdleTime
// and whose ban (if any) has expired.
func (h *Handler) purgeStaleAttempts() {
	now := time.Now()
	h.attemptsMu.Lock()
	defer h.attemptsMu.Unlock()
	for ip, ai := range h.failedAttempts {
		// Skip if still banned
		if !ai.blockedUntil.IsZero() && now.Before(ai.blockedUntil) {
			continue
		}
		// Remove if idle too long
		if now.Sub(ai.lastActivity) > attemptMaxIdleTime {
			delete(h.failedAttempts, ip)
		}
	}
}

// NewHandler creates a new management handler instance.
func NewHandlerWithoutConfigFilePath(cfg *config.Config, manager *coreauth.Manager) *Handler {
	return NewHandler(cfg, "", manager)
}

// StateMiddleware is kept for compatibility but no longer serializes the entire
// request lifecycle. Handlers should use short-lived snapshots and mutation helpers instead.
func (h *Handler) StateMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
	}
}

// SetConfig updates the in-memory config reference when the server hot-reloads.
func (h *Handler) SetConfig(cfg *config.Config) {
	h.stateMu.Lock()
	h.cfg = cfg
	h.stateMu.Unlock()
}

// SetAuthManager updates the auth manager reference used by management endpoints.
func (h *Handler) SetAuthManager(manager *coreauth.Manager) {
	h.stateMu.Lock()
	h.authManager = manager
	h.stateMu.Unlock()
}

// SetUsageStatistics allows replacing the usage statistics reference.
func (h *Handler) SetUsageStatistics(stats *usage.RequestStatistics) {
	h.stateMu.Lock()
	h.usageStats = stats
	h.stateMu.Unlock()
}

// SetLocalPassword configures the runtime-local password accepted for localhost requests.
func (h *Handler) SetLocalPassword(password string) {
	h.stateMu.Lock()
	h.localPassword = password
	h.stateMu.Unlock()
}

// SetLogDirectory updates the directory where main.log should be looked up.
func (h *Handler) SetLogDirectory(dir string) {
	if dir == "" {
		return
	}
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	h.stateMu.Lock()
	h.logDir = dir
	h.stateMu.Unlock()
}

// SetPostAuthHook registers a hook to be called after auth record creation but before persistence.
func (h *Handler) SetPostAuthHook(hook coreauth.PostAuthHook) {
	h.stateMu.Lock()
	h.postAuthHook = hook
	h.stateMu.Unlock()
}

func cloneConfig(cfg *config.Config) (*config.Config, error) {
	if cfg == nil {
		return nil, nil
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var cloned config.Config
	if err := yaml.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}

func (h *Handler) runtimeSnapshot() (*runtimeStateSnapshot, error) {
	if h == nil {
		return &runtimeStateSnapshot{}, nil
	}
	h.stateMu.RLock()
	cfg, err := cloneConfig(h.cfg)
	snapshot := &runtimeStateSnapshot{
		cfg:                 cfg,
		configFilePath:      h.configFilePath,
		authManager:         h.authManager,
		usageStats:          h.usageStats,
		tokenStore:          h.tokenStore,
		localPassword:       h.localPassword,
		allowRemoteOverride: h.allowRemoteOverride,
		envSecret:           h.envSecret,
		logDir:              h.logDir,
		postAuthHook:        h.postAuthHook,
	}
	h.stateMu.RUnlock()
	if snapshot.tokenStore == nil {
		snapshot.tokenStore = sdkAuth.GetTokenStore()
	}
	if err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (h *Handler) configSnapshot() (*config.Config, error) {
	snapshot, err := h.runtimeSnapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.cfg, nil
}

func effectiveAuthDirFromSnapshot(cfg *config.Config, store coreauth.Store) string {
	if cfg == nil {
		return ResolveEffectiveAuthDir("", store)
	}
	return ResolveEffectiveAuthDir(cfg.AuthDir, store)
}

func (h *Handler) effectiveAuthDir() string {
	snapshot, err := h.runtimeSnapshot()
	if err != nil {
		return ""
	}
	return effectiveAuthDirFromSnapshot(snapshot.cfg, snapshot.tokenStore)
}

func (h *Handler) registerOAuthSession(state, provider string) string {
	snapshot, err := h.runtimeSnapshot()
	if err != nil {
		return ""
	}
	authDir := effectiveAuthDirFromSnapshot(snapshot.cfg, snapshot.tokenStore)
	RegisterOAuthSession(state, provider, authDir)
	return authDir
}

// Middleware enforces access control for management endpoints.
// All requests (local and remote) require a valid management key.
// Additionally, remote access requires allow-remote-management=true.
func (h *Handler) Middleware() gin.HandlerFunc {
	const maxFailures = 5
	const banDuration = 30 * time.Minute

	return func(c *gin.Context) {
		c.Header("X-CPA-VERSION", buildinfo.Version)
		c.Header("X-CPA-COMMIT", buildinfo.Commit)
		c.Header("X-CPA-BUILD-DATE", buildinfo.BuildDate)

		clientIP := c.ClientIP()
		localClient := clientIP == "127.0.0.1" || clientIP == "::1"
		h.stateMu.RLock()
		cfg := h.cfg
		localPassword := h.localPassword
		allowRemoteOverride := h.allowRemoteOverride
		envSecret := h.envSecret
		h.stateMu.RUnlock()
		var (
			allowRemote bool
			secretHash  string
		)
		if cfg != nil {
			allowRemote = cfg.RemoteManagement.AllowRemote
			secretHash = cfg.RemoteManagement.SecretKey
		}
		if allowRemoteOverride {
			allowRemote = true
		}

		fail := func() {}
		if !localClient {
			h.attemptsMu.Lock()
			ai := h.failedAttempts[clientIP]
			if ai != nil {
				if !ai.blockedUntil.IsZero() {
					if time.Now().Before(ai.blockedUntil) {
						remaining := time.Until(ai.blockedUntil).Round(time.Second)
						h.attemptsMu.Unlock()
						c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("IP banned due to too many failed attempts. Try again in %s", remaining)})
						return
					}
					// Ban expired, reset state
					ai.blockedUntil = time.Time{}
					ai.count = 0
				}
			}
			h.attemptsMu.Unlock()

			if !allowRemote {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "remote management disabled"})
				return
			}

			fail = func() {
				h.attemptsMu.Lock()
				aip := h.failedAttempts[clientIP]
				if aip == nil {
					aip = &attemptInfo{}
					h.failedAttempts[clientIP] = aip
				}
				aip.count++
				aip.lastActivity = time.Now()
				if aip.count >= maxFailures {
					aip.blockedUntil = time.Now().Add(banDuration)
					aip.count = 0
				}
				h.attemptsMu.Unlock()
			}
		}
		if secretHash == "" && envSecret == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "remote management key not set"})
			return
		}

		// Accept either Authorization: Bearer <key> or X-Management-Key
		var provided string
		if ah := c.GetHeader("Authorization"); ah != "" {
			parts := strings.SplitN(ah, " ", 2)
			if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
				provided = parts[1]
			} else {
				provided = ah
			}
		}
		if provided == "" {
			provided = c.GetHeader("X-Management-Key")
		}

		if provided == "" {
			if !localClient {
				fail()
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing management key"})
			return
		}

		if localClient {
			if localPassword != "" {
				if subtle.ConstantTimeCompare([]byte(provided), []byte(localPassword)) == 1 {
					c.Next()
					return
				}
			}
		}

		if envSecret != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(envSecret)) == 1 {
			if !localClient {
				h.attemptsMu.Lock()
				if ai := h.failedAttempts[clientIP]; ai != nil {
					ai.count = 0
					ai.blockedUntil = time.Time{}
				}
				h.attemptsMu.Unlock()
			}
			c.Next()
			return
		}

		if secretHash == "" || bcrypt.CompareHashAndPassword([]byte(secretHash), []byte(provided)) != nil {
			if !localClient {
				fail()
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid management key"})
			return
		}

		if !localClient {
			h.attemptsMu.Lock()
			if ai := h.failedAttempts[clientIP]; ai != nil {
				ai.count = 0
				ai.blockedUntil = time.Time{}
			}
			h.attemptsMu.Unlock()
		}

		c.Next()
	}
}

func (h *Handler) persistConfig(c *gin.Context, cfg *config.Config) bool {
	if cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "configuration unavailable"})
		return false
	}
	// Preserve comments when writing
	snapshot, err := h.runtimeSnapshot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to snapshot config: %v", err)})
		return false
	}
	if err := config.SaveConfigPreserveComments(snapshot.configFilePath, cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to save config: %v", err)})
		return false
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
	return true
}

// persist saves the current in-memory config to disk.
func (h *Handler) persist(c *gin.Context) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	cfg, err := h.configSnapshot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to snapshot config: %v", err)})
		return false
	}
	return h.persistConfig(c, cfg)
}

func (h *Handler) applyConfigMutation(c *gin.Context, mutate func(*config.Config) error) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	current, err := h.configSnapshot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to snapshot config: %v", err)})
		return false
	}
	if current == nil {
		current = &config.Config{}
	}
	nextCfg, err := cloneConfig(current)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to clone config: %v", err)})
		return false
	}
	if err := mutate(nextCfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return false
	}
	if !h.persistConfig(c, nextCfg) {
		return false
	}
	h.stateMu.Lock()
	h.cfg = nextCfg
	h.stateMu.Unlock()
	return true
}

// Helper methods for simple types
func (h *Handler) updateBoolField(c *gin.Context, set func(*config.Config, bool)) {
	var body struct {
		Value *bool `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		set(cfg, *body.Value)
		return nil
	})
}

func (h *Handler) updateIntField(c *gin.Context, set func(*config.Config, int) error) {
	var body struct {
		Value *int `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		return set(cfg, *body.Value)
	})
}

func (h *Handler) updateStringField(c *gin.Context, set func(*config.Config, string)) {
	var body struct {
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.applyConfigMutation(c, func(cfg *config.Config) error {
		set(cfg, *body.Value)
		return nil
	})
}
