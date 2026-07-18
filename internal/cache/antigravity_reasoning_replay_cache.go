package cache

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// AntigravityReasoningReplayCacheTTL limits how long encrypted reasoning replay
	// items stay in process memory.
	AntigravityReasoningReplayCacheTTL = 1 * time.Hour

	// AntigravityReasoningReplayCacheMaxEntries bounds process memory for replay
	// continuity. Oldest entries are evicted first.
	AntigravityReasoningReplayCacheMaxEntries = 10240

	// AntigravityReasoningReplayCacheEvictBatchSize leaves headroom after the cache
	// reaches capacity so high write volume does not rescan the map every turn.
	AntigravityReasoningReplayCacheEvictBatchSize = 128

	minAntigravityThoughtSignatureReplayLen = 16
)

type antigravityReasoningReplayEntry struct {
	Items     [][]byte
	Timestamp time.Time
}

var (
	antigravityReasoningReplayMu      sync.Mutex
	antigravityReasoningReplayEntries = make(map[string]antigravityReasoningReplayEntry)
)

type antigravityReasoningReplayKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error)
	KVDel(ctx context.Context, keys ...string) (int64, error)
	KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

var currentAntigravityReasoningReplayKVClient = func() (antigravityReasoningReplayKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

// CacheAntigravityReasoningReplayItem stores a final GPT/Codex reasoning item for
// stateless replay. The stored item is normalized to the minimal shape accepted
// by Responses input replay.
func CacheAntigravityReasoningReplayItem(modelName, sessionKey string, item []byte) bool {
	return CacheAntigravityReasoningReplayItems(modelName, sessionKey, [][]byte{item})
}

// CacheAntigravityReasoningReplayItems stores the final GPT/Codex assistant output
// items needed to replay a stateless next turn.
func CacheAntigravityReasoningReplayItems(modelName, sessionKey string, items [][]byte) bool {
	return CacheAntigravityReasoningReplayItemsBestEffort(context.Background(), modelName, sessionKey, items)
}

// CacheAntigravityReasoningReplayItemsBestEffort stores replay items for completed response paths.
func CacheAntigravityReasoningReplayItemsBestEffort(ctx context.Context, modelName, sessionKey string, items [][]byte) bool {
	key := antigravityReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return false
	}
	normalized, ok := normalizeAntigravityReasoningReplayItems(items)
	if !ok {
		return false
	}
	if client, homeMode, errClient := currentAntigravityReasoningReplayKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("home kv best-effort antigravity reasoning replay set failed prefix=cpa:antigravity:*: %v", errClient)
			return false
		}
		raw, errMarshal := json.Marshal(normalized)
		if errMarshal != nil {
			log.Errorf("home kv best-effort antigravity reasoning replay set failed prefix=cpa:antigravity:*: %v", errMarshal)
			return false
		}
		written, errSet := client.KVSet(ctx, antigravityReasoningReplayKVKey(modelName, sessionKey), raw, homekv.KVSetOptions{EX: AntigravityReasoningReplayCacheTTL})
		if errSet != nil {
			log.Errorf("home kv best-effort antigravity reasoning replay set failed prefix=cpa:antigravity:*: %v", errSet)
			return false
		}
		return written
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	antigravityReasoningReplayMu.Lock()
	defer antigravityReasoningReplayMu.Unlock()
	antigravityReasoningReplayEntries[key] = antigravityReasoningReplayEntry{
		Items:     normalized,
		Timestamp: now,
	}
	if len(antigravityReasoningReplayEntries) > AntigravityReasoningReplayCacheMaxEntries {
		evictOldestAntigravityReasoningReplayEntries(AntigravityReasoningReplayCacheEvictBatchSize)
	}
	return true
}

// GetAntigravityReasoningReplayItem retrieves a normalized reasoning replay item.
func GetAntigravityReasoningReplayItem(modelName, sessionKey string) ([]byte, bool) {
	items, ok := GetAntigravityReasoningReplayItems(modelName, sessionKey)
	if !ok || len(items) == 0 {
		return nil, false
	}
	return items[0], true
}

// GetAntigravityReasoningReplayItems retrieves normalized assistant output items.
func GetAntigravityReasoningReplayItems(modelName, sessionKey string) ([][]byte, bool) {
	items, ok, err := GetAntigravityReasoningReplayItemsRequired(context.Background(), modelName, sessionKey)
	if err == nil {
		return items, ok
	}
	return nil, false
}

