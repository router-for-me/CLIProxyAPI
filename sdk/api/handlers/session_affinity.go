package handlers

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/net/context"
)

const (
	defaultSessionAffinityHeader      = "X-Session-Affinity"
	defaultSessionAffinityTTL         = 24 * time.Hour
	defaultSessionAffinityKeyPrefix   = "cliproxy:session-affinity:"
	defaultSessionAffinityProbe       = 5 * time.Second
	defaultSessionAffinityPingWait    = 2 * time.Second
	defaultSessionAffinityHotCacheTTL = 30 * time.Second
)

type sessionAffinityContextKey struct{}

type sessionAffinityHotCache struct {
	mu       sync.RWMutex
	ttl      time.Duration
	bindings map[string]sessionAffinityHotEntry
}

type sessionAffinityHotEntry struct {
	authID    string
	expiresAt time.Time
}

// SessionAffinityStore persists session-to-auth bindings for sticky routing.
type SessionAffinityStore interface {
	Get(ctx context.Context, sessionKey string) (string, bool)
	Set(ctx context.Context, sessionKey, authID string)
	Delete(ctx context.Context, sessionKey string)
}

// MemorySessionAffinityStore keeps session affinity bindings in process memory.
type MemorySessionAffinityStore struct {
	mu       sync.RWMutex
	bindings map[string]string
}

// RedisSessionAffinityStore persists bindings in Redis with a TTL.
type RedisSessionAffinityStore struct {
	client         redis.UniversalClient
	signature      string
	keyPrefix      string
	ttl            time.Duration
	fallback       *MemorySessionAffinityStore
	probeInterval  time.Duration
	pingTimeout    time.Duration
	stateMu        sync.Mutex
	redisAvailable bool
	lastProbeAt    time.Time
	pendingOps     map[string]*string
	pingFn         func(context.Context) error
	getFn          func(context.Context, string) (string, error)
	setFn          func(context.Context, string, string, time.Duration) error
	delFn          func(context.Context, string) error
}

// NewMemorySessionAffinityStore constructs an empty in-memory affinity store.
func NewMemorySessionAffinityStore() *MemorySessionAffinityStore {
	return &MemorySessionAffinityStore{
		bindings: make(map[string]string),
	}
}

// NewRedisSessionAffinityStore constructs a Redis-backed affinity store.
func NewRedisSessionAffinityStore(client redis.UniversalClient, keyPrefix string, ttl time.Duration) *RedisSessionAffinityStore {
	keyPrefix = strings.TrimSpace(keyPrefix)
	if keyPrefix == "" {
		keyPrefix = defaultSessionAffinityKeyPrefix
	}
	if ttl <= 0 {
		ttl = defaultSessionAffinityTTL
	}
	return &RedisSessionAffinityStore{
		client:         client,
		keyPrefix:      keyPrefix,
		ttl:            ttl,
		fallback:       NewMemorySessionAffinityStore(),
		probeInterval:  defaultSessionAffinityProbe,
		pingTimeout:    defaultSessionAffinityPingWait,
		redisAvailable: true,
		pendingOps:     make(map[string]*string),
		pingFn: func(ctx context.Context) error {
			return client.Ping(ctx).Err()
		},
		getFn: func(ctx context.Context, key string) (string, error) {
			return client.Get(ctx, key).Result()
		},
		setFn: func(ctx context.Context, key string, value string, ttl time.Duration) error {
			return client.Set(ctx, key, value, ttl).Err()
		},
		delFn: func(ctx context.Context, key string) error {
			return client.Del(ctx, key).Err()
		},
	}
}

// Get returns the bound auth ID for a session key.
func (s *MemorySessionAffinityStore) Get(_ context.Context, sessionKey string) (string, bool) {
	sessionKey = strings.TrimSpace(sessionKey)
	if s == nil || sessionKey == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	authID, ok := s.bindings[sessionKey]
	if !ok || strings.TrimSpace(authID) == "" {
		return "", false
	}
	return authID, true
}

// Set stores or replaces the binding for a session key.
func (s *MemorySessionAffinityStore) Set(_ context.Context, sessionKey, authID string) {
	sessionKey = strings.TrimSpace(sessionKey)
	authID = strings.TrimSpace(authID)
	if s == nil || sessionKey == "" || authID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings == nil {
		s.bindings = make(map[string]string)
	}
	s.bindings[sessionKey] = authID
}

