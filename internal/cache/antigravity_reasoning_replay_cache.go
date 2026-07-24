package cache

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
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

	// AntigravityReasoningReplayCacheMaxItemsPerEntry and MaxBytesPerEntry
	// bound one logical conversation. Oversized chains are not partially cached,
	// because dropping an arbitrary prefix would break native signature ordering.
	AntigravityReasoningReplayCacheMaxItemsPerEntry = 4096
	AntigravityReasoningReplayCacheMaxBytesPerEntry = 16 << 20

	// JSON encodes each normalized []byte item as base64. Leave enough room for
	// that expansion while rejecting oversized Home values before unmarshalling.
	antigravityReasoningReplayCacheMaxSerializedBytes = 24 << 20
)

type antigravityReasoningReplayEntry struct {
	Items     [][]byte
	Timestamp time.Time
	Revision  uint64
	Branch    string
	Deleted   bool
}

const antigravityReasoningReplayGenerationItemType = "cpa_antigravity_replay_generation"

// AntigravityReasoningReplaySnapshot identifies the exact replay state read for
// one request. Its fields are intentionally opaque outside this package.
type AntigravityReasoningReplaySnapshot struct {
	raw           []byte
	items         [][]byte
	loaded        bool
	found         bool
	revision      uint64
	branch        string
	evictionEpoch uint64
}

var (
	antigravityReasoningReplayMu            sync.Mutex
	antigravityReasoningReplayEntries       = make(map[string]antigravityReasoningReplayEntry)
	antigravityReasoningReplayNextRevision  uint64
	antigravityReasoningReplayEvictionEpoch uint64
)

type antigravityReasoningReplayKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error)
	KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error)
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
		raw, errMarshal := marshalAntigravityReasoningReplayHomeValue(normalized, "")
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
	antigravityReasoningReplayNextRevision++
	antigravityReasoningReplayEntries[key] = antigravityReasoningReplayEntry{
		Items:     normalized,
		Timestamp: now,
		Revision:  antigravityReasoningReplayNextRevision,
		Branch:    newAntigravityReasoningReplayGeneration(),
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
	items, _, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(ctx, modelName, sessionKey)
	return items, found, errGet
}

// GetAntigravityReasoningReplayItemsWithSnapshotRequired retrieves replay items
// and the exact cache state that guarded this request.
func GetAntigravityReasoningReplayItemsWithSnapshotRequired(ctx context.Context, modelName, sessionKey string) ([][]byte, AntigravityReasoningReplaySnapshot, bool, error) {
	key := antigravityReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return nil, AntigravityReasoningReplaySnapshot{}, false, nil
	}
	client, homeMode, errClient := currentAntigravityReasoningReplayKVClient()
	if homeMode {
		if errClient != nil {
			return nil, AntigravityReasoningReplaySnapshot{}, false, errClient
		}
		kvKey := antigravityReasoningReplayKVKey(modelName, sessionKey)
		var raw []byte
		found := false
		for attempt := 0; attempt < 4; attempt++ {
			currentRaw, currentFound, errGet := client.KVGet(ctx, kvKey)
			if errGet != nil {
				return nil, AntigravityReasoningReplaySnapshot{loaded: true}, false, errGet
			}
			if currentFound {
				raw = currentRaw
				found = true
				break
			}
			reservation := newAntigravityReasoningReplayTombstone()
			swapped, errReserve := client.KVCompareAndSwap(ctx, kvKey, nil, false, reservation, AntigravityReasoningReplayCacheTTL)
			if errReserve != nil {
				return nil, AntigravityReasoningReplaySnapshot{loaded: true}, false, errReserve
			}
			if swapped {
				raw = reservation
				found = true
				break
			}
		}
		if !found {
			return nil, AntigravityReasoningReplaySnapshot{loaded: true}, false, fmt.Errorf("could not fence absent antigravity reasoning replay state")
		}
		if len(raw) > antigravityReasoningReplayCacheMaxSerializedBytes {
			return nil, AntigravityReasoningReplaySnapshot{loaded: true, found: true}, false, nil
		}
		snapshot := AntigravityReasoningReplaySnapshot{raw: append([]byte(nil), raw...), loaded: true, found: true}
		homeItems, deleted, _, branch, okDecode := decodeAntigravityReasoningReplayHomeValue(raw)
		snapshot.branch = branch
		if !okDecode || deleted || len(homeItems) == 0 {
			return nil, snapshot, false, nil
		}
		if len(homeItems) > AntigravityReasoningReplayCacheMaxItemsPerEntry {
			return nil, snapshot, false, nil
		}
		normalized, okNormalize := normalizeAntigravityReasoningReplayItems(homeItems)
		if !okNormalize || len(normalized) != len(homeItems) {
			return nil, snapshot, false, nil
		}
		snapshot.items = cloneAntigravityReasoningReplayItems(normalized)
		if _, errExpire := client.KVExpire(ctx, kvKey, AntigravityReasoningReplayCacheTTL); errExpire != nil {
			return nil, snapshot, false, errExpire
		}
		return normalized, snapshot, true, nil
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	antigravityReasoningReplayMu.Lock()
	defer antigravityReasoningReplayMu.Unlock()
	entry, ok := antigravityReasoningReplayEntries[key]
	if !ok {
		return nil, AntigravityReasoningReplaySnapshot{loaded: true, evictionEpoch: antigravityReasoningReplayEvictionEpoch}, false, nil
	}
	if now.Sub(entry.Timestamp) > AntigravityReasoningReplayCacheTTL {
		antigravityReasoningReplayEvictionEpoch++
		delete(antigravityReasoningReplayEntries, key)
		return nil, AntigravityReasoningReplaySnapshot{loaded: true, evictionEpoch: antigravityReasoningReplayEvictionEpoch}, false, nil
	}
	entry.Timestamp = now
	antigravityReasoningReplayEntries[key] = entry
	snapshot := AntigravityReasoningReplaySnapshot{loaded: true, found: true, revision: entry.Revision, branch: entry.Branch, evictionEpoch: antigravityReasoningReplayEvictionEpoch}
	if entry.Deleted || len(entry.Items) == 0 {
		return nil, snapshot, false, nil
	}
	snapshot.items = cloneAntigravityReasoningReplayItems(entry.Items)
	return cloneAntigravityReasoningReplayItems(entry.Items), snapshot, true, nil
}