// GetAntigravityReasoningReplayItemsRequired retrieves replay items for request-time paths.
func GetAntigravityReasoningReplayItemsRequired(ctx context.Context, modelName, sessionKey string) ([][]byte, bool, error) {
	key := antigravityReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return nil, false, nil
	}
	client, homeMode, errClient := currentAntigravityReasoningReplayKVClient()
	if homeMode {
		if errClient != nil {
			return nil, false, errClient
		}
		raw, found, errGet := client.KVGet(ctx, antigravityReasoningReplayKVKey(modelName, sessionKey))
		if errGet != nil || !found {
			return nil, false, errGet
		}
		var homeItems [][]byte
		if errUnmarshal := json.Unmarshal(raw, &homeItems); errUnmarshal != nil {
			return nil, false, errUnmarshal
		}
		if _, errExpire := client.KVExpire(ctx, antigravityReasoningReplayKVKey(modelName, sessionKey), AntigravityReasoningReplayCacheTTL); errExpire != nil {
			return nil, false, errExpire
		}
		return cloneAntigravityReasoningReplayItems(homeItems), true, nil
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	antigravityReasoningReplayMu.Lock()
	defer antigravityReasoningReplayMu.Unlock()
	entry, ok := antigravityReasoningReplayEntries[key]
	if !ok {
		return nil, false, nil
	}
	if now.Sub(entry.Timestamp) > AntigravityReasoningReplayCacheTTL {
		delete(antigravityReasoningReplayEntries, key)
		return nil, false, nil
	}
	entry.Timestamp = now
	antigravityReasoningReplayEntries[key] = entry
	return cloneAntigravityReasoningReplayItems(entry.Items), true, nil
}

// DeleteAntigravityReasoningReplayItem removes one replay item after upstream rejects
// it or the caller otherwise knows it is stale.
func DeleteAntigravityReasoningReplayItem(modelName, sessionKey string) {
	if errDelete := DeleteAntigravityReasoningReplayItemRequired(context.Background(), modelName, sessionKey); errDelete != nil {
		return
	}
}

// DeleteAntigravityReasoningReplayItemRequired removes one replay item for request-time paths.
func DeleteAntigravityReasoningReplayItemRequired(ctx context.Context, modelName, sessionKey string) error {
	key := antigravityReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return nil
	}
	client, homeMode, errClient := currentAntigravityReasoningReplayKVClient()
	if homeMode {
		if errClient != nil {
			return errClient
		}
		_, errDel := client.KVDel(ctx, antigravityReasoningReplayKVKey(modelName, sessionKey))
		return errDel
	}
	antigravityReasoningReplayMu.Lock()
	delete(antigravityReasoningReplayEntries, key)
	antigravityReasoningReplayMu.Unlock()
	return nil
}

// ClearAntigravityReasoningReplayCache clears all Antigravity reasoning replay state.
func ClearAntigravityReasoningReplayCache() {
	antigravityReasoningReplayMu.Lock()
	antigravityReasoningReplayEntries = make(map[string]antigravityReasoningReplayEntry)
	antigravityReasoningReplayMu.Unlock()
}

func antigravityReasoningReplayCacheKey(modelName, sessionKey string) string {
	modelName = strings.TrimSpace(modelName)
	sessionKey = strings.TrimSpace(sessionKey)
	if modelName == "" || sessionKey == "" {
		return ""
	}
	// The session key is the continuity boundary. Keep this independent from
	// the selected upstream Codex credential so auth failover can preserve replay.
	return strings.Join([]string{"antigravity-reasoning-replay", modelName, sessionKey}, "\x00")
}

func antigravityReasoningReplayKVKey(modelName, sessionKey string) string {
	return "cpa:antigravity:reasoning-replay:" + homekv.HashKeyPart(strings.TrimSpace(modelName)) + ":" + homekv.HashKeyPart(strings.TrimSpace(sessionKey))
}

func normalizeAntigravityReasoningReplayItems(items [][]byte) ([][]byte, bool) {
	normalized := make([][]byte, 0, len(items))
	for _, item := range items {
		normalizedItem, ok := normalizeAntigravityReasoningReplayItem(item)
		if ok {
			normalized = append(normalized, normalizedItem)
		}
	}
	return normalized, len(normalized) > 0
}