// Delete removes the binding for a session key.
func (s *MemorySessionAffinityStore) Delete(_ context.Context, sessionKey string) {
	sessionKey = strings.TrimSpace(sessionKey)
	if s == nil || sessionKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bindings, sessionKey)
}

func (s *MemorySessionAffinityStore) Snapshot() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.bindings) == 0 {
		return nil
	}
	out := make(map[string]string, len(s.bindings))
	for key, value := range s.bindings {
		out[key] = value
	}
	return out
}

// Get returns the bound auth ID for a session key.
func (s *RedisSessionAffinityStore) Get(ctx context.Context, sessionKey string) (string, bool) {
	sessionKey = strings.TrimSpace(sessionKey)
	if s == nil || s.client == nil || sessionKey == "" {
		return "", false
	}
	if !s.ensureRedisAvailable(ctx) {
		if s.fallback != nil {
			return s.fallback.Get(ctx, sessionKey)
		}
		return "", false
	}
	authID, err := s.getFn(ctx, s.redisKey(sessionKey))
	if err == redis.Nil {
		if s.fallback != nil {
			s.fallback.Delete(ctx, sessionKey)
		}
		return "", false
	}
	if err != nil {
		log.WithError(err).Warn("session affinity: redis get failed")
		s.markRedisUnavailable()
		if s.fallback != nil {
			return s.fallback.Get(ctx, sessionKey)
		}
		return "", false
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		if s.fallback != nil {
			s.fallback.Delete(ctx, sessionKey)
		}
		return "", false
	}
	if s.fallback != nil {
		s.fallback.Set(ctx, sessionKey, authID)
	}
	return authID, true
}

// Set stores or replaces the binding for a session key.
func (s *RedisSessionAffinityStore) Set(ctx context.Context, sessionKey, authID string) {
	sessionKey = strings.TrimSpace(sessionKey)
	authID = strings.TrimSpace(authID)
	if s == nil || s.client == nil || sessionKey == "" || authID == "" {
		return
	}
	if s.fallback != nil {
		s.fallback.Set(ctx, sessionKey, authID)
	}
	if !s.ensureRedisAvailable(ctx) {
		s.recordPendingSet(sessionKey, authID)
		return
	}
	if err := s.setFn(ctx, s.redisKey(sessionKey), authID, s.ttl); err != nil {
		log.WithError(err).Warn("session affinity: redis set failed")
		s.recordPendingSet(sessionKey, authID)
		s.markRedisUnavailable()
	}
}

// Delete removes the binding for a session key.
func (s *RedisSessionAffinityStore) Delete(ctx context.Context, sessionKey string) {
	sessionKey = strings.TrimSpace(sessionKey)
	if s == nil || s.client == nil || sessionKey == "" {
		return
	}
	if s.fallback != nil {
		s.fallback.Delete(ctx, sessionKey)
	}
	if !s.ensureRedisAvailable(ctx) {
		s.recordPendingDelete(sessionKey)
		return
	}
	if err := s.delFn(ctx, s.redisKey(sessionKey)); err != nil && err != redis.Nil {
		log.WithError(err).Warn("session affinity: redis delete failed")
		s.recordPendingDelete(sessionKey)
		s.markRedisUnavailable()
	}
}

func (s *RedisSessionAffinityStore) redisKey(sessionKey string) string {
	return s.keyPrefix + sessionKey
}

func (s *RedisSessionAffinityStore) ensureRedisAvailable(ctx context.Context) bool {
	if s == nil || s.client == nil {
		return false
	}
	s.stateMu.Lock()
	available := s.redisAvailable
	lastProbeAt := s.lastProbeAt
	interval := s.probeInterval
	s.stateMu.Unlock()
	if available {
		return true
	}
	now := time.Now()
	if interval > 0 && !lastProbeAt.IsZero() && now.Sub(lastProbeAt) < interval {
		return false
	}

	probeCtx := ctx
	cancel := func() {}
	if probeCtx == nil {
		probeCtx = context.Background()
	}
	if s.pingTimeout > 0 {
		probeCtx, cancel = context.WithTimeout(probeCtx, s.pingTimeout)
	}
	defer cancel()

	if err := s.pingFn(probeCtx); err != nil {
		s.stateMu.Lock()
		s.redisAvailable = false
		s.lastProbeAt = now
		s.stateMu.Unlock()
		log.WithError(err).Warn("session affinity: redis probe failed, using memory fallback")
		return false
	}

	if !s.flushPendingOps(probeCtx) {
		return false
	}

	s.stateMu.Lock()
	wasUnavailable := !s.redisAvailable
	s.redisAvailable = true
	s.lastProbeAt = now
	s.stateMu.Unlock()
	if wasUnavailable {
		log.Info("session affinity: redis recovered, resuming redis-backed routing")
	}
	return true
}

