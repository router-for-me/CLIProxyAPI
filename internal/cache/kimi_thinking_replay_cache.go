package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	// KimiThinkingReplayCacheTTL limits how long signed assistant content stays replayable.
	KimiThinkingReplayCacheTTL = 1 * time.Hour

	// KimiThinkingReplayCacheMaxEntries bounds process memory used for replay continuity.
	KimiThinkingReplayCacheMaxEntries = 10240

	// KimiThinkingReplayCacheEvictBatchSize leaves headroom after reaching capacity.
	KimiThinkingReplayCacheEvictBatchSize = 128

	// KimiThinkingReplayCacheMaxBytesPerEntry bounds one complete assistant content array.
	KimiThinkingReplayCacheMaxBytesPerEntry = 8 << 20

	// KimiThinkingReplayCacheMaxBlocksPerEntry prevents pathological content arrays.
	KimiThinkingReplayCacheMaxBlocksPerEntry = 512

	// KimiThinkingReplayCacheMaxTotalBytes bounds aggregate in-process replay content.
	KimiThinkingReplayCacheMaxTotalBytes = 256 << 20

	kimiThinkingReplayCacheMaxSerializedBytes = KimiThinkingReplayCacheMaxBytesPerEntry + 1024
)

type kimiThinkingReplayEntry struct {
	Content    []byte
	Timestamp  time.Time
	Generation string
	Deleted    bool
}

// KimiThinkingReplaySnapshot identifies the exact replay generation read for one request.
type KimiThinkingReplaySnapshot struct {
	raw        []byte
	generation string
	loaded     bool
	found      bool
}

