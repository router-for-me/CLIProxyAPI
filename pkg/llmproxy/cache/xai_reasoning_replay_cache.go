package cache

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// XAIReasoningReplayCacheTTL limits how long encrypted reasoning replay
	// items stay in process memory.
	XAIReasoningReplayCacheTTL = 1 * time.Hour

	// XAIReasoningReplayCacheMaxEntries bounds process memory for replay
	// continuity. Oldest entries are evicted first.
	XAIReasoningReplayCacheMaxEntries = 10240

	// XAIReasoningReplayCacheEvictBatchSize leaves headroom after the cache
	// reaches capacity so high write volume does not rescan the map every turn.
	XAIReasoningReplayCacheEvictBatchSize = 128
)

type xaiReasoningReplayEntry struct {
	Items     [][]byte
	Timestamp time.Time
}

var (
	xaiReasoningReplayMu      sync.Mutex
	xaiReasoningReplayEntries = make(map[string]xaiReasoningReplayEntry)
)

type xaiReasoningReplayKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error)
	KVDel(ctx context.Context, keys ...string) (int64, error)
	KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

var currentXAIReasoningReplayKVClient = func() (xaiReasoningReplayKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

// CacheXAIReasoningReplayItem stores a final Grok reasoning item for stateless
// replay. The stored item is normalized to the minimal shape accepted by
// Responses input replay.
func CacheXAIReasoningReplayItem(modelName, sessionKey string, item []byte) bool {
	return CacheXAIReasoningReplayItems(modelName, sessionKey, [][]byte{item})
}

// CacheXAIReasoningReplayItems stores the final Grok assistant output items
// needed to replay a stateless next turn.
func CacheXAIReasoningReplayItems(modelName, sessionKey string, items [][]byte) bool {
	return CacheXAIReasoningReplayItemsBestEffort(context.Background(), modelName, sessionKey, items)
}

// CacheXAIReasoningReplayItemsBestEffort stores replay items for completed response paths.
func CacheXAIReasoningReplayItemsBestEffort(ctx context.Context, modelName, sessionKey string, items [][]byte) bool {
	key := xaiReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return false
	}
	normalized, ok := normalizeXAIReasoningReplayItems(items)
	if !ok {
		return false
	}
	if client, homeMode, errClient := currentXAIReasoningReplayKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("home kv best-effort xai reasoning replay set failed prefix=cpa:xai:*: %v", errClient)
			return false
		}
		raw, errMarshal := json.Marshal(normalized)
		if errMarshal != nil {
			log.Errorf("home kv best-effort xai reasoning replay set failed prefix=cpa:xai:*: %v", errMarshal)
			return false
		}
		written, errSet := client.KVSet(ctx, xaiReasoningReplayKVKey(modelName, sessionKey), raw, homekv.KVSetOptions{EX: XAIReasoningReplayCacheTTL})
		if errSet != nil {
			log.Errorf("home kv best-effort xai reasoning replay set failed prefix=cpa:xai:*: %v", errSet)
			return false
		}
		return written
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	xaiReasoningReplayMu.Lock()
	defer xaiReasoningReplayMu.Unlock()
	xaiReasoningReplayEntries[key] = xaiReasoningReplayEntry{
		Items:     normalized,
		Timestamp: now,
	}
	if len(xaiReasoningReplayEntries) > XAIReasoningReplayCacheMaxEntries {
		evictOldestXAIReasoningReplayEntriesLocked(XAIReasoningReplayCacheEvictBatchSize)
	}
	return true
}

// GetXAIReasoningReplayItem retrieves a normalized reasoning replay item.
func GetXAIReasoningReplayItem(modelName, sessionKey string) ([]byte, bool) {
	items, ok := GetXAIReasoningReplayItems(modelName, sessionKey)
	if !ok || len(items) == 0 {
		return nil, false
	}
	return items[0], true
}

// GetXAIReasoningReplayItems retrieves normalized assistant output items.
func GetXAIReasoningReplayItems(modelName, sessionKey string) ([][]byte, bool) {
	items, ok, err := GetXAIReasoningReplayItemsRequired(context.Background(), modelName, sessionKey)
	if err == nil {
		return items, ok
	}
	return nil, false
}

