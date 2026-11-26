package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	log "github.com/sirupsen/logrus"
)

// SignatureEntry holds a cached thinking signature with timestamp
type SignatureEntry struct {
	Signature string
	Timestamp time.Time
}

const (
	// SignatureCacheTTL is how long signatures are valid
	SignatureCacheTTL = 3 * time.Hour

	// SignatureTextHashLen is the length of the hash key (16 hex chars = 64-bit key space)
	SignatureTextHashLen = 16

	// MinValidSignatureLen is the minimum length for a signature to be considered valid
	MinValidSignatureLen = 50

	// CacheCleanupInterval controls how often stale entries are purged
	CacheCleanupInterval = 10 * time.Minute
)

// signatureCache stores signatures by credential scope and model group -> textHash -> SignatureEntry
var signatureCache sync.Map

type signatureCacheScopeContextKey struct{}

type signatureCacheScopeValue struct {
	scope string
	bound bool
}

// WithSignatureCacheScope isolates signature recovery to one upstream credential.
func WithSignatureCacheScope(ctx context.Context, scope string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, signatureCacheScopeContextKey{}, signatureCacheScopeValue{
		scope: strings.TrimSpace(scope),
		bound: true,
	})
}

func signatureCacheScope(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	value, ok := ctx.Value(signatureCacheScopeContextKey{}).(signatureCacheScopeValue)
	return value.scope, ok && value.bound
}

// RequireSignatureCacheScope prevents unscoped legacy fallback on security-sensitive paths.
func RequireSignatureCacheScope(ctx context.Context) context.Context {
	if _, bound := signatureCacheScope(ctx); bound {
		return ctx
	}
	return WithSignatureCacheScope(ctx, "")
}

func signatureCacheGroupKey(modelName, scope string) string {
	groupKey := GetModelGroup(modelName)
	if scope == "" {
		return groupKey
	}
	return homekv.HashKeyPart(scope) + "\x00" + groupKey
}

// cacheCleanupOnce ensures the background cleanup goroutine starts only once
var cacheCleanupOnce sync.Once

type signatureKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error)
	KVDel(ctx context.Context, keys ...string) (int64, error)
	KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

var currentSignatureKVClient = func() (signatureKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

// groupCache is the inner map type
type groupCache struct {
	mu      sync.RWMutex
	entries map[string]SignatureEntry
	deleted bool
}

// hashText creates a stable, Unicode-safe key from text content
func hashText(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])[:SignatureTextHashLen]
}

// getOrCreateGroupCache gets or creates a cache bucket for a model group
func getOrCreateGroupCache(groupKey string) *groupCache {
	// Start background cleanup on first access
	cacheCleanupOnce.Do(startCacheCleanup)

	if val, ok := signatureCache.Load(groupKey); ok {
		return val.(*groupCache)
	}
	sc := &groupCache{entries: make(map[string]SignatureEntry)}
	actual, _ := signatureCache.LoadOrStore(groupKey, sc)
	return actual.(*groupCache)
}

// startCacheCleanup launches a background goroutine that periodically
// removes caches where all entries have expired.
func startCacheCleanup() {
	go func() {
		ticker := time.NewTicker(CacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredCaches()
		}
	}()
}

// purgeExpiredCaches removes caches with no valid (non-expired) entries.
func purgeExpiredCaches() {
	now := time.Now()
	signatureCache.Range(func(key, value any) bool {
		sc := value.(*groupCache)
		sc.mu.Lock()
		// Remove expired entries
		for k, entry := range sc.entries {
			if now.Sub(entry.Timestamp) > SignatureCacheTTL {
				delete(sc.entries, k)
			}
		}
		// Remove cache bucket while holding the lock to prevent TOCTOU eviction.
		if len(sc.entries) == 0 && !sc.deleted {
			sc.deleted = true
			signatureCache.CompareAndDelete(key, sc)
		}
		sc.mu.Unlock()
		return true
	})
	purgeExpiredCodexReasoningReplayCache(now)
	purgeExpiredXAIReasoningReplayCache(now)
	purgeExpiredAntigravityReasoningReplayCache(now)
	purgeExpiredKimiThinkingReplayCache(now)
	purgeExpiredClaudeThinkingReplayCache(now)
	purgeExpiredClaudeWebSearchResultCache(now)
}

