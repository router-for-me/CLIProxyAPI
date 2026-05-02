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
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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
//
// Config storage uses atomic.Pointer[config.Config] so:
//   - Get* handlers do a lock-free Load to obtain the current snapshot.
//   - Put*/Patch*/Delete* handlers go through applyConfigChange, which
//     clones the snapshot, applies the mutation, persists to disk, then
//     atomic-swaps. Concurrent readers never observe a half-mutated
//     snapshot.
//   - SetConfig (called from Server.UpdateClients on filesystem hot-reload)
//     atomically replaces the snapshot.
//
// The snapshot pointer returned from cfg() is treated as immutable by all
// readers — any mutation MUST go through applyConfigChange so the new
// snapshot is published as a fresh allocation.
type Handler struct {
	cfgPtr atomic.Pointer[config.Config]
	commit func(*config.Config) // optional Server-side fan-out hook
	// authManagerPtr holds the auth manager behind atomic.Pointer so SetAuthManager
	// can run lock-free. The previous mutex-guarded form deadlocked when
	// applyConfigChange held h.mu and the commit hook reached SetAuthManager
	// transitively via Server.UpdateClients (Codex Phase C review BLOCKER #1).
	// Reads scattered across the management package always observed an
	// unsynchronized h.authManager(); the lock was paranoia, not correctness.
	authManagerPtr      atomic.Pointer[coreauth.Manager]
	configFilePath      string
	mu                  sync.Mutex
	attemptsMu          sync.Mutex
	failedAttempts      map[string]*attemptInfo // keyed by client IP
	tokenStore          coreauth.Store
	localPassword       string
	allowRemoteOverride bool
	envSecret           string
	logDir              string
	postAuthHook        coreauth.PostAuthHook
}

// authManager returns the current auth manager snapshot. Callers must
// nil-check the result.
func (h *Handler) authManager() *coreauth.Manager {
	if h == nil {
		return nil
	}
	return h.authManagerPtr.Load()
}

// NewHandler creates a new management handler instance.
func NewHandler(cfg *config.Config, configFilePath string, manager *coreauth.Manager) *Handler {
	envSecret, _ := os.LookupEnv("MANAGEMENT_PASSWORD")
	envSecret = strings.TrimSpace(envSecret)

	h := &Handler{
		configFilePath:      configFilePath,
		failedAttempts:      make(map[string]*attemptInfo),
		tokenStore:          sdkAuth.GetTokenStore(),
		allowRemoteOverride: envSecret != "",
		envSecret:           envSecret,
	}
	h.cfgPtr.Store(cfg)
	if manager != nil {
		h.authManagerPtr.Store(manager)
	}
	h.startAttemptCleanup()
	return h
}

// cfg returns the current config snapshot. Returns nil if no config has been
// loaded yet. Call once at request entry; readers should treat the returned
// pointer as immutable for the duration of their work.
func (h *Handler) cfg() *config.Config {
	if h == nil {
		return nil
	}
	return h.cfgPtr.Load()
}

// SetConfigCommitter wires a Server-side fan-out hook that's invoked after a
// successful clone-modify-persist-swap. The hook receives the new snapshot
// AFTER it has been atomically published, and is responsible for triggering
// downstream work like log-level changes, request-logger toggle, AMP module
// hot-reload, etc. (typically Server.UpdateClients).
//
// If no committer is set, applyConfigChange still publishes the snapshot
// locally — useful for tests that want behavior-equivalent persistence
// without a full Server.
func (h *Handler) SetConfigCommitter(commit func(*config.Config)) {
	if h == nil {
		return
	}
	h.commit = commit
}