// GetXAIReasoningReplayItemsRequired retrieves replay items for request-time paths.
func GetXAIReasoningReplayItemsRequired(ctx context.Context, modelName, sessionKey string) ([][]byte, bool, error) {
	key := xaiReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return nil, false, nil
	}
	client, homeMode, errClient := currentXAIReasoningReplayKVClient()
	if homeMode {
		if errClient != nil {
			return nil, false, errClient
		}
		raw, found, errGet := client.KVGet(ctx, xaiReasoningReplayKVKey(modelName, sessionKey))
		if errGet != nil || !found {
			return nil, false, errGet
		}
		var homeItems [][]byte
		if errUnmarshal := json.Unmarshal(raw, &homeItems); errUnmarshal != nil {
			return nil, false, errUnmarshal
		}
		if _, errExpire := client.KVExpire(ctx, xaiReasoningReplayKVKey(modelName, sessionKey), XAIReasoningReplayCacheTTL); errExpire != nil {
			log.Warnf("home kv xai reasoning replay expire failed prefix=cpa:xai:*: %v", errExpire)
		}
		return cloneXAIReasoningReplayItems(homeItems), true, nil
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	xaiReasoningReplayMu.Lock()
	defer xaiReasoningReplayMu.Unlock()
	entry, ok := xaiReasoningReplayEntries[key]
	if !ok {
		return nil, false, nil
	}
	if now.Sub(entry.Timestamp) > XAIReasoningReplayCacheTTL {
		delete(xaiReasoningReplayEntries, key)
		return nil, false, nil
	}
	entry.Timestamp = now
	xaiReasoningReplayEntries[key] = entry
	return cloneXAIReasoningReplayItems(entry.Items), true, nil
}

// DeleteXAIReasoningReplayItem removes one replay item after upstream rejects
// it or the caller otherwise knows it is stale.
func DeleteXAIReasoningReplayItem(modelName, sessionKey string) {
	if errDelete := DeleteXAIReasoningReplayItemRequired(context.Background(), modelName, sessionKey); errDelete != nil {
		return
	}
}

// DeleteXAIReasoningReplayItemRequired removes one replay item for request-time paths.
func DeleteXAIReasoningReplayItemRequired(ctx context.Context, modelName, sessionKey string) error {
	key := xaiReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return nil
	}
	client, homeMode, errClient := currentXAIReasoningReplayKVClient()
	if homeMode {
		if errClient != nil {
			return errClient
		}
		_, errDel := client.KVDel(ctx, xaiReasoningReplayKVKey(modelName, sessionKey))
		return errDel
	}
	xaiReasoningReplayMu.Lock()
	delete(xaiReasoningReplayEntries, key)
	xaiReasoningReplayMu.Unlock()
	return nil
}

// ClearXAIReasoningReplayCache clears all xAI reasoning replay state.
func ClearXAIReasoningReplayCache() {
	xaiReasoningReplayMu.Lock()
	xaiReasoningReplayEntries = make(map[string]xaiReasoningReplayEntry)
	xaiReasoningReplayMu.Unlock()
}

func xaiReasoningReplayCacheKey(modelName, sessionKey string) string {
	modelName = strings.TrimSpace(modelName)
	sessionKey = strings.TrimSpace(sessionKey)
	if modelName == "" || sessionKey == "" {
		return ""
	}
	// The session key is the continuity boundary. Keep this independent from
	// the selected upstream xAI credential so auth failover can preserve replay.
	return strings.Join([]string{"xai-reasoning-replay", modelName, sessionKey}, "\x00")
}

func xaiReasoningReplayKVKey(modelName, sessionKey string) string {
	return "cpa:xai:reasoning-replay:" + homekv.HashKeyPart(strings.TrimSpace(modelName)) + ":" + homekv.HashKeyPart(strings.TrimSpace(sessionKey))
}

func normalizeXAIReasoningReplayItems(items [][]byte) ([][]byte, bool) {
	normalized := make([][]byte, 0, len(items))
	for _, item := range items {
		normalizedItem, ok := normalizeXAIReasoningReplayItem(item)
		if ok {
			normalized = append(normalized, normalizedItem)
		}
	}
	return normalized, len(normalized) > 0
}