func (s *RedisSessionAffinityStore) markRedisUnavailable() {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.redisAvailable = false
	s.lastProbeAt = time.Now()
}

func (s *RedisSessionAffinityStore) recordPendingSet(sessionKey, authID string) {
	if s == nil {
		return
	}
	sessionKey = strings.TrimSpace(sessionKey)
	authID = strings.TrimSpace(authID)
	if sessionKey == "" || authID == "" {
		return
	}
	value := authID
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.pendingOps == nil {
		s.pendingOps = make(map[string]*string)
	}
	s.pendingOps[sessionKey] = &value
}

func (s *RedisSessionAffinityStore) recordPendingDelete(sessionKey string) {
	if s == nil {
		return
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.pendingOps == nil {
		s.pendingOps = make(map[string]*string)
	}
	s.pendingOps[sessionKey] = nil
}

func (s *RedisSessionAffinityStore) flushPendingOps(ctx context.Context) bool {
	if s == nil {
		return false
	}

	s.stateMu.Lock()
	pending := make(map[string]*string, len(s.pendingOps))
	for key, value := range s.pendingOps {
		pending[key] = value
	}
	s.stateMu.Unlock()

	if len(pending) == 0 {
		return true
	}

	for sessionKey, value := range pending {
		var err error
		if value == nil {
			err = s.delFn(ctx, s.redisKey(sessionKey))
			if err == redis.Nil {
				err = nil
			}
		} else {
			err = s.setFn(ctx, s.redisKey(sessionKey), *value, s.ttl)
		}
		if err != nil {
			log.WithError(err).Warn("session affinity: failed to replay pending redis ops")
			s.markRedisUnavailable()
			return false
		}
	}

	s.stateMu.Lock()
	for key, value := range pending {
		current, ok := s.pendingOps[key]
		if !ok {
			continue
		}
		if current == nil && value == nil {
			delete(s.pendingOps, key)
			continue
		}
		if current != nil && value != nil && *current == *value {
			delete(s.pendingOps, key)
		}
	}
	s.stateMu.Unlock()
	return true
}

func (s *RedisSessionAffinityStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

// WithSessionAffinityKey returns a child context tagged with a sticky session key.
func WithSessionAffinityKey(ctx context.Context, sessionKey string) context.Context {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionAffinityContextKey{}, sessionKey)
}

func newSessionAffinityHotCache(ttl time.Duration) *sessionAffinityHotCache {
	if ttl <= 0 {
		ttl = defaultSessionAffinityHotCacheTTL
	}
	return &sessionAffinityHotCache{
		ttl:      ttl,
		bindings: make(map[string]sessionAffinityHotEntry),
	}
}

func (c *sessionAffinityHotCache) Get(sessionKey string) (string, bool) {
	sessionKey = strings.TrimSpace(sessionKey)
	if c == nil || sessionKey == "" {
		return "", false
	}
	now := time.Now()
	c.mu.RLock()
	entry, ok := c.bindings[sessionKey]
	c.mu.RUnlock()
	if !ok || strings.TrimSpace(entry.authID) == "" {
		return "", false
	}
	if !entry.expiresAt.IsZero() && !entry.expiresAt.After(now) {
		c.Delete(sessionKey)
		return "", false
	}
	return entry.authID, true
}

func (c *sessionAffinityHotCache) Set(sessionKey, authID string) {
	sessionKey = strings.TrimSpace(sessionKey)
	authID = strings.TrimSpace(authID)
	if c == nil || sessionKey == "" || authID == "" {
		return
	}
	c.mu.Lock()
	if c.bindings == nil {
		c.bindings = make(map[string]sessionAffinityHotEntry)
	}
	c.bindings[sessionKey] = sessionAffinityHotEntry{
		authID:    authID,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()
}

func (c *sessionAffinityHotCache) Delete(sessionKey string) {
	sessionKey = strings.TrimSpace(sessionKey)
	if c == nil || sessionKey == "" {
		return
	}
	c.mu.Lock()
	delete(c.bindings, sessionKey)
	c.mu.Unlock()
}

func sessionAffinityKeyFromContext(ctx context.Context, headerName string, rawJSON []byte) string {
	if ctx == nil {
		return sessionAffinityKeyFromPayload(rawJSON)
	}
	raw := ctx.Value(sessionAffinityContextKey{})
	switch v := raw.(type) {
	case string:
		if key := strings.TrimSpace(v); key != "" {
			return key
		}
	case []byte:
		if key := strings.TrimSpace(string(v)); key != "" {
			return key
		}
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		if strings.TrimSpace(headerName) == "" {
			headerName = defaultSessionAffinityHeader
		}
		if key := strings.TrimSpace(ginCtx.GetHeader(headerName)); key != "" {
			return key
		}
	}
	return sessionAffinityKeyFromPayload(rawJSON)
}

func sessionAffinityKeyFromPayload(rawJSON []byte) string {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return ""
	}
	for _, path := range []string{
		"previous_response_id",
		"conversation_id",
		"thread_id",
		"session_id",
		"metadata.session_id",
		"metadata.conversation_id",
		"prompt_cache_key",
	} {
		if value := strings.TrimSpace(gjson.GetBytes(rawJSON, path).String()); value != "" {
			return "body:" + path + ":" + value
		}
	}
	return ""
}

func (h *BaseAPIHandler) buildExecutionMetadata(ctx context.Context, providers []string, normalizedModel string, rawJSON []byte) map[string]any {
	meta := requestExecutionMetadata(ctx)
	if h == nil || h.SessionAffinityStore == nil || h.AuthManager == nil {
		return meta
	}
	if _, pinned := meta[coreexecutor.PinnedAuthMetadataKey]; pinned {
		return meta
	}

	sessionKey := sessionAffinityKeyFromContext(ctx, h.sessionAffinityHeaderName(), rawJSON)
	if sessionKey == "" {
		return meta
	}

	if authID, ok := h.sessionAffinityGet(ctx, sessionKey); ok {
		if h.AuthManager.CanUsePinnedAuth(authID, providers, normalizedModel) {
			meta[coreexecutor.PinnedAuthMetadataKey] = authID
		} else {
			h.sessionAffinityDelete(ctx, sessionKey)
		}
	} else {
		meta[coreexecutor.InitialStickySessionMetadataKey] = true
	}

	if selectedAuthCallback := selectedAuthCallbackFromExecutionMetadata(meta); selectedAuthCallback != nil {
		meta[coreexecutor.SelectedAuthCallbackMetadataKey] = func(authID string) {
			selectedAuthCallback(authID)
			h.bindSessionAffinity(ctx, sessionKey, providers, normalizedModel, authID)
		}
		return meta
	}

	meta[coreexecutor.SelectedAuthCallbackMetadataKey] = func(authID string) {
		h.bindSessionAffinity(ctx, sessionKey, providers, normalizedModel, authID)
	}
	return meta
}

func selectedAuthCallbackFromExecutionMetadata(meta map[string]any) func(string) {
	if len(meta) == 0 {
		return nil
	}
	callback, _ := meta[coreexecutor.SelectedAuthCallbackMetadataKey].(func(string))
	return callback
}

func (h *BaseAPIHandler) bindSessionAffinity(ctx context.Context, sessionKey string, providers []string, normalizedModel, authID string) {
	sessionKey = strings.TrimSpace(sessionKey)
	authID = strings.TrimSpace(authID)
	if h == nil || h.SessionAffinityStore == nil || h.AuthManager == nil || sessionKey == "" || authID == "" {
		return
	}
	if !h.AuthManager.CanUsePinnedAuth(authID, providers, normalizedModel) {
		return
	}
	if h.sessionAffinityHotCache != nil {
		h.sessionAffinityHotCache.Set(sessionKey, authID)
	}
	h.SessionAffinityStore.Set(ctx, sessionKey, authID)
}

func (h *BaseAPIHandler) sessionAffinityGet(ctx context.Context, sessionKey string) (string, bool) {
	if h == nil || h.SessionAffinityStore == nil {
		return "", false
	}
	if h.sessionAffinityHotCache != nil {
		if authID, ok := h.sessionAffinityHotCache.Get(sessionKey); ok {
			return authID, true
		}
	}
	authID, ok := h.SessionAffinityStore.Get(ctx, sessionKey)
	if ok && h.sessionAffinityHotCache != nil {
		h.sessionAffinityHotCache.Set(sessionKey, authID)
	}
	return authID, ok
}

func (h *BaseAPIHandler) sessionAffinityDelete(ctx context.Context, sessionKey string) {
	if h == nil || h.SessionAffinityStore == nil {
		return
	}
	if h.sessionAffinityHotCache != nil {
		h.sessionAffinityHotCache.Delete(sessionKey)
	}
	h.SessionAffinityStore.Delete(ctx, sessionKey)
}

var _ SessionAffinityStore = (*MemorySessionAffinityStore)(nil)
var _ SessionAffinityStore = (*RedisSessionAffinityStore)(nil)

// NewSessionAffinityStore builds the configured session affinity backend.
func NewSessionAffinityStore(cfg *config.SDKConfig) SessionAffinityStore {
	if cfg == nil {
		return NewMemorySessionAffinityStore()
	}
	switch strings.ToLower(strings.TrimSpace(cfg.SessionAffinity.Provider)) {
	case "", "memory":
		return NewMemorySessionAffinityStore()
	case "redis":
		addr := strings.TrimSpace(cfg.SessionAffinity.Redis.Addr)
		if addr == "" {
			log.Warn("session affinity: redis provider configured without addr, falling back to memory store")
			return NewMemorySessionAffinityStore()
		}
		client := redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: cfg.SessionAffinity.Redis.Password,
			DB:       cfg.SessionAffinity.Redis.DB,
		})
		store := NewRedisSessionAffinityStore(client, cfg.SessionAffinity.Redis.KeyPrefix, sessionAffinityTTL(cfg))
		store.signature = sessionAffinityStoreSignature(cfg)
		pingCtx, pingCancel := context.WithTimeout(context.Background(), store.pingTimeout)
		startErr := store.pingFn(pingCtx)
		pingCancel()
		if startErr != nil {
			store.markRedisUnavailable()
			log.Warn("session affinity: redis unavailable on startup, using memory fallback until recovery")
		} else {
			store.stateMu.Lock()
			store.redisAvailable = true
			store.lastProbeAt = time.Now()
			store.stateMu.Unlock()
		}
		return store
	default:
		log.Warnf("session affinity: unsupported provider %q, falling back to memory store", cfg.SessionAffinity.Provider)
		return NewMemorySessionAffinityStore()
	}
}