// ReplaceAntigravityReasoningReplayItemsIfUnchanged publishes a completed chain
// only when no newer request has changed the state read by this request.
func ReplaceAntigravityReasoningReplayItemsIfUnchanged(ctx context.Context, modelName, sessionKey string, snapshot AntigravityReasoningReplaySnapshot, items [][]byte) (bool, error) {
	key := antigravityReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return false, nil
	}
	normalized, okNormalize := normalizeAntigravityReasoningReplayItems(items)
	if !okNormalize {
		return false, fmt.Errorf("invalid antigravity reasoning replay items")
	}
	if !snapshot.loaded {
		return CacheAntigravityReasoningReplayItemsBestEffort(ctx, modelName, sessionKey, normalized), nil
	}
	client, homeMode, errClient := currentAntigravityReasoningReplayKVClient()
	if homeMode {
		if errClient != nil {
			return false, errClient
		}
		kvKey := antigravityReasoningReplayKVKey(modelName, sessionKey)
		expectedRaw := snapshot.raw
		expectedFound := snapshot.found
		branch := snapshot.branch
		if branch == "" || !antigravityReasoningReplayItemsPrefix(snapshot.items, normalized) {
			branch = newAntigravityReasoningReplayGeneration()
		}
		for attempt := 0; attempt < 4; attempt++ {
			raw, errMarshal := marshalAntigravityReasoningReplayHomeValue(normalized, branch)
			if errMarshal != nil {
				return false, errMarshal
			}
			swapped, errCAS := client.KVCompareAndSwap(ctx, kvKey, expectedRaw, expectedFound, raw, AntigravityReasoningReplayCacheTTL)
			if errCAS != nil || swapped {
				return swapped, errCAS
			}
			currentRaw, currentFound, errGet := client.KVGet(ctx, kvKey)
			if errGet != nil || !currentFound {
				return false, errGet
			}
			if len(currentRaw) > antigravityReasoningReplayCacheMaxSerializedBytes {
				return false, nil
			}
			currentItems, deleted, _, currentBranch, okDecode := decodeAntigravityReasoningReplayHomeValue(currentRaw)
			if !okDecode || deleted || snapshot.branch == "" || currentBranch != snapshot.branch {
				return false, nil
			}
			normalizedCurrent, okNormalizeCurrent := normalizeAntigravityReasoningReplayItems(currentItems)
			if !okNormalizeCurrent || len(normalizedCurrent) != len(currentItems) || !antigravityReasoningReplayItemsPrefix(normalizedCurrent, normalized) {
				return false, nil
			}
			expectedRaw = currentRaw
			expectedFound = true
		}
		return false, nil
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	antigravityReasoningReplayMu.Lock()
	defer antigravityReasoningReplayMu.Unlock()
	entry, found := antigravityReasoningReplayEntries[key]
	matchesSnapshot := found == snapshot.found && ((found && entry.Revision == snapshot.revision) || (!found && snapshot.evictionEpoch == antigravityReasoningReplayEvictionEpoch))
	isDescendant := found && !entry.Deleted && snapshot.branch != "" && entry.Branch == snapshot.branch && antigravityReasoningReplayItemsPrefix(entry.Items, normalized)
	if !matchesSnapshot && !isDescendant {
		return false, nil
	}
	branch := snapshot.branch
	if branch == "" || (matchesSnapshot && !antigravityReasoningReplayItemsPrefix(snapshot.items, normalized)) {
		branch = newAntigravityReasoningReplayGeneration()
	}
	antigravityReasoningReplayNextRevision++
	antigravityReasoningReplayEntries[key] = antigravityReasoningReplayEntry{Items: normalized, Timestamp: now, Revision: antigravityReasoningReplayNextRevision, Branch: branch}
	if len(antigravityReasoningReplayEntries) > AntigravityReasoningReplayCacheMaxEntries {
		evictOldestAntigravityReasoningReplayEntries(AntigravityReasoningReplayCacheEvictBatchSize)
	}
	return true, nil
}