func normalizeXAIReasoningReplayItem(item []byte) ([]byte, bool) {
	itemResult := gjson.ParseBytes(item)
	switch strings.TrimSpace(itemResult.Get("type").String()) {
	case "reasoning":
		return normalizeXAIReasoningReplayReasoningItem(itemResult)
	case "function_call":
		return normalizeXAIReasoningReplayFunctionCallItem(itemResult)
	case "custom_tool_call":
		return normalizeXAIReasoningReplayCustomToolCallItem(itemResult)
	default:
		return nil, false
	}
}

func normalizeXAIReasoningReplayReasoningItem(itemResult gjson.Result) ([]byte, bool) {
	encryptedContentResult := itemResult.Get("encrypted_content")
	if encryptedContentResult.Type != gjson.String {
		return nil, false
	}
	encryptedContent := encryptedContentResult.String()
	if encryptedContent != strings.TrimSpace(encryptedContent) {
		return nil, false
	}
	if _, err := signature.InspectGrokEncryptedContent(encryptedContent); err != nil {
		return nil, false
	}

	normalized := []byte(`{"type":"reasoning","summary":[],"content":null}`)
	normalized, _ = sjson.SetBytes(normalized, "encrypted_content", encryptedContent)
	return normalized, true
}

func normalizeXAIReasoningReplayFunctionCallItem(itemResult gjson.Result) ([]byte, bool) {
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	name := strings.TrimSpace(itemResult.Get("name").String())
	arguments := itemResult.Get("arguments")
	if callID == "" || name == "" || arguments.Type != gjson.String {
		return nil, false
	}

	normalized := []byte(`{"type":"function_call"}`)
	normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
	normalized, _ = sjson.SetBytes(normalized, "name", name)
	normalized, _ = sjson.SetBytes(normalized, "arguments", arguments.String())
	return normalized, true
}

func normalizeXAIReasoningReplayCustomToolCallItem(itemResult gjson.Result) ([]byte, bool) {
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	name := strings.TrimSpace(itemResult.Get("name").String())
	input := itemResult.Get("input")
	if callID == "" || name == "" || !input.Exists() {
		return nil, false
	}

	normalized := []byte(`{"type":"custom_tool_call","status":"completed"}`)
	if status := strings.TrimSpace(itemResult.Get("status").String()); status != "" {
		normalized, _ = sjson.SetBytes(normalized, "status", status)
	}
	normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
	normalized, _ = sjson.SetBytes(normalized, "name", name)
	if input.Type == gjson.String {
		normalized, _ = sjson.SetBytes(normalized, "input", input.String())
	} else {
		normalized, _ = sjson.SetRawBytes(normalized, "input", []byte(input.Raw))
	}
	return normalized, true
}

func cloneXAIReasoningReplayItems(items [][]byte) [][]byte {
	cloned := make([][]byte, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, append([]byte(nil), item...))
	}
	return cloned
}

func evictOldestXAIReasoningReplayEntriesLocked(count int) {
	if count <= 0 || len(xaiReasoningReplayEntries) == 0 {
		return
	}
	type candidate struct {
		key       string
		timestamp time.Time
	}
	candidates := make([]candidate, 0, len(xaiReasoningReplayEntries))
	for key, entry := range xaiReasoningReplayEntries {
		candidates = append(candidates, candidate{key: key, timestamp: entry.Timestamp})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].timestamp.Before(candidates[j].timestamp)
	})
	if count > len(candidates) {
		count = len(candidates)
	}
	for i := 0; i < count; i++ {
		delete(xaiReasoningReplayEntries, candidates[i].key)
	}
}

func purgeExpiredXAIReasoningReplayCache(now time.Time) {
	xaiReasoningReplayMu.Lock()
	for key, entry := range xaiReasoningReplayEntries {
		if now.Sub(entry.Timestamp) > XAIReasoningReplayCacheTTL {
			delete(xaiReasoningReplayEntries, key)
		}
	}
	xaiReasoningReplayMu.Unlock()
}