func reconcileSessionAffinityStore(current SessionAffinityStore, cfg *config.SDKConfig) SessionAffinityStore {
	desiredSignature := sessionAffinityStoreSignature(cfg)
	switch desiredSignature {
	case "memory":
		if existing, ok := current.(*MemorySessionAffinityStore); ok && existing != nil {
			return existing
		}
	default:
		if existing, ok := current.(*RedisSessionAffinityStore); ok && existing != nil && existing.signature == desiredSignature {
			return existing
		}
	}
	closeSessionAffinityStore(current)
	return NewSessionAffinityStore(cfg)
}

func closeSessionAffinityStore(store SessionAffinityStore) {
	if store == nil {
		return
	}
	if closer, ok := store.(interface{ Close() error }); ok && closer != nil {
		if err := closer.Close(); err != nil {
			log.WithError(err).Warn("session affinity: failed to close previous store")
		}
	}
}

func sessionAffinityStoreSignature(cfg *config.SDKConfig) string {
	if cfg == nil {
		return "memory"
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.SessionAffinity.Provider))
	switch provider {
	case "", "memory":
		return "memory"
	case "redis":
		return strings.Join([]string{
			"redis",
			strings.TrimSpace(cfg.SessionAffinity.Redis.Addr),
			strings.TrimSpace(cfg.SessionAffinity.Redis.Password),
			strings.TrimSpace(cfg.SessionAffinity.Redis.KeyPrefix),
			strings.TrimSpace(sessionAffinityTTL(cfg).String()),
			strings.TrimSpace(strconv.Itoa(cfg.SessionAffinity.Redis.DB)),
		}, "|")
	default:
		return "memory"
	}
}

func (h *BaseAPIHandler) sessionAffinityHeaderName() string {
	if h == nil || h.Cfg == nil {
		return defaultSessionAffinityHeader
	}
	header := strings.TrimSpace(h.Cfg.SessionAffinity.Header)
	if header == "" {
		return defaultSessionAffinityHeader
	}
	return header
}

func sessionAffinityTTL(cfg *config.SDKConfig) time.Duration {
	if cfg == nil || cfg.SessionAffinity.TTLSeconds <= 0 {
		return defaultSessionAffinityTTL
	}
	return time.Duration(cfg.SessionAffinity.TTLSeconds) * time.Second
}