type kimiThinkingReplayHomeValue struct {
	Generation string          `json:"generation"`
	Deleted    bool            `json:"deleted,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
}

var (
	kimiThinkingReplayMu         sync.Mutex
	kimiThinkingReplayEntries    = make(map[string]kimiThinkingReplayEntry)
	kimiThinkingReplayTotalBytes int
)

type kimiThinkingReplayKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error)
	KVDel(ctx context.Context, keys ...string) (int64, error)
	KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error)
	KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

var currentKimiThinkingReplayKVClient = func() (kimiThinkingReplayKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

// CacheKimiThinkingReplayBestEffort stores one complete signed assistant content array.
func CacheKimiThinkingReplayBestEffort(ctx context.Context, modelFamily, sessionKey string, content []byte) bool {
	key := kimiThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" || !validKimiThinkingReplayContent(content) {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cloned := append([]byte(nil), content...)
	generation := uuid.NewString()
	if client, homeMode, errClient := currentKimiThinkingReplayKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("home kv best-effort kimi thinking replay set failed prefix=cpa:kimi:*: %v", errClient)
			return false
		}
		raw, errMarshal := marshalKimiThinkingReplayHomeValue(generation, false, cloned)
		if errMarshal != nil {
			log.Errorf("home kv best-effort kimi thinking replay set failed prefix=cpa:kimi:*: %v", errMarshal)
			return false
		}
		written, errSet := client.KVSet(ctx, kimiThinkingReplayKVKey(modelFamily, sessionKey), raw, homekv.KVSetOptions{EX: KimiThinkingReplayCacheTTL})
		if errSet != nil {
			log.Errorf("home kv best-effort kimi thinking replay set failed prefix=cpa:kimi:*: %v", errSet)
			return false
		}
		return written
	}

	storeKimiThinkingReplayLocal(key, cloned, generation, false, time.Now())
	return true
}

// GetKimiThinkingReplayRequired retrieves complete assistant content for request-time replay.
func GetKimiThinkingReplayRequired(ctx context.Context, modelFamily, sessionKey string) ([]byte, bool, error) {
	content, _, found, errGet := GetKimiThinkingReplayWithSnapshotRequired(ctx, modelFamily, sessionKey)
	return content, found, errGet
}

// GetKimiThinkingReplayWithSnapshotRequired retrieves replay content and the exact cache state read.
func GetKimiThinkingReplayWithSnapshotRequired(ctx context.Context, modelFamily, sessionKey string) ([]byte, KimiThinkingReplaySnapshot, bool, error) {
	key := kimiThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" {
		return nil, KimiThinkingReplaySnapshot{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client, homeMode, errClient := currentKimiThinkingReplayKVClient()
	if homeMode {
		if errClient != nil {
			return nil, KimiThinkingReplaySnapshot{loaded: true}, false, errClient
		}
		kvKey := kimiThinkingReplayKVKey(modelFamily, sessionKey)
		raw, errRead := readOrReserveKimiThinkingReplayHomeValue(ctx, client, kvKey)
		if errRead != nil {
			return nil, KimiThinkingReplaySnapshot{loaded: true}, false, errRead
		}
		snapshot := KimiThinkingReplaySnapshot{raw: append([]byte(nil), raw...), loaded: true, found: true}
		content, generation, deleted, okDecode := decodeKimiThinkingReplayHomeValue(raw)
		if !okDecode {
			return nil, snapshot, false, fmt.Errorf("invalid kimi thinking replay content")
		}
		snapshot.generation = generation
		if _, errExpire := client.KVExpire(ctx, kvKey, KimiThinkingReplayCacheTTL); errExpire != nil {
			log.Warnf("home kv kimi thinking replay expire failed prefix=cpa:kimi:*: %v", errExpire)
		}
		if deleted {
			return nil, snapshot, false, nil
		}
		return content, snapshot, true, nil
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	kimiThinkingReplayMu.Lock()
	defer kimiThinkingReplayMu.Unlock()
	entry, ok := kimiThinkingReplayEntries[key]
	if !ok || now.Sub(entry.Timestamp) > KimiThinkingReplayCacheTTL {
		if ok {
			kimiThinkingReplayTotalBytes -= len(entry.Content)
			delete(kimiThinkingReplayEntries, key)
		}
		entry = reserveKimiThinkingReplayLocalLocked(key, now)
	}
	entry.Timestamp = now
	kimiThinkingReplayEntries[key] = entry
	snapshot := KimiThinkingReplaySnapshot{generation: entry.Generation, loaded: true, found: true}
	if entry.Deleted {
		return nil, snapshot, false, nil
	}
	return append([]byte(nil), entry.Content...), snapshot, true, nil
}

// ReplaceKimiThinkingReplayIfUnchanged stores completed content only if the request snapshot is current.
func ReplaceKimiThinkingReplayIfUnchanged(ctx context.Context, modelFamily, sessionKey string, snapshot KimiThinkingReplaySnapshot, content []byte) (bool, error) {
	key := kimiThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" || !validKimiThinkingReplayContent(content) {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !snapshot.loaded {
		return CacheKimiThinkingReplayBestEffort(ctx, modelFamily, sessionKey, content), nil
	}
	cloned := append([]byte(nil), content...)
	generation := uuid.NewString()
	client, homeMode, errClient := currentKimiThinkingReplayKVClient()
	if homeMode {
		if errClient != nil {
			return false, errClient
		}
		raw, errMarshal := marshalKimiThinkingReplayHomeValue(generation, false, cloned)
		if errMarshal != nil {
			return false, errMarshal
		}
		return client.KVCompareAndSwap(ctx, kimiThinkingReplayKVKey(modelFamily, sessionKey), snapshot.raw, snapshot.found, raw, KimiThinkingReplayCacheTTL)
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	kimiThinkingReplayMu.Lock()
	defer kimiThinkingReplayMu.Unlock()
	entry, found := kimiThinkingReplayEntries[key]
	if found != snapshot.found || (found && entry.Generation != snapshot.generation) {
		return false, nil
	}
	kimiThinkingReplayTotalBytes -= len(entry.Content)
	kimiThinkingReplayTotalBytes += len(cloned)
	kimiThinkingReplayEntries[key] = kimiThinkingReplayEntry{Content: cloned, Timestamp: time.Now(), Generation: generation}
	enforceKimiThinkingReplayLimitsLocked()
	return true, nil
}

// DeleteKimiThinkingReplayIfUnchanged clears replay state only if the request snapshot is current.
func DeleteKimiThinkingReplayIfUnchanged(ctx context.Context, modelFamily, sessionKey string, snapshot KimiThinkingReplaySnapshot) (bool, error) {
	key := kimiThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !snapshot.loaded {
		return true, DeleteKimiThinkingReplayRequired(ctx, modelFamily, sessionKey)
	}
	generation := uuid.NewString()
	client, homeMode, errClient := currentKimiThinkingReplayKVClient()
	if homeMode {
		if errClient != nil {
			return false, errClient
		}
		tombstone, errMarshal := marshalKimiThinkingReplayHomeValue(generation, true, nil)
		if errMarshal != nil {
			return false, errMarshal
		}
		return client.KVCompareAndSwap(ctx, kimiThinkingReplayKVKey(modelFamily, sessionKey), snapshot.raw, snapshot.found, tombstone, KimiThinkingReplayCacheTTL)
	}

	kimiThinkingReplayMu.Lock()
	defer kimiThinkingReplayMu.Unlock()
	entry, found := kimiThinkingReplayEntries[key]
	if found != snapshot.found || (found && entry.Generation != snapshot.generation) {
		return false, nil
	}
	kimiThinkingReplayTotalBytes -= len(entry.Content)
	kimiThinkingReplayEntries[key] = kimiThinkingReplayEntry{Timestamp: time.Now(), Generation: generation, Deleted: true}
	return true, nil
}

// DeleteKimiThinkingReplayRequired removes stale replay state unconditionally.
func DeleteKimiThinkingReplayRequired(ctx context.Context, modelFamily, sessionKey string) error {
	key := kimiThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client, homeMode, errClient := currentKimiThinkingReplayKVClient()
	if homeMode {
		if errClient != nil {
			return errClient
		}
		_, errDelete := client.KVDel(ctx, kimiThinkingReplayKVKey(modelFamily, sessionKey))
		return errDelete
	}
	kimiThinkingReplayMu.Lock()
	if entry, found := kimiThinkingReplayEntries[key]; found {
		kimiThinkingReplayTotalBytes -= len(entry.Content)
		delete(kimiThinkingReplayEntries, key)
	}
	kimiThinkingReplayMu.Unlock()
	return nil
}

// ClearKimiThinkingReplayCache clears all in-process Kimi replay state.
func ClearKimiThinkingReplayCache() {
	kimiThinkingReplayMu.Lock()
	kimiThinkingReplayEntries = make(map[string]kimiThinkingReplayEntry)
	kimiThinkingReplayTotalBytes = 0
	kimiThinkingReplayMu.Unlock()
}

func readOrReserveKimiThinkingReplayHomeValue(ctx context.Context, client kimiThinkingReplayKVClient, key string) ([]byte, error) {
	for attempt := 0; attempt < 4; attempt++ {
		raw, found, errGet := client.KVGet(ctx, key)
		if errGet != nil {
			return nil, errGet
		}
		if found {
			if len(raw) > kimiThinkingReplayCacheMaxSerializedBytes {
				return nil, fmt.Errorf("kimi thinking replay value exceeds size limit")
			}
			return raw, nil
		}
		tombstone, errMarshal := marshalKimiThinkingReplayHomeValue(uuid.NewString(), true, nil)
		if errMarshal != nil {
			return nil, errMarshal
		}
		swapped, errReserve := client.KVCompareAndSwap(ctx, key, nil, false, tombstone, KimiThinkingReplayCacheTTL)
		if errReserve != nil {
			return nil, errReserve
		}
		if swapped {
			return tombstone, nil
		}
	}
	return nil, fmt.Errorf("could not reserve absent kimi thinking replay state")
}

func marshalKimiThinkingReplayHomeValue(generation string, deleted bool, content []byte) ([]byte, error) {
	value := kimiThinkingReplayHomeValue{Generation: generation, Deleted: deleted}
	if !deleted {
		value.Content = append(json.RawMessage(nil), content...)
	}
	return json.Marshal(value)
}

func decodeKimiThinkingReplayHomeValue(raw []byte) ([]byte, string, bool, bool) {
	if len(raw) == 0 || len(raw) > kimiThinkingReplayCacheMaxSerializedBytes || !gjson.ValidBytes(raw) {
		return nil, "", false, false
	}
	root := gjson.ParseBytes(raw)
	if root.IsArray() {
		if !validKimiThinkingReplayContent(raw) {
			return nil, "", false, false
		}
		return append([]byte(nil), raw...), "legacy", false, true
	}
	var value kimiThinkingReplayHomeValue
	if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal != nil || strings.TrimSpace(value.Generation) == "" {
		return nil, "", false, false
	}
	if value.Deleted {
		return nil, value.Generation, true, true
	}
	if !validKimiThinkingReplayContent(value.Content) {
		return nil, "", false, false
	}
	return append([]byte(nil), value.Content...), value.Generation, false, true
}

func reserveKimiThinkingReplayLocalLocked(key string, now time.Time) kimiThinkingReplayEntry {
	entry := kimiThinkingReplayEntry{Timestamp: now, Generation: uuid.NewString(), Deleted: true}
	kimiThinkingReplayEntries[key] = entry
	enforceKimiThinkingReplayLimitsLocked()
	return entry
}

func storeKimiThinkingReplayLocal(key string, content []byte, generation string, deleted bool, now time.Time) {
	cacheCleanupOnce.Do(startCacheCleanup)
	kimiThinkingReplayMu.Lock()
	defer kimiThinkingReplayMu.Unlock()
	if previous, found := kimiThinkingReplayEntries[key]; found {
		kimiThinkingReplayTotalBytes -= len(previous.Content)
	}
	kimiThinkingReplayTotalBytes += len(content)
	kimiThinkingReplayEntries[key] = kimiThinkingReplayEntry{Content: content, Timestamp: now, Generation: generation, Deleted: deleted}
	enforceKimiThinkingReplayLimitsLocked()
}

func kimiThinkingReplayCacheKey(modelFamily, sessionKey string) string {
	modelFamily = strings.TrimSpace(modelFamily)
	sessionKey = strings.TrimSpace(sessionKey)
	if modelFamily == "" || sessionKey == "" {
		return ""
	}
	return strings.Join([]string{"kimi-thinking-replay", modelFamily, sessionKey}, "\x00")
}

func kimiThinkingReplayKVKey(modelFamily, sessionKey string) string {
	return "cpa:kimi:thinking-replay:" + homekv.HashKeyPart(strings.TrimSpace(modelFamily)) + ":" + homekv.HashKeyPart(strings.TrimSpace(sessionKey))
}

func validKimiThinkingReplayContent(content []byte) bool {
	if len(content) == 0 || len(content) > KimiThinkingReplayCacheMaxBytesPerEntry || !gjson.ValidBytes(content) {
		return false
	}
	root := gjson.ParseBytes(content)
	return root.IsArray() && len(root.Array()) > 0 && len(root.Array()) <= KimiThinkingReplayCacheMaxBlocksPerEntry
}

func enforceKimiThinkingReplayLimitsLocked() {
	for len(kimiThinkingReplayEntries) > KimiThinkingReplayCacheMaxEntries || kimiThinkingReplayTotalBytes > KimiThinkingReplayCacheMaxTotalBytes {
		if len(kimiThinkingReplayEntries) == 0 {
			kimiThinkingReplayTotalBytes = 0
			return
		}
		evictOldestKimiThinkingReplayEntriesLocked(KimiThinkingReplayCacheEvictBatchSize)
	}
}

func evictOldestKimiThinkingReplayEntriesLocked(count int) {
	if count <= 0 || len(kimiThinkingReplayEntries) == 0 {
		return
	}
	type candidate struct {
		key       string
		timestamp time.Time
	}
	candidates := make([]candidate, 0, len(kimiThinkingReplayEntries))
	for key, entry := range kimiThinkingReplayEntries {
		candidates = append(candidates, candidate{key: key, timestamp: entry.Timestamp})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].timestamp.Before(candidates[j].timestamp)
	})
	if count > len(candidates) {
		count = len(candidates)
	}
	for i := 0; i < count; i++ {
		entry := kimiThinkingReplayEntries[candidates[i].key]
		kimiThinkingReplayTotalBytes -= len(entry.Content)
		delete(kimiThinkingReplayEntries, candidates[i].key)
	}
}

func purgeExpiredKimiThinkingReplayCache(now time.Time) {
	kimiThinkingReplayMu.Lock()
	for key, entry := range kimiThinkingReplayEntries {
		if now.Sub(entry.Timestamp) > KimiThinkingReplayCacheTTL {
			kimiThinkingReplayTotalBytes -= len(entry.Content)
			delete(kimiThinkingReplayEntries, key)
		}
	}
	kimiThinkingReplayMu.Unlock()
}
