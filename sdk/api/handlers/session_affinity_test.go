package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"golang.org/x/net/context"
)

func TestMemorySessionAffinityStoreBasicLifecycle(t *testing.T) {
	t.Parallel()

	store := NewMemorySessionAffinityStore()
	if _, ok := store.Get(context.Background(), "session-1"); ok {
		t.Fatalf("unexpected binding before set")
	}

	store.Set(context.Background(), "session-1", "auth-1")
	if got, ok := store.Get(context.Background(), "session-1"); !ok || got != "auth-1" {
		t.Fatalf("binding = %q, %v; want auth-1, true", got, ok)
	}

	store.Delete(context.Background(), "session-1")
	if _, ok := store.Get(context.Background(), "session-1"); ok {
		t.Fatalf("unexpected binding after delete")
	}
}

func TestNewSessionAffinityStoreDefaultsToMemory(t *testing.T) {
	t.Parallel()

	store := NewSessionAffinityStore(nil)
	if _, ok := store.(*MemorySessionAffinityStore); !ok {
		t.Fatalf("store type = %T, want *MemorySessionAffinityStore", store)
	}
}

func TestNewRedisSessionAffinityStoreBuildsRedisBackend(t *testing.T) {
	t.Parallel()

	redisStore := NewRedisSessionAffinityStore(redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:6379",
	}), "test-prefix:", 90*time.Second)
	if redisStore.ttl != 90*time.Second {
		t.Fatalf("ttl = %s, want %s", redisStore.ttl, 90*time.Second)
	}
	if redisStore.keyPrefix != "test-prefix:" {
		t.Fatalf("keyPrefix = %q, want test-prefix:", redisStore.keyPrefix)
	}
	if redisStore.fallback == nil {
		t.Fatalf("fallback store should be initialized")
	}
}

func TestNewRedisSessionAffinityStoreInitializesFallbackMirror(t *testing.T) {
	t.Parallel()

	store := NewRedisSessionAffinityStore(redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:6379",
	}), "prefix:", time.Minute)
	if store.fallback == nil {
		t.Fatalf("fallback store should be initialized")
	}
}

func TestNewSessionAffinityStoreFallsBackToMemoryWhenRedisPingFails(t *testing.T) {
	t.Parallel()

	store := NewSessionAffinityStore(&sdkconfig.SDKConfig{
		SessionAffinity: sdkconfig.SessionAffinityConfig{
			Provider: "redis",
			Redis: sdkconfig.SessionAffinityRedisConfig{
				Addr: "127.0.0.1:1",
			},
		},
	})
	redisStore, ok := store.(*RedisSessionAffinityStore)
	if !ok {
		t.Fatalf("store type = %T, want *RedisSessionAffinityStore", store)
	}
	if redisStore.redisAvailable {
		t.Fatalf("redis should start in unavailable state when ping fails")
	}
}

func TestRedisSessionAffinityStoreRecoversAfterProbe(t *testing.T) {
	t.Parallel()

	store := NewRedisSessionAffinityStore(redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:6379",
	}), "prefix:", time.Minute)
	store.redisAvailable = false
	store.probeInterval = 0

	redisValues := make(map[string]string)
	pingCalls := 0
	store.pingFn = func(context.Context) error {
		pingCalls++
		return nil
	}
	store.getFn = func(_ context.Context, key string) (string, error) {
		value, ok := redisValues[key]
		if !ok {
			return "", redis.Nil
		}
		return value, nil
	}
	store.setFn = func(_ context.Context, key string, value string, _ time.Duration) error {
		redisValues[key] = value
		return nil
	}
	store.delFn = func(_ context.Context, key string) error {
		delete(redisValues, key)
		return nil
	}

	store.Set(context.Background(), "session-1", "auth-1")
	if pingCalls == 0 {
		t.Fatalf("expected recovery probe to run")
	}
	if !store.redisAvailable {
		t.Fatalf("redis should be marked available after successful probe")
	}
	if got := redisValues["prefix:session-1"]; got != "auth-1" {
		t.Fatalf("redis value = %q, want auth-1", got)
	}
}

func TestRedisSessionAffinityStoreUsesFallbackUntilProbeIntervalElapses(t *testing.T) {
	t.Parallel()

	store := NewRedisSessionAffinityStore(redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:6379",
	}), "prefix:", time.Minute)
	store.redisAvailable = false
	store.probeInterval = time.Hour
	store.lastProbeAt = time.Now()

	pingCalls := 0
	store.pingFn = func(context.Context) error {
		pingCalls++
		return nil
	}
	store.fallback.Set(context.Background(), "session-2", "auth-2")

	got, ok := store.Get(context.Background(), "session-2")
	if !ok || got != "auth-2" {
		t.Fatalf("fallback binding = %q, %v; want auth-2, true", got, ok)
	}
	if pingCalls != 0 {
		t.Fatalf("unexpected probe before interval elapsed")
	}
}