func normalizeAntigravityReasoningReplayItem(item []byte) ([]byte, bool) {
	itemResult := gjson.ParseBytes(item)
	switch strings.TrimSpace(itemResult.Get("type").String()) {
	case "thought_signature":
		return normalizeAntigravityThoughtSignatureReplayItem(itemResult)
	case "function_call_part":
		return normalizeAntigravityFunctionCallPartReplayItem(itemResult)
	default:
		return nil, false
	}
}

func normalizeAntigravityThoughtSignatureReplayItem(itemResult gjson.Result) ([]byte, bool) {
	sig := strings.TrimSpace(itemResult.Get("thoughtSignature").String())
	if sig == "" {
		sig = strings.TrimSpace(itemResult.Get("thought_signature").String())
	}
	if sig == "" || len(sig) < minAntigravityThoughtSignatureReplayLen {
		return nil, false
	}
	normalized := []byte(`{"type":"thought_signature"}`)
	normalized, _ = sjson.SetBytes(normalized, "thoughtSignature", sig)
	if contentIndex := itemResult.Get("contentIndex"); contentIndex.Type == gjson.Number {
		normalized, _ = sjson.SetBytes(normalized, "contentIndex", contentIndex.Int())
	}
	if partIndex := itemResult.Get("partIndex"); partIndex.Type == gjson.Number {
		normalized, _ = sjson.SetBytes(normalized, "partIndex", partIndex.Int())
	}
	return normalized, true
}

func normalizeAntigravityFunctionCallPartReplayItem(itemResult gjson.Result) ([]byte, bool) {
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	if callID == "" {
		callID = strings.TrimSpace(itemResult.Get("id").String())
	}
	name := strings.TrimSpace(itemResult.Get("name").String())
	args := itemResult.Get("args")
	if name == "" || !args.Exists() {
		fc := itemResult.Get("functionCall")
		if fc.Exists() {
			if callID == "" {
				callID = strings.TrimSpace(fc.Get("id").String())
			}
			if name == "" {
				name = strings.TrimSpace(fc.Get("name").String())
			}
			if !args.Exists() {
				args = fc.Get("args")
			}
		}
	}
	if name == "" || !args.Exists() {
		return nil, false
	}
	normalized := []byte(`{"type":"function_call_part"}`)
	if callID != "" {
		normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
	}
	normalized, _ = sjson.SetBytes(normalized, "name", name)
	if args.Type == gjson.String {
		normalized, _ = sjson.SetBytes(normalized, "args", args.String())
	} else {
		normalized, _ = sjson.SetRawBytes(normalized, "args", []byte(args.Raw))
	}
	sig := strings.TrimSpace(itemResult.Get("thoughtSignature").String())
	if sig != "" {
		normalized, _ = sjson.SetBytes(normalized, "thoughtSignature", sig)
	}
	if contentIndex := itemResult.Get("contentIndex"); contentIndex.Type == gjson.Number {
		normalized, _ = sjson.SetBytes(normalized, "contentIndex", contentIndex.Int())
	}
	if partIndex := itemResult.Get("partIndex"); partIndex.Type == gjson.Number {
		normalized, _ = sjson.SetBytes(normalized, "partIndex", partIndex.Int())
	}
	return normalized, true
}

func cloneAntigravityReasoningReplayItems(items [][]byte) [][]byte {
	cloned := make([][]byte, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, append([]byte(nil), item...))
	}
	return cloned
}

func evictOldestAntigravityReasoningReplayEntries(count int) {
	if count <= 0 || len(antigravityReasoningReplayEntries) == 0 {
		return
	}
	type candidate struct {
		key       string
		timestamp time.Time
	}
	candidates := make([]candidate, 0, len(antigravityReasoningReplayEntries))
	for key, entry := range antigravityReasoningReplayEntries {
		candidates = append(candidates, candidate{key: key, timestamp: entry.Timestamp})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].timestamp.Before(candidates[j].timestamp)
	})
	if count > len(candidates) {
		count = len(candidates)
	}
	for i := 0; i < count; i++ {
		delete(antigravityReasoningReplayEntries, candidates[i].key)
	}
}

func purgeExpiredAntigravityReasoningReplayCache(now time.Time) {
	antigravityReasoningReplayMu.Lock()
	for key, entry := range antigravityReasoningReplayEntries {
		if now.Sub(entry.Timestamp) > AntigravityReasoningReplayCacheTTL {
			delete(antigravityReasoningReplayEntries, key)
		}
	}
	antigravityReasoningReplayMu.Unlock()
}