// DeleteAntigravityReasoningReplayItemsIfUnchanged clears replay state only when
// it still matches the state read for this request.
func DeleteAntigravityReasoningReplayItemsIfUnchanged(ctx context.Context, modelName, sessionKey string, snapshot AntigravityReasoningReplaySnapshot) (bool, error) {
	key := antigravityReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return false, nil
	}
	if !snapshot.loaded {
		return true, DeleteAntigravityReasoningReplayItemRequired(ctx, modelName, sessionKey)
	}
	client, homeMode, errClient := currentAntigravityReasoningReplayKVClient()
	if homeMode {
		if errClient != nil {
			return false, errClient
		}
		return client.KVCompareAndSwap(ctx, antigravityReasoningReplayKVKey(modelName, sessionKey), snapshot.raw, snapshot.found, newAntigravityReasoningReplayTombstone(), AntigravityReasoningReplayCacheTTL)
	}
	cacheCleanupOnce.Do(startCacheCleanup)
	antigravityReasoningReplayMu.Lock()
	defer antigravityReasoningReplayMu.Unlock()
	entry, found := antigravityReasoningReplayEntries[key]
	if found != snapshot.found || (found && entry.Revision != snapshot.revision) || (!found && snapshot.evictionEpoch != antigravityReasoningReplayEvictionEpoch) {
		return false, nil
	}
	antigravityReasoningReplayNextRevision++
	antigravityReasoningReplayEntries[key] = antigravityReasoningReplayEntry{Timestamp: time.Now(), Revision: antigravityReasoningReplayNextRevision, Branch: newAntigravityReasoningReplayGeneration(), Deleted: true}
	if len(antigravityReasoningReplayEntries) > AntigravityReasoningReplayCacheMaxEntries {
		evictOldestAntigravityReasoningReplayEntries(AntigravityReasoningReplayCacheEvictBatchSize)
	}
	return true, nil
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
		_, errSet := client.KVSet(ctx, antigravityReasoningReplayKVKey(modelName, sessionKey), newAntigravityReasoningReplayTombstone(), homekv.KVSetOptions{EX: AntigravityReasoningReplayCacheTTL})
		return errSet
	}
	cacheCleanupOnce.Do(startCacheCleanup)
	antigravityReasoningReplayMu.Lock()
	antigravityReasoningReplayNextRevision++
	antigravityReasoningReplayEntries[key] = antigravityReasoningReplayEntry{Timestamp: time.Now(), Revision: antigravityReasoningReplayNextRevision, Branch: newAntigravityReasoningReplayGeneration(), Deleted: true}
	if len(antigravityReasoningReplayEntries) > AntigravityReasoningReplayCacheMaxEntries {
		evictOldestAntigravityReasoningReplayEntries(AntigravityReasoningReplayCacheEvictBatchSize)
	}
	antigravityReasoningReplayMu.Unlock()
	return nil
}

func newAntigravityReasoningReplayGeneration() string {
	var nonce [16]byte
	if _, errRead := rand.Read(nonce[:]); errRead != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", nonce[:])
}

func marshalAntigravityReasoningReplayHomeValue(items [][]byte, branch string) ([]byte, error) {
	if branch == "" {
		branch = newAntigravityReasoningReplayGeneration()
	}
	marker := []byte(`{"type":"","generation":"","branch":""}`)
	marker, _ = sjson.SetBytes(marker, "type", antigravityReasoningReplayGenerationItemType)
	marker, _ = sjson.SetBytes(marker, "generation", newAntigravityReasoningReplayGeneration())
	marker, _ = sjson.SetBytes(marker, "branch", branch)
	stored := make([][]byte, 0, len(items)+1)
	stored = append(stored, marker)
	stored = append(stored, items...)
	return json.Marshal(stored)
}