// cloneConfigSnapshot returns a deep copy of cur via YAML round-trip. Used by
// applyConfigChange so mutations to the clone don't affect concurrent readers
// holding the prior snapshot.
//
// YAML round-trip is the persistence format used by SaveConfigPreserveComments,
// so it preserves every field that survives disk persistence — critically
// including `json:"-"` fields like Host, Port, RemoteManagement, and AuthDir.
// A prior fix (Codex Phase C IMPORTANT #7) tried JSON round-trip to surface
// marshal errors and avoid yaml.v3 nil-vs-empty canonicalisation, but it
// dropped those `json:"-"` fields and would have zeroed Host/Port/auth-dir/
// remote-management on every mgmt edit (Codex re-review BLOCKER #2). YAML
// round-trip is the only safe choice here.
//
// We keep the error-return signature so applyConfigChange surfaces a 500
// instead of silently persisting an empty config when marshal/unmarshal
// fails — the original concern from IMPORTANT #7 still stands.
//
// Mgmt writes already pay disk I/O for the persist step, so the round-trip
// cost is negligible.
func cloneConfigSnapshot(cur *config.Config) (*config.Config, error) {
	if cur == nil {
		return &config.Config{}, nil
	}
	data, err := yaml.Marshal(cur)
	if err != nil {
		return nil, fmt.Errorf("clone config: marshal: %w", err)
	}
	var next config.Config
	if err := yaml.Unmarshal(data, &next); err != nil {
		return nil, fmt.Errorf("clone config: unmarshal: %w", err)
	}
	return &next, nil
}

// applyConfigChange runs fn on a clone of the current config snapshot under
// h.mu, persists the modified clone to disk, atomically publishes the new
// snapshot, and invokes the optional commit hook. On success, writes a
// 200 {"status":"ok"} response. On failure, writes a 500 error response and
// the snapshot stays unchanged.
//
// Caller must NOT hold h.mu — applyConfigChange takes it internally to
// serialize concurrent management writes against each other.
func (h *Handler) applyConfigChange(c *gin.Context, fn func(*config.Config)) bool {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	next, cloneErr := cloneConfigSnapshot(h.cfgPtr.Load())
	if cloneErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to clone config: %v", cloneErr)})
		return false
	}
	fn(next)

	// If fn already wrote a response (e.g. resolved a target index against
	// the cloned snapshot, found nothing, and emitted 404) we must NOT
	// persist or commit. This is the fix for Codex Phase C IMPORTANT #5
	// stale-index pre-resolution: handlers can now re-resolve inside the
	// closure against the cloned config and short-circuit safely.
	if c.Writer.Written() {
		return false
	}

	if err := config.SaveConfigPreserveComments(h.configFilePath, next); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to save config: %v", err)})
		return false
	}

	h.cfgPtr.Store(next)

	// Commit fan-out runs INSIDE h.mu (lock held through commit) so two
	// concurrent mgmt writes A and B cannot interleave their commits and
	// roll Server fan-out state back to A while disk holds B (Codex Phase
	// C re-review BLOCKER #1 follow-up). This is now safe because both
	// SetConfig and SetAuthManager are lock-free atomic stores; commit's
	// transitive call into SetAuthManager no longer re-locks h.mu.
	if h.commit != nil {
		h.commit(next)
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
	return true
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

// SetConfig updates the in-memory config snapshot when the server hot-reloads.
// Atomically publishes the new pointer; readers via cfg() see the update on
// their next Load.
func (h *Handler) SetConfig(cfg *config.Config) {
	if h == nil {
		return
	}
	h.cfgPtr.Store(cfg)
}

// SetAuthManager updates the auth manager reference used by management endpoints.
// Lock-free atomic store so it is safe to call while h.mu is held by an
// in-flight applyConfigChange — see Codex Phase C BLOCKER #1.
func (h *Handler) SetAuthManager(manager *coreauth.Manager) {
	if h == nil {
		return
	}
	if manager == nil {
		h.authManagerPtr.Store(nil)
		return
	}
	h.authManagerPtr.Store(manager)
}

// SetLocalPassword configures the runtime-local password accepted for localhost requests.
func (h *Handler) SetLocalPassword(password string) { h.localPassword = password }

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
	h.logDir = dir
}

// SetPostAuthHook registers a hook to be called after auth record creation but before persistence.
func (h *Handler) SetPostAuthHook(hook coreauth.PostAuthHook) {
	h.postAuthHook = hook
}