// CacheSignature stores a thinking signature for a given model group and text.
// Used for Claude models that require signed thinking blocks in multi-turn conversations.
func CacheSignature(modelName, text, signature string) {
	CacheSignatureBestEffort(context.Background(), modelName, text, signature)
}

// CacheSignatureBestEffort stores a thinking signature for completed response paths.
func CacheSignatureBestEffort(ctx context.Context, modelName, text, signature string) bool {
	if text == "" || signature == "" {
		return false
	}
	if len(signature) < MinValidSignatureLen {
		return false
	}
	scope, scopeBound := signatureCacheScope(ctx)
	if scopeBound && scope == "" {
		return false
	}

	if client, homeMode, errClient := currentSignatureKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("home kv best-effort signature set failed prefix=cpa:signature:*: %v", errClient)
			return false
		}
		written, errSet := client.KVSet(ctx, signatureKVKeyScoped(modelName, scope, text), []byte(signature), homekv.KVSetOptions{EX: SignatureCacheTTL})
		if errSet != nil {
			log.Errorf("home kv best-effort signature set failed prefix=cpa:signature:*: %v", errSet)
			return false
		}
		return written
	}

	groupKey := signatureCacheGroupKey(modelName, scope)
	textHash := hashText(text)
	for {
		sc := getOrCreateGroupCache(groupKey)
		sc.mu.Lock()
		if sc.deleted {
			sc.mu.Unlock()
			continue
		}
		sc.entries[textHash] = SignatureEntry{
			Signature: signature,
			Timestamp: time.Now(),
		}
		sc.mu.Unlock()
		return true
	}
}

// GetCachedSignature retrieves a legacy, unscoped signature for compatibility callers.
func GetCachedSignature(modelName, text string) string {
	signature, errSignature := getCachedSignatureRequired(context.Background(), modelName, text, true)
	if errSignature != nil {
		return ""
	}
	return signature
}

// GetCachedSignatureRequired retrieves a scoped signature for request-time paths.
func GetCachedSignatureRequired(ctx context.Context, modelName, text string) (string, error) {
	return getCachedSignatureRequired(ctx, modelName, text, false)
}

func getCachedSignatureRequired(ctx context.Context, modelName, text string, allowLegacyScope bool) (string, error) {
	scope, scopeBound := signatureCacheScope(ctx)
	modelGroup := GetModelGroup(modelName)
	if scopeBound && scope == "" && !allowLegacyScope {
		if modelGroup == "gemini" {
			return "skip_thought_signature_validator", nil
		}
		return "", nil
	}
	groupKey := signatureCacheGroupKey(modelName, scope)

	if text == "" {
		if modelGroup == "gemini" {
			return "skip_thought_signature_validator", nil
		}
		return "", nil
	}

	if client, homeMode, errClient := currentSignatureKVClient(); homeMode {
		if errClient != nil {
			return "", errClient
		}
		key := signatureKVKeyScoped(modelName, scope, text)
		raw, found, errGet := client.KVGet(ctx, key)
		if errGet != nil {
			return "", errGet
		}
		if !found {
			if modelGroup == "gemini" {
				return "skip_thought_signature_validator", nil
			}
			return "", nil
		}
		if _, errExpire := client.KVExpire(ctx, key, SignatureCacheTTL); errExpire != nil {
			return "", errExpire
		}
		return string(raw), nil
	}

	val, ok := signatureCache.Load(groupKey)
	if !ok {
		if modelGroup == "gemini" {
			return "skip_thought_signature_validator", nil
		}
		return "", nil
	}
	sc := val.(*groupCache)

	textHash := hashText(text)

	now := time.Now()

	sc.mu.Lock()
	entry, exists := sc.entries[textHash]
	if !exists {
		sc.mu.Unlock()
		if modelGroup == "gemini" {
			return "skip_thought_signature_validator", nil
		}
		return "", nil
	}
	if now.Sub(entry.Timestamp) > SignatureCacheTTL {
		delete(sc.entries, textHash)
		sc.mu.Unlock()
		if modelGroup == "gemini" {
			return "skip_thought_signature_validator", nil
		}
		return "", nil
	}

	// Refresh TTL on access (sliding expiration).
	entry.Timestamp = now
	sc.entries[textHash] = entry
	sc.mu.Unlock()

	return entry.Signature, nil
}