func decodeAntigravityReasoningReplayHomeValue(raw []byte) (items [][]byte, deleted bool, generation, branch string, ok bool) {
	if errUnmarshal := json.Unmarshal(raw, &items); errUnmarshal != nil {
		return nil, false, "", "", false
	}
	if len(items) == 0 || strings.TrimSpace(gjson.GetBytes(items[0], "type").String()) != antigravityReasoningReplayGenerationItemType {
		return items, false, "", "", true
	}
	marker := gjson.ParseBytes(items[0])
	deleted = marker.Get("deleted").Bool()
	generation = strings.TrimSpace(marker.Get("generation").String())
	branch = strings.TrimSpace(marker.Get("branch").String())
	return items[1:], deleted, generation, branch, true
}

func antigravityReasoningReplayItemsPrefix(prefix, items [][]byte) bool {
	if len(prefix) > len(items) {
		return false
	}
	for index := range prefix {
		if !bytes.Equal(prefix[index], items[index]) {
			return false
		}
	}
	return true
}

func newAntigravityReasoningReplayTombstone() []byte {
	marker := []byte(`{"type":"","generation":"","branch":"","deleted":true}`)
	marker, _ = sjson.SetBytes(marker, "type", antigravityReasoningReplayGenerationItemType)
	marker, _ = sjson.SetBytes(marker, "generation", newAntigravityReasoningReplayGeneration())
	marker, _ = sjson.SetBytes(marker, "branch", newAntigravityReasoningReplayGeneration())
	raw, _ := json.Marshal([][]byte{marker})
	return raw
}

// ClearAntigravityReasoningReplayCache clears all Antigravity reasoning replay state.
func ClearAntigravityReasoningReplayCache() {
	antigravityReasoningReplayMu.Lock()
	antigravityReasoningReplayEntries = make(map[string]antigravityReasoningReplayEntry)
	antigravityReasoningReplayEvictionEpoch++
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
	if len(items) > AntigravityReasoningReplayCacheMaxItemsPerEntry {
		return nil, false
	}
	normalized := make([][]byte, 0, len(items))
	totalBytes := 0
	for _, item := range items {
		normalizedItem, ok := normalizeAntigravityReasoningReplayItem(item)
		if ok {
			totalBytes += len(normalizedItem)
			if totalBytes > AntigravityReasoningReplayCacheMaxBytesPerEntry {
				return nil, false
			}
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
	if sig == "" || sig == "skip_thought_signature_validator" || len(sig) < minAntigravityThoughtSignatureReplayLen {
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
	if targetKind := strings.TrimSpace(itemResult.Get("targetKind").String()); targetKind == "text" || targetKind == "thought" {
		normalized, _ = sjson.SetBytes(normalized, "targetKind", targetKind)
	}
	if targetHash := strings.TrimSpace(itemResult.Get("targetHash").String()); targetHash != "" {
		normalized, _ = sjson.SetBytes(normalized, "targetHash", targetHash)
	}
	if targetOccurrence := itemResult.Get("targetOccurrence"); targetOccurrence.Type == gjson.Number && targetOccurrence.Int() >= 0 {
		normalized, _ = sjson.SetBytes(normalized, "targetOccurrence", targetOccurrence.Int())
	}
	if contextHash := strings.TrimSpace(itemResult.Get("contextHash").String()); contextHash != "" {
		normalized, _ = sjson.SetBytes(normalized, "contextHash", contextHash)
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
	if sig != "" && sig != "skip_thought_signature_validator" {
		normalized, _ = sjson.SetBytes(normalized, "thoughtSignature", sig)
	}
	if contentIndex := itemResult.Get("contentIndex"); contentIndex.Type == gjson.Number {
		normalized, _ = sjson.SetBytes(normalized, "contentIndex", contentIndex.Int())
	}
	if partIndex := itemResult.Get("partIndex"); partIndex.Type == gjson.Number {
		normalized, _ = sjson.SetBytes(normalized, "partIndex", partIndex.Int())
	}
	if targetOccurrence := itemResult.Get("targetOccurrence"); targetOccurrence.Type == gjson.Number && targetOccurrence.Int() >= 0 {
		normalized, _ = sjson.SetBytes(normalized, "targetOccurrence", targetOccurrence.Int())
	}
	if contextHash := strings.TrimSpace(itemResult.Get("contextHash").String()); contextHash != "" {
		normalized, _ = sjson.SetBytes(normalized, "contextHash", contextHash)
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
		antigravityReasoningReplayEvictionEpoch++
		delete(antigravityReasoningReplayEntries, candidates[i].key)
	}
}

func purgeExpiredAntigravityReasoningReplayCache(now time.Time) {
	antigravityReasoningReplayMu.Lock()
	for key, entry := range antigravityReasoningReplayEntries {
		if now.Sub(entry.Timestamp) > AntigravityReasoningReplayCacheTTL {
			antigravityReasoningReplayEvictionEpoch++
			delete(antigravityReasoningReplayEntries, key)
		}
	}
	antigravityReasoningReplayMu.Unlock()
}