// Middleware enforces access control for management endpoints.
// All requests (local and remote) require a valid management key.
// Additionally, remote access requires allow-remote-management=true.
func (h *Handler) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-CPA-VERSION", buildinfo.Version)
		c.Header("X-CPA-COMMIT", buildinfo.Commit)
		c.Header("X-CPA-BUILD-DATE", buildinfo.BuildDate)

		clientIP := c.ClientIP()
		localClient := clientIP == "127.0.0.1" || clientIP == "::1"

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

		allowed, statusCode, errMsg := h.AuthenticateManagementKey(clientIP, localClient, provided)
		if !allowed {
			c.AbortWithStatusJSON(statusCode, gin.H{"error": errMsg})
			return
		}
		c.Next()
	}
}

// AuthenticateManagementKey verifies the provided management key for the given client.
// It mirrors the behaviour of Middleware() so non-HTTP callers can reuse the same logic.
func (h *Handler) AuthenticateManagementKey(clientIP string, localClient bool, provided string) (bool, int, string) {
	const maxFailures = 5
	const banDuration = 30 * time.Minute

	if h == nil {
		return false, http.StatusForbidden, "remote management disabled"
	}

	cfg := h.cfg()
	var (
		allowRemote bool
		secretHash  string
	)
	if cfg != nil {
		allowRemote = cfg.RemoteManagement.AllowRemote
		secretHash = cfg.RemoteManagement.SecretKey
	}
	if h.allowRemoteOverride {
		allowRemote = true
	}
	envSecret := h.envSecret

	now := time.Now()
	h.attemptsMu.Lock()
	ai := h.failedAttempts[clientIP]
	if ai != nil && !ai.blockedUntil.IsZero() {
		if now.Before(ai.blockedUntil) {
			remaining := ai.blockedUntil.Sub(now).Round(time.Second)
			h.attemptsMu.Unlock()
			return false, http.StatusForbidden, fmt.Sprintf("IP banned due to too many failed attempts. Try again in %s", remaining)
		}
		// Ban expired, reset state
		ai.blockedUntil = time.Time{}
		ai.count = 0
	}
	h.attemptsMu.Unlock()

	if !localClient && !allowRemote {
		return false, http.StatusForbidden, "remote management disabled"
	}

	fail := func() {
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

	reset := func() {
		h.attemptsMu.Lock()
		if ai := h.failedAttempts[clientIP]; ai != nil {
			ai.count = 0
			ai.blockedUntil = time.Time{}
		}
		h.attemptsMu.Unlock()
	}

	if secretHash == "" && envSecret == "" {
		return false, http.StatusForbidden, "remote management key not set"
	}

	if provided == "" {
		fail()
		return false, http.StatusUnauthorized, "missing management key"
	}

	if localClient {
		if lp := h.localPassword; lp != "" {
			if subtle.ConstantTimeCompare([]byte(provided), []byte(lp)) == 1 {
				reset()
				return true, 0, ""
			}
		}
	}

	if envSecret != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(envSecret)) == 1 {
		reset()
		return true, 0, ""
	}

	if secretHash == "" || bcrypt.CompareHashAndPassword([]byte(secretHash), []byte(provided)) != nil {
		fail()
		return false, http.StatusUnauthorized, "invalid management key"
	}

	reset()

	return true, 0, ""
}

// Helper methods for simple types. Each mutator receives the cloned config
// and applies the field write; applyConfigChange persists + publishes.

func (h *Handler) updateBoolField(c *gin.Context, set func(*config.Config, bool)) {
	var body struct {
		Value *bool `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		set(cfg, *body.Value)
	})
}

func (h *Handler) updateIntField(c *gin.Context, set func(*config.Config, int)) {
	var body struct {
		Value *int `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.applyConfigChange(c, func(cfg *config.Config) {
		set(cfg, *body.Value)
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
	h.applyConfigChange(c, func(cfg *config.Config) {
		set(cfg, *body.Value)
	})
}