func TestRedisSessionAffinityStoreMarksUnavailableOnWriteFailure(t *testing.T) {
	t.Parallel()

	store := NewRedisSessionAffinityStore(redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:6379",
	}), "prefix:", time.Minute)
	store.setFn = func(context.Context, string, string, time.Duration) error {
		return errors.New("set failed")
	}

	store.Set(context.Background(), "session-3", "auth-3")
	if store.redisAvailable {
		t.Fatalf("redis should be marked unavailable after write failure")
	}
	if got, ok := store.fallback.Get(context.Background(), "session-3"); !ok || got != "auth-3" {
		t.Fatalf("fallback binding = %q, %v; want auth-3, true", got, ok)
	}
}

func TestRedisSessionAffinityStoreReplaysPendingSetAfterRecovery(t *testing.T) {
	t.Parallel()

	store := NewRedisSessionAffinityStore(redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:6379",
	}), "prefix:", time.Minute)
	store.redisAvailable = false
	store.probeInterval = 0

	redisValues := make(map[string]string)
	healthy := false
	store.pingFn = func(context.Context) error {
		if !healthy {
			return errors.New("redis unavailable")
		}
		return nil
	}
	store.setFn = func(_ context.Context, key string, value string, _ time.Duration) error {
		redisValues[key] = value
		return nil
	}
	store.delFn = func(_ context.Context, key string) error {
		delete(redisValues, key)
		return nil
	}

	store.Set(context.Background(), "session-4", "auth-4")
	if got, ok := store.fallback.Get(context.Background(), "session-4"); !ok || got != "auth-4" {
		t.Fatalf("fallback binding = %q, %v; want auth-4, true", got, ok)
	}
	if _, ok := redisValues["prefix:session-4"]; ok {
		t.Fatalf("redis should not be updated before recovery")
	}
	healthy = true
	if !store.ensureRedisAvailable(context.Background()) {
		t.Fatalf("expected redis recovery")
	}
	if got := redisValues["prefix:session-4"]; got != "auth-4" {
		t.Fatalf("redis value = %q, want auth-4", got)
	}
}

func TestRedisSessionAffinityStoreReplaysPendingDeleteAfterRecovery(t *testing.T) {
	t.Parallel()

	store := NewRedisSessionAffinityStore(redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:6379",
	}), "prefix:", time.Minute)
	store.redisAvailable = false
	store.probeInterval = 0

	redisValues := map[string]string{"prefix:session-5": "auth-5"}
	healthy := false
	store.pingFn = func(context.Context) error {
		if !healthy {
			return errors.New("redis unavailable")
		}
		return nil
	}
	store.setFn = func(_ context.Context, key string, value string, _ time.Duration) error {
		redisValues[key] = value
		return nil
	}
	store.delFn = func(_ context.Context, key string) error {
		delete(redisValues, key)
		return nil
	}

	store.fallback.Set(context.Background(), "session-5", "auth-5")
	store.Delete(context.Background(), "session-5")
	if _, ok := store.fallback.Get(context.Background(), "session-5"); ok {
		t.Fatalf("fallback binding should be deleted immediately")
	}
	healthy = true
	if !store.ensureRedisAvailable(context.Background()) {
		t.Fatalf("expected redis recovery")
	}
	if _, ok := redisValues["prefix:session-5"]; ok {
		t.Fatalf("redis binding should be deleted after recovery")
	}
}

func TestUpdateClientsReusesMemorySessionAffinityStore(t *testing.T) {
	t.Parallel()

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	memoryStore, ok := handler.SessionAffinityStore.(*MemorySessionAffinityStore)
	if !ok {
		t.Fatalf("store type = %T, want *MemorySessionAffinityStore", handler.SessionAffinityStore)
	}
	memoryStore.Set(context.Background(), "session-6", "auth-6")

	handler.UpdateClients(&sdkconfig.SDKConfig{})

	reused, ok := handler.SessionAffinityStore.(*MemorySessionAffinityStore)
	if !ok {
		t.Fatalf("store type = %T, want *MemorySessionAffinityStore", handler.SessionAffinityStore)
	}
	if reused != memoryStore {
		t.Fatalf("memory store should be reused across updates")
	}
	if got, ok := reused.Get(context.Background(), "session-6"); !ok || got != "auth-6" {
		t.Fatalf("binding = %q, %v; want auth-6, true", got, ok)
	}
}

func TestUpdateClientsReusesRedisSessionAffinityStoreWhenConfigMatches(t *testing.T) {
	t.Parallel()

	store := NewRedisSessionAffinityStore(redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:6379",
	}), "prefix:", time.Minute)
	cfg := &sdkconfig.SDKConfig{
		SessionAffinity: sdkconfig.SessionAffinityConfig{
			Provider:   "redis",
			TTLSeconds: 60,
			Redis: sdkconfig.SessionAffinityRedisConfig{
				Addr:      "127.0.0.1:6379",
				KeyPrefix: "prefix:",
			},
		},
	}
	store.signature = sessionAffinityStoreSignature(cfg)
	handler := &BaseAPIHandler{
		Cfg:                  cfg,
		SessionAffinityStore: store,
	}

	handler.UpdateClients(cfg)

	reused, ok := handler.SessionAffinityStore.(*RedisSessionAffinityStore)
	if !ok {
		t.Fatalf("store type = %T, want *RedisSessionAffinityStore", handler.SessionAffinityStore)
	}
	if reused != store {
		t.Fatalf("redis store should be reused when config matches")
	}
}