// ClearSignatureCache clears signature cache for a specific model group or all groups.
func ClearSignatureCache(modelName string) {
	if modelName == "" {
		signatureCache.Range(func(key, _ any) bool {
			signatureCache.Delete(key)
			return true
		})
		return
	}
	groupKey := GetModelGroup(modelName)
	signatureCache.Range(func(key, _ any) bool {
		cacheKey, ok := key.(string)
		if ok && (cacheKey == groupKey || strings.HasSuffix(cacheKey, "\x00"+groupKey)) {
			signatureCache.Delete(key)
		}
		return true
	})
}

// DeleteCachedSignatureRequired removes one exact cached signature.
func DeleteCachedSignatureRequired(ctx context.Context, modelName, text string) error {
	if text == "" {
		return nil
	}
	scope, scopeBound := signatureCacheScope(ctx)
	if scopeBound && scope == "" {
		return nil
	}
	if client, homeMode, errClient := currentSignatureKVClient(); homeMode {
		if errClient != nil {
			return errClient
		}
		_, errDel := client.KVDel(ctx, signatureKVKeyScoped(modelName, scope, text))
		return errDel
	}
	groupKey := signatureCacheGroupKey(modelName, scope)
	textHash := hashText(text)
	val, ok := signatureCache.Load(groupKey)
	if !ok {
		return nil
	}
	sc := val.(*groupCache)
	sc.mu.Lock()
	delete(sc.entries, textHash)
	if len(sc.entries) == 0 && !sc.deleted {
		sc.deleted = true
		signatureCache.CompareAndDelete(groupKey, sc)
	}
	sc.mu.Unlock()
	return nil
}

// HasValidSignature checks if a signature is valid (non-empty and long enough)
func HasValidSignature(modelName, signature string) bool {
	return (signature != "" && len(signature) >= MinValidSignatureLen) || (signature == "skip_thought_signature_validator" && GetModelGroup(modelName) == "gemini")
}

func GetModelGroup(modelName string) string {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	if strings.Contains(normalized, "gpt") {
		return "gpt"
	} else if strings.Contains(normalized, "claude") {
		return "claude"
	} else if strings.Contains(normalized, "gemini") {
		return "gemini"
	}
	return modelName
}

func signatureKVKey(modelName, text string) string {
	return signatureKVKeyScoped(modelName, "", text)
}

func signatureKVKeyScoped(modelName, scope, text string) string {
	parts := []string{"cpa:signature"}
	if scope = strings.TrimSpace(scope); scope != "" {
		parts = append(parts, homekv.HashKeyPart(scope))
	}
	parts = append(parts, GetModelGroup(modelName), homekv.HashKeyPart(text))
	return strings.Join(parts, ":")
}

var signatureCacheEnabled atomic.Bool
var signatureBypassStrictMode atomic.Bool

func init() {
	signatureCacheEnabled.Store(true)
	signatureBypassStrictMode.Store(false)
}

// SetSignatureCacheEnabled switches Antigravity signature handling between cache mode and bypass mode.
func SetSignatureCacheEnabled(enabled bool) {
	previous := signatureCacheEnabled.Swap(enabled)
	if previous == enabled {
		return
	}
	if !enabled {
		log.Info("antigravity signature cache DISABLED - bypass mode active, cached signatures will not be used for request translation")
	}
}

// SignatureCacheEnabled returns whether signature cache validation is enabled.
func SignatureCacheEnabled() bool {
	return signatureCacheEnabled.Load()
}

// SetSignatureBypassStrictMode controls whether bypass mode uses strict protobuf-tree validation.
func SetSignatureBypassStrictMode(strict bool) {
	previous := signatureBypassStrictMode.Swap(strict)
	if previous == strict {
		return
	}
	if strict {
		log.Debug("antigravity bypass signature validation: strict mode (protobuf tree)")
	} else {
		log.Debug("antigravity bypass signature validation: basic mode (R/E + 0x12)")
	}
}

// SignatureBypassStrictMode returns whether bypass mode uses strict protobuf-tree validation.
func SignatureBypassStrictMode() bool {
	return signatureBypassStrictMode.Load()
}
