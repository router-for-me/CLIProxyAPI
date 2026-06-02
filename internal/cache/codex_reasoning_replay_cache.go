package cache

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// CodexReasoningReplayCacheTTL limits how long encrypted reasoning replay
	// items stay in process memory.
	CodexReasoningReplayCacheTTL = 1 * time.Hour

	// CodexReasoningReplayCacheMaxEntries bounds process memory for replay
	// continuity. Oldest entries are evicted first.
	CodexReasoningReplayCacheMaxEntries = 10240

	// CodexReasoningReplayCacheEvictBatchSize leaves headroom after the cache
	// reaches capacity so high write volume does not rescan the map every turn.
	CodexReasoningReplayCacheEvictBatchSize = 128
)

type codexReasoningReplayEntry struct {
	Items     [][]byte
	Timestamp time.Time
}

var (
	codexReasoningReplayMu      sync.Mutex
	codexReasoningReplayEntries = make(map[string]codexReasoningReplayEntry)
)

// CacheCodexReasoningReplayItem stores a final GPT/Codex reasoning item for
// stateless replay. The stored item is normalized to the minimal shape accepted
// by Responses input replay.
func CacheCodexReasoningReplayItem(modelName, sessionKey string, item []byte) bool {
	return CacheCodexReasoningReplayItems(modelName, sessionKey, [][]byte{item})
}

// CacheCodexReasoningReplayItems stores the final GPT/Codex assistant output
// items needed to replay a stateless next turn.
func CacheCodexReasoningReplayItems(modelName, sessionKey string, items [][]byte) bool {
	key := codexReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return false
	}
	normalized, ok := normalizeCodexReasoningReplayItems(items)
	if !ok {
		return false
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	codexReasoningReplayMu.Lock()
	defer codexReasoningReplayMu.Unlock()
	codexReasoningReplayEntries[key] = codexReasoningReplayEntry{
		Items:     normalized,
		Timestamp: now,
	}
	if len(codexReasoningReplayEntries) > CodexReasoningReplayCacheMaxEntries {
		evictOldestCodexReasoningReplayEntries(CodexReasoningReplayCacheEvictBatchSize)
	}
	return true
}

// GetCodexReasoningReplayItem retrieves a normalized reasoning replay item.
func GetCodexReasoningReplayItem(modelName, sessionKey string) ([]byte, bool) {
	items, ok := GetCodexReasoningReplayItems(modelName, sessionKey)
	if !ok || len(items) == 0 {
		return nil, false
	}
	return items[0], true
}

// GetCodexReasoningReplayItems retrieves normalized assistant output items.
func GetCodexReasoningReplayItems(modelName, sessionKey string) ([][]byte, bool) {
	key := codexReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return nil, false
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	codexReasoningReplayMu.Lock()
	defer codexReasoningReplayMu.Unlock()
	entry, ok := codexReasoningReplayEntries[key]
	if !ok {
		return nil, false
	}
	if now.Sub(entry.Timestamp) > CodexReasoningReplayCacheTTL {
		delete(codexReasoningReplayEntries, key)
		return nil, false
	}
	entry.Timestamp = now
	codexReasoningReplayEntries[key] = entry
	return cloneCodexReasoningReplayItems(entry.Items), true
}

// DeleteCodexReasoningReplayItem removes one replay item after upstream rejects
// it or the caller otherwise knows it is stale.
func DeleteCodexReasoningReplayItem(modelName, sessionKey string) {
	key := codexReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return
	}
	codexReasoningReplayMu.Lock()
	delete(codexReasoningReplayEntries, key)
	codexReasoningReplayMu.Unlock()
}

// ClearCodexReasoningReplayCache clears all Codex reasoning replay state.
func ClearCodexReasoningReplayCache() {
	codexReasoningReplayMu.Lock()
	codexReasoningReplayEntries = make(map[string]codexReasoningReplayEntry)
	codexReasoningReplayMu.Unlock()
}

func codexReasoningReplayCacheKey(modelName, sessionKey string) string {
	modelName = strings.TrimSpace(modelName)
	sessionKey = strings.TrimSpace(sessionKey)
	if modelName == "" || sessionKey == "" {
		return ""
	}
	// The session key is the continuity boundary. Keep this independent from
	// the selected upstream Codex credential so auth failover can preserve replay.
	return strings.Join([]string{"codex-reasoning-replay", modelName, sessionKey}, "\x00")
}

func normalizeCodexReasoningReplayItems(items [][]byte) ([][]byte, bool) {
	normalized := make([][]byte, 0, len(items))
	for _, item := range items {
		normalizedItem, ok := normalizeCodexReasoningReplayItem(item)
		if ok {
			normalized = append(normalized, normalizedItem)
		}
	}
	return normalized, len(normalized) > 0
}

func normalizeCodexReasoningReplayItem(item []byte) ([]byte, bool) {
	itemResult := gjson.ParseBytes(item)
	switch strings.TrimSpace(itemResult.Get("type").String()) {
	case "reasoning":
		return normalizeCodexReasoningReplayReasoningItem(itemResult)
	case "function_call":
		return normalizeCodexReasoningReplayFunctionCallItem(itemResult)
	case "custom_tool_call":
		return normalizeCodexReasoningReplayCustomToolCallItem(itemResult)
	default:
		return nil, false
	}
}

func normalizeCodexReasoningReplayReasoningItem(itemResult gjson.Result) ([]byte, bool) {
	encryptedContentResult := itemResult.Get("encrypted_content")
	if encryptedContentResult.Type != gjson.String {
		return nil, false
	}
	encryptedContent := encryptedContentResult.String()
	if encryptedContent != strings.TrimSpace(encryptedContent) {
		return nil, false
	}
	if _, err := signature.InspectGPTReasoningSignature(encryptedContent); err != nil {
		return nil, false
	}

	normalized := []byte(`{"type":"reasoning","summary":[],"content":null}`)
	normalized, _ = sjson.SetBytes(normalized, "encrypted_content", encryptedContent)
	return normalized, true
}

func normalizeCodexReasoningReplayFunctionCallItem(itemResult gjson.Result) ([]byte, bool) {
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

func normalizeCodexReasoningReplayCustomToolCallItem(itemResult gjson.Result) ([]byte, bool) {
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

func cloneCodexReasoningReplayItems(items [][]byte) [][]byte {
	cloned := make([][]byte, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, append([]byte(nil), item...))
	}
	return cloned
}

func evictOldestCodexReasoningReplayEntries(count int) {
	if count <= 0 || len(codexReasoningReplayEntries) == 0 {
		return
	}
	type candidate struct {
		key       string
		timestamp time.Time
	}
	candidates := make([]candidate, 0, len(codexReasoningReplayEntries))
	for key, entry := range codexReasoningReplayEntries {
		candidates = append(candidates, candidate{key: key, timestamp: entry.Timestamp})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].timestamp.Before(candidates[j].timestamp)
	})
	if count > len(candidates) {
		count = len(candidates)
	}
	for i := 0; i < count; i++ {
		delete(codexReasoningReplayEntries, candidates[i].key)
	}
}

func purgeExpiredCodexReasoningReplayCache(now time.Time) {
	codexReasoningReplayMu.Lock()
	for key, entry := range codexReasoningReplayEntries {
		if now.Sub(entry.Timestamp) > CodexReasoningReplayCacheTTL {
			delete(codexReasoningReplayEntries, key)
		}
	}
	codexReasoningReplayMu.Unlock()
}
