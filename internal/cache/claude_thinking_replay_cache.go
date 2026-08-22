package cache

import (
	"bytes"
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
	// ClaudeThinkingReplayCacheTTL limits how long signed assistant turns stay replayable.
	ClaudeThinkingReplayCacheTTL = 1 * time.Hour

	// ClaudeThinkingReplayCacheMaxEntries bounds process memory used by Claude replay continuity.
	ClaudeThinkingReplayCacheMaxEntries = 10240

	// ClaudeThinkingReplayCacheEvictBatchSize leaves headroom after reaching capacity.
	ClaudeThinkingReplayCacheEvictBatchSize = 128

	// ClaudeThinkingReplayCacheMaxBytesPerSession bounds all cached assistant turns for one session.
	ClaudeThinkingReplayCacheMaxBytesPerSession = 8 << 20

	// ClaudeThinkingReplayCacheMaxTurnsPerSession bounds the number of assistant turns per session.
	ClaudeThinkingReplayCacheMaxTurnsPerSession = 64

	// ClaudeThinkingReplayCacheMaxBlocksPerTurn prevents pathological content arrays.
	ClaudeThinkingReplayCacheMaxBlocksPerTurn = 512

	// ClaudeThinkingReplayCacheMaxTotalBytes bounds aggregate in-process Claude replay content.
	ClaudeThinkingReplayCacheMaxTotalBytes = 256 << 20

	// ClaudeThinkingReplayCacheMaxAliases bounds the number of message-to-scope
	// aliases kept in the local fallback map.
	ClaudeThinkingReplayCacheMaxAliases = 102400

	// ClaudeThinkingReplayCacheMaxAliasesPerKey bounds how many distinct
	// conversation scopes a single message can map to in the local alias map.
	ClaudeThinkingReplayCacheMaxAliasesPerKey = 8

	// ClaudeThinkingReplayCacheMaxAliasesPerCredential bounds how many distinct
	// alias keys a single credential/model can create in Home KV. Keep small so
	// the per-credential index value does not exceed the underlying KV entry
	// size limit.
	ClaudeThinkingReplayCacheMaxAliasesPerCredential = 256

	claudeThinkingReplayCacheMaxSerializedBytes = ClaudeThinkingReplayCacheMaxBytesPerSession + 1024
)

type claudeThinkingReplayEntry struct {
	Contents   [][]byte
	Timestamp  time.Time
	Generation string
	Deleted    bool
}

// ClaudeThinkingReplaySnapshot identifies the exact replay generation read for one request.
type ClaudeThinkingReplaySnapshot = KimiThinkingReplaySnapshot

type claudeThinkingReplayHomeValue struct {
	Generation string            `json:"generation"`
	Deleted    bool              `json:"deleted,omitempty"`
	Contents   []json.RawMessage `json:"contents,omitempty"`
}

var (
	claudeThinkingReplayMu         sync.Mutex
	claudeThinkingReplayEntries    = make(map[string]claudeThinkingReplayEntry)
	claudeThinkingReplayTotalBytes int

	claudeThinkingReplayAliasMu sync.RWMutex
	// claudeThinkingReplayAliases maps a per-model message hash to a list of
	// conversation-scoped session keys that have contained that message. The list
	// lets two sessionless conversations share a visible message without
	// overwriting each other; Resolve scores candidates by how many request
	// messages resolve to the same session and breaks ties by recency.
	claudeThinkingReplayAliases    = make(map[string][]claudeThinkingReplayAliasEntry)
	claudeThinkingReplayAliasBytes int
)

type claudeThinkingReplayAliasEntry struct {
	sessionKey    string
	firstUserHash string
	timestamp     time.Time
}

// ClaudeThinkingReplayAliasMessage pairs a message hash with the weight it
// should receive during alias resolution. User messages typically receive a
// higher weight because they are a stronger conversation anchor than
// an echoed assistant turn.
type ClaudeThinkingReplayAliasMessage struct {
	Hash   string
	Weight int
}

var currentClaudeThinkingReplayKVClient = func() (kimiThinkingReplayKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

// CacheClaudeThinkingReplayBestEffort stores one complete signed assistant content array.
func CacheClaudeThinkingReplayBestEffort(ctx context.Context, modelFamily, sessionKey string, content []byte) bool {
	key := claudeThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" || !validClaudeThinkingReplayContent(content) {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	contents := [][]byte{append([]byte(nil), content...)}
	generation := uuid.NewString()
	if client, homeMode, errClient := currentClaudeThinkingReplayKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("home kv best-effort Claude thinking replay set failed: %v", errClient)
			return false
		}
		raw, errMarshal := marshalClaudeThinkingReplayHomeValue(generation, false, contents)
		if errMarshal != nil {
			log.Errorf("home kv best-effort Claude thinking replay set failed: %v", errMarshal)
			return false
		}
		written, errSet := client.KVSet(ctx, claudeThinkingReplayKVKey(modelFamily, sessionKey), raw, homekv.KVSetOptions{EX: ClaudeThinkingReplayCacheTTL})
		if errSet != nil {
			log.Errorf("home kv best-effort Claude thinking replay set failed: %v", errSet)
			return false
		}
		return written
	}

	storeClaudeThinkingReplayLocal(key, contents, generation, false, time.Now())
	return true
}

// GetClaudeThinkingReplayRequired retrieves all cached assistant turns for request-time replay.
func GetClaudeThinkingReplayRequired(ctx context.Context, modelFamily, sessionKey string) ([][]byte, bool, error) {
	contents, _, found, errGet := GetClaudeThinkingReplayWithSnapshotRequired(ctx, modelFamily, sessionKey)
	return contents, found, errGet
}

// GetClaudeThinkingReplayWithSnapshotRequired retrieves replay content and the exact cache state read.
func GetClaudeThinkingReplayWithSnapshotRequired(ctx context.Context, modelFamily, sessionKey string) ([][]byte, ClaudeThinkingReplaySnapshot, bool, error) {
	key := claudeThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" {
		return nil, ClaudeThinkingReplaySnapshot{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client, homeMode, errClient := currentClaudeThinkingReplayKVClient()
	if homeMode {
		if errClient != nil {
			return nil, ClaudeThinkingReplaySnapshot{loaded: true}, false, errClient
		}
		kvKey := claudeThinkingReplayKVKey(modelFamily, sessionKey)
		raw, errRead := readOrReserveClaudeThinkingReplayHomeValue(ctx, client, kvKey)
		if errRead != nil {
			return nil, ClaudeThinkingReplaySnapshot{loaded: true}, false, errRead
		}
		snapshot := ClaudeThinkingReplaySnapshot{raw: append([]byte(nil), raw...), loaded: true, found: true}
		contents, generation, deleted, okDecode := decodeClaudeThinkingReplayHomeValue(raw)
		if !okDecode {
			return nil, snapshot, false, fmt.Errorf("invalid Claude thinking replay content")
		}
		snapshot.generation = generation
		if _, errExpire := client.KVExpire(ctx, kvKey, ClaudeThinkingReplayCacheTTL); errExpire != nil {
			log.Warnf("home kv Claude thinking replay expire failed: %v", errExpire)
		}
		if deleted {
			return nil, snapshot, false, nil
		}
		return cloneClaudeThinkingReplayContents(contents), snapshot, len(contents) > 0, nil
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	claudeThinkingReplayMu.Lock()
	defer claudeThinkingReplayMu.Unlock()
	entry, ok := claudeThinkingReplayEntries[key]
	if !ok || now.Sub(entry.Timestamp) > ClaudeThinkingReplayCacheTTL {
		if ok {
			claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
			delete(claudeThinkingReplayEntries, key)
		}
		entry = reserveClaudeThinkingReplayLocalLocked(key, now)
	}
	entry.Timestamp = now
	claudeThinkingReplayEntries[key] = entry
	snapshot := ClaudeThinkingReplaySnapshot{generation: entry.Generation, loaded: true, found: true}
	if entry.Deleted {
		return nil, snapshot, false, nil
	}
	return cloneClaudeThinkingReplayContents(entry.Contents), snapshot, len(entry.Contents) > 0, nil
}

// ReplaceClaudeThinkingReplayIfUnchanged appends a completed assistant turn only if the request snapshot is current.
func ReplaceClaudeThinkingReplayIfUnchanged(ctx context.Context, modelFamily, sessionKey string, snapshot ClaudeThinkingReplaySnapshot, content []byte) (bool, error) {
	key := claudeThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" || !validClaudeThinkingReplayContent(content) {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !snapshot.loaded {
		return CacheClaudeThinkingReplayBestEffort(ctx, modelFamily, sessionKey, content), nil
	}
	client, homeMode, errClient := currentClaudeThinkingReplayKVClient()
	if homeMode {
		if errClient != nil {
			return false, errClient
		}
		contents, _, deleted, okDecode := decodeClaudeThinkingReplayHomeValue(snapshot.raw)
		if !okDecode {
			return false, fmt.Errorf("invalid Claude thinking replay snapshot")
		}
		if deleted {
			contents = nil
		}
		contents = appendClaudeThinkingReplayContent(contents, content)
		generation := uuid.NewString()
		raw, errMarshal := marshalClaudeThinkingReplayHomeValue(generation, false, contents)
		if errMarshal != nil {
			return false, errMarshal
		}
		return client.KVCompareAndSwap(ctx, claudeThinkingReplayKVKey(modelFamily, sessionKey), snapshot.raw, snapshot.found, raw, ClaudeThinkingReplayCacheTTL)
	}

	claudeThinkingReplayMu.Lock()
	defer claudeThinkingReplayMu.Unlock()
	entry, found := claudeThinkingReplayEntries[key]
	if found != snapshot.found || (found && entry.Generation != snapshot.generation) {
		return false, nil
	}
	contents := appendClaudeThinkingReplayContent(entry.Contents, content)
	claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
	claudeThinkingReplayTotalBytes += claudeThinkingReplayEntryBytes(contents)
	claudeThinkingReplayEntries[key] = claudeThinkingReplayEntry{
		Contents:   contents,
		Timestamp:  time.Now(),
		Generation: uuid.NewString(),
	}
	enforceClaudeThinkingReplayLimitsLocked()
	return true, nil
}

// DeleteClaudeThinkingReplayIfUnchanged clears replay state only if the request snapshot is current.
func DeleteClaudeThinkingReplayIfUnchanged(ctx context.Context, modelFamily, sessionKey string, snapshot ClaudeThinkingReplaySnapshot) (bool, error) {
	key := claudeThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !snapshot.loaded {
		return true, DeleteClaudeThinkingReplayRequired(ctx, modelFamily, sessionKey)
	}
	generation := uuid.NewString()
	client, homeMode, errClient := currentClaudeThinkingReplayKVClient()
	if homeMode {
		if errClient != nil {
			return false, errClient
		}
		tombstone, errMarshal := marshalClaudeThinkingReplayHomeValue(generation, true, nil)
		if errMarshal != nil {
			return false, errMarshal
		}
		return client.KVCompareAndSwap(ctx, claudeThinkingReplayKVKey(modelFamily, sessionKey), snapshot.raw, snapshot.found, tombstone, ClaudeThinkingReplayCacheTTL)
	}

	claudeThinkingReplayMu.Lock()
	defer claudeThinkingReplayMu.Unlock()
	entry, found := claudeThinkingReplayEntries[key]
	if found != snapshot.found || (found && entry.Generation != snapshot.generation) {
		return false, nil
	}
	claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
	claudeThinkingReplayEntries[key] = claudeThinkingReplayEntry{Timestamp: time.Now(), Generation: generation, Deleted: true}
	return true, nil
}

// DeleteClaudeThinkingReplayRequired removes stale replay state unconditionally.
func DeleteClaudeThinkingReplayRequired(ctx context.Context, modelFamily, sessionKey string) error {
	key := claudeThinkingReplayCacheKey(modelFamily, sessionKey)
	if key == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client, homeMode, errClient := currentClaudeThinkingReplayKVClient()
	if homeMode {
		if errClient != nil {
			return errClient
		}
		_, errDelete := client.KVDel(ctx, claudeThinkingReplayKVKey(modelFamily, sessionKey))
		return errDelete
	}
	claudeThinkingReplayMu.Lock()
	if entry, found := claudeThinkingReplayEntries[key]; found {
		claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
		delete(claudeThinkingReplayEntries, key)
	}
	claudeThinkingReplayMu.Unlock()
	return nil
}

// ClearClaudeThinkingReplayCache clears only Claude replay state and its
// message-to-scope aliases.
func ClearClaudeThinkingReplayCache() {
	claudeThinkingReplayMu.Lock()
	claudeThinkingReplayEntries = make(map[string]claudeThinkingReplayEntry)
	claudeThinkingReplayTotalBytes = 0
	claudeThinkingReplayMu.Unlock()

	claudeThinkingReplayAliasMu.Lock()
	claudeThinkingReplayAliases = make(map[string][]claudeThinkingReplayAliasEntry)
	claudeThinkingReplayAliasBytes = 0
	claudeThinkingReplayAliasMu.Unlock()
}

// RegisterClaudeThinkingReplayAlias records that a request message belongs to a
// conversation scope. Two different conversations can share the same visible
// message, so the alias is a list of (session, timestamp) pairs rather than a
// single mapping. In Home KV mode the alias is stored as a separate KV entry
// with a per-credential index that enforces a cap and evicts oldest aliases.
func RegisterClaudeThinkingReplayAlias(ctx context.Context, modelFamily, sessionKey, messageHash, firstUserHash string) {
	if modelFamily == "" || sessionKey == "" || messageHash == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client, homeMode, errClient := currentClaudeThinkingReplayKVClient()
	if homeMode {
		if errClient != nil {
			return
		}
		registerClaudeThinkingReplayAliasHome(ctx, client, modelFamily, sessionKey, messageHash, firstUserHash)
		return
	}

	key := claudeThinkingReplayAliasKey(modelFamily, messageHash)
	claudeThinkingReplayAliasMu.Lock()
	defer claudeThinkingReplayAliasMu.Unlock()
	now := time.Now()
	purgeExpiredClaudeThinkingReplayAliasesLocked(now)
	claudeThinkingReplayUpsertAliasLocked(key, sessionKey, firstUserHash, now)
	enforceClaudeThinkingReplayAliasLimitsLocked()
}

// ResolveClaudeThinkingReplaySessionKey looks for an existing conversation scope
// that the request messages belong to. It scores each candidate session by the
// weighted count of request messages that point to it and breaks ties by
// recency, so shared messages do not resolve the wrong conversation when
// multiple messages remain after compaction.
func ResolveClaudeThinkingReplaySessionKey(ctx context.Context, modelFamily string, messages []ClaudeThinkingReplayAliasMessage, requestFirstUserHash string) (string, bool) {
	if modelFamily == "" || len(messages) == 0 {
		return "", false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client, homeMode, errClient := currentClaudeThinkingReplayKVClient()
	if homeMode {
		if errClient != nil {
			return "", false
		}
		return resolveClaudeThinkingReplayAliasHome(ctx, client, modelFamily, messages, requestFirstUserHash)
	}

	claudeThinkingReplayAliasMu.RLock()
	defer claudeThinkingReplayAliasMu.RUnlock()
	return claudeThinkingReplayResolveBestAliasLocked(modelFamily, messages, requestFirstUserHash, time.Now())
}

func claudeThinkingReplayAliasKey(modelFamily, messageHash string) string {
	return strings.Join([]string{modelFamily, messageHash}, "\x00")
}

func claudeThinkingReplayAliasKVKey(modelFamily, messageHash string) string {
	return "cpa:claude:thinking-replay-alias:" + homekv.HashKeyPart(strings.TrimSpace(modelFamily)) + ":" + homekv.HashKeyPart(strings.TrimSpace(messageHash))
}

// claudeThinkingReplayCredentialHash extracts the stable credential hash embedded
// in a modelFamily string ("claude:<hash>:<baseModel>"). The alias index is keyed
// by credential so the per-credential cap applies across all model names a caller
// may use.
func claudeThinkingReplayCredentialHash(modelFamily string) string {
	const prefix = "claude:"
	if !strings.HasPrefix(modelFamily, prefix) {
		return modelFamily
	}
	rest := modelFamily[len(prefix):]
	if i := strings.IndexByte(rest, ':'); i > 0 {
		return rest[:i]
	}
	return modelFamily
}

func claudeThinkingReplayAliasIndexKVKey(modelFamily string) string {
	credentialHash := claudeThinkingReplayCredentialHash(modelFamily)
	return "cpa:claude:thinking-replay-alias-index:" + homekv.HashKeyPart(strings.TrimSpace(credentialHash))
}

func claudeThinkingReplayUpsertAliasLocked(key, sessionKey, firstUserHash string, now time.Time) {
	list := claudeThinkingReplayAliases[key]
	for i := range list {
		if list[i].sessionKey == sessionKey {
			claudeThinkingReplayAliasBytes -= len(list[i].sessionKey) + len(list[i].firstUserHash)
			list[i].timestamp = now
			list[i].firstUserHash = firstUserHash
			claudeThinkingReplayAliasBytes += len(sessionKey) + len(firstUserHash)
			claudeThinkingReplayAliases[key] = list
			return
		}
	}
	list = append(list, claudeThinkingReplayAliasEntry{sessionKey: sessionKey, firstUserHash: firstUserHash, timestamp: now})
	if len(list) > ClaudeThinkingReplayCacheMaxAliasesPerKey {
		claudeThinkingReplayAliasBytes -= len(list[0].sessionKey) + len(list[0].firstUserHash)
		list = list[1:]
	}
	claudeThinkingReplayAliases[key] = list
	claudeThinkingReplayAliasBytes += len(key) + len(sessionKey) + len(firstUserHash)
}

func claudeThinkingReplayResolveBestAliasLocked(modelFamily string, messages []ClaudeThinkingReplayAliasMessage, requestFirstUserHash string, now time.Time) (string, bool) {
	const firstUserMatchBonus = 2
	scores := make(map[string]int)
	for _, m := range messages {
		key := claudeThinkingReplayAliasKey(modelFamily, m.Hash)
		list, ok := claudeThinkingReplayAliases[key]
		if !ok {
			continue
		}
		for _, entry := range list {
			if now.Sub(entry.timestamp) > ClaudeThinkingReplayCacheTTL {
				continue
			}
			scores[entry.sessionKey] += m.Weight
			if requestFirstUserHash != "" && entry.firstUserHash == requestFirstUserHash {
				scores[entry.sessionKey] += firstUserMatchBonus
			}
		}
	}
	return claudeThinkingReplayResolveBestAlias(scores)
}

// claudeThinkingReplayResolveBestAlias returns the session with the highest
// score. If multiple sessions tie for the highest score the result is
// ambiguous, so it returns no match rather than risk restoring the wrong
// conversation.
func claudeThinkingReplayResolveBestAlias(scores map[string]int) (string, bool) {
	if len(scores) == 0 {
		return "", false
	}
	maxScore := 0
	for _, s := range scores {
		if s > maxScore {
			maxScore = s
		}
	}
	if maxScore <= 0 {
		return "", false
	}
	best := ""
	tied := 0
	for session, s := range scores {
		if s == maxScore {
			best = session
			tied++
		}
	}
	if tied > 1 {
		return "", false
	}
	return best, true
}

func purgeExpiredClaudeThinkingReplayAliasesLocked(now time.Time) {
	for key, list := range claudeThinkingReplayAliases {
		kept := list[:0]
		for _, entry := range list {
			if now.Sub(entry.timestamp) <= ClaudeThinkingReplayCacheTTL {
				kept = append(kept, entry)
			} else {
				claudeThinkingReplayAliasBytes -= len(key) + len(entry.sessionKey) + len(entry.firstUserHash)
			}
		}
		if len(kept) == 0 {
			delete(claudeThinkingReplayAliases, key)
		} else {
			claudeThinkingReplayAliases[key] = kept
		}
	}
}

func enforceClaudeThinkingReplayAliasLimitsLocked() {
	total := 0
	for _, list := range claudeThinkingReplayAliases {
		total += len(list)
	}
	for total > ClaudeThinkingReplayCacheMaxAliases {
		type candidate struct {
			key       string
			index     int
			timestamp time.Time
		}
		var candidates []candidate
		for key, list := range claudeThinkingReplayAliases {
			for i, entry := range list {
				candidates = append(candidates, candidate{key: key, index: i, timestamp: entry.timestamp})
			}
		}
		if len(candidates) == 0 {
			break
		}
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].timestamp.Before(candidates[j].timestamp)
		})
		batch := ClaudeThinkingReplayCacheEvictBatchSize
		if batch > len(candidates) {
			batch = len(candidates)
		}
		if batch > total-ClaudeThinkingReplayCacheMaxAliases {
			batch = total - ClaudeThinkingReplayCacheMaxAliases
		}
		for i := 0; i < batch; i++ {
			c := candidates[i]
			list := claudeThinkingReplayAliases[c.key]
			if c.index < len(list) {
				claudeThinkingReplayAliasBytes -= len(c.key) + len(list[c.index].sessionKey) + len(list[c.index].firstUserHash)
				list = append(list[:c.index], list[c.index+1:]...)
				if len(list) == 0 {
					delete(claudeThinkingReplayAliases, c.key)
				} else {
					claudeThinkingReplayAliases[c.key] = list
				}
				total--
			}
		}
	}
}

// claudeThinkingReplayAliasIndexRecord and claudeThinkingReplayAliasIndex are
// used to cap Home KV aliases per credential. The index lists all alias keys
// created by that credential so the oldest can be evicted.
type claudeThinkingReplayAliasIndexRecord struct {
	AliasKey  string    `json:"alias_key"`
	Timestamp time.Time `json:"timestamp"`
}

type claudeThinkingReplayAliasIndex struct {
	Aliases []claudeThinkingReplayAliasIndexRecord `json:"aliases"`
}

type claudeThinkingReplayAliasHomeValue struct {
	Sessions []claudeThinkingReplayAliasHomeSession `json:"sessions"`
}

type claudeThinkingReplayAliasHomeSession struct {
	SessionKey    string    `json:"session_key"`
	FirstUserHash string    `json:"first_user_hash"`
	Timestamp     time.Time `json:"timestamp"`
}

func registerClaudeThinkingReplayAliasHome(ctx context.Context, client kimiThinkingReplayKVClient, modelFamily, sessionKey, messageHash, firstUserHash string) {
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHash)
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)
	now := time.Now()

	// Update the shared alias value atomically with compare-and-swap retries.
	// Multiple sessionless conversations can register the same message hash
	// concurrently, so an unconditional KVSet would let the last writer discard
	// the others.
	for attempt := 0; attempt < 4; attempt++ {
		var value claudeThinkingReplayAliasHomeValue
		raw, found, errGet := client.KVGet(ctx, aliasKey)
		if errGet != nil {
			log.Warnf("claude thinking replay alias read failed: %v", errGet)
			return
		}
		if found {
			if err := json.Unmarshal(raw, &value); err != nil {
				log.Warnf("claude thinking replay alias unmarshal failed: %v", err)
				value = claudeThinkingReplayAliasHomeValue{}
			}
		}
		value.Sessions = claudeThinkingReplayAliasHomeValueUpsert(value.Sessions, sessionKey, firstUserHash, now)
		if len(value.Sessions) > ClaudeThinkingReplayCacheMaxAliasesPerKey {
			sort.Slice(value.Sessions, func(i, j int) bool {
				return value.Sessions[i].Timestamp.Before(value.Sessions[j].Timestamp)
			})
			value.Sessions = value.Sessions[len(value.Sessions)-ClaudeThinkingReplayCacheMaxAliasesPerKey:]
		}
		newRaw, errMarshal := json.Marshal(value)
		if errMarshal != nil {
			log.Warnf("claude thinking replay alias marshal failed: %v", errMarshal)
			return
		}
		swapped, errSwap := client.KVCompareAndSwap(ctx, aliasKey, raw, found, newRaw, ClaudeThinkingReplayCacheTTL)
		if errSwap != nil {
			log.Warnf("claude thinking replay alias cas failed: %v", errSwap)
			return
		}
		if swapped {
			break
		}
		if attempt == 3 {
			log.Warnf("claude thinking replay alias cas exhausted after %d attempts", attempt+1)
			return
		}
	}

	// Maintain the per-credential index so old aliases can be evicted.
	for attempt := 0; attempt < 4; attempt++ {
		indexRaw, indexFound, errIndex := client.KVGet(ctx, indexKey)
		if errIndex != nil {
			log.Warnf("claude thinking replay alias index read failed: %v", errIndex)
			return
		}
		index, ok := decodeClaudeThinkingReplayAliasIndex(indexRaw)
		if !ok {
			index = claudeThinkingReplayAliasIndex{}
		}
		index.Aliases = purgeExpiredClaudeThinkingReplayAliasIndex(index.Aliases, now)
		index.Aliases = claudeThinkingReplayAliasIndexUpsert(index.Aliases, aliasKey, now)
		var evicted []string
		if len(index.Aliases) > ClaudeThinkingReplayCacheMaxAliasesPerCredential {
			sort.Slice(index.Aliases, func(i, j int) bool {
				return index.Aliases[i].Timestamp.Before(index.Aliases[j].Timestamp)
			})
			for len(index.Aliases) > ClaudeThinkingReplayCacheMaxAliasesPerCredential {
				evicted = append(evicted, index.Aliases[0].AliasKey)
				index.Aliases = index.Aliases[1:]
			}
		}
		indexBytes, errMarshal := json.Marshal(index)
		if errMarshal != nil {
			log.Warnf("claude thinking replay alias index marshal failed: %v", errMarshal)
			return
		}
		swapped, errSwap := client.KVCompareAndSwap(ctx, indexKey, indexRaw, indexFound, indexBytes, ClaudeThinkingReplayCacheTTL)
		if errSwap != nil {
			log.Warnf("claude thinking replay alias index cas failed: %v", errSwap)
			return
		}
		if swapped {
			// Only delete evicted alias values after the index CAS succeeds.
			// A concurrent worker may have refreshed an alias between our read
			// and the CAS; deleting before the CAS could erase a still-indexed
			// alias and break compacted continuations.
			for _, key := range evicted {
				if _, errDel := client.KVDel(ctx, key); errDel != nil {
					log.Warnf("claude thinking replay alias eviction failed: %v", errDel)
				}
			}
			break
		}
		if attempt == 3 {
			log.Warnf("claude thinking replay alias index cas exhausted after %d attempts", attempt+1)
			return
		}
	}
}

func resolveClaudeThinkingReplayAliasHome(ctx context.Context, client kimiThinkingReplayKVClient, modelFamily string, messages []ClaudeThinkingReplayAliasMessage, requestFirstUserHash string) (string, bool) {
	const firstUserMatchBonus = 2
	scores := make(map[string]int)
	now := time.Now()
	for _, m := range messages {
		raw, found, err := client.KVGet(ctx, claudeThinkingReplayAliasKVKey(modelFamily, m.Hash))
		if err != nil || !found {
			continue
		}
		var value claudeThinkingReplayAliasHomeValue
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		for _, s := range value.Sessions {
			if now.Sub(s.Timestamp) > ClaudeThinkingReplayCacheTTL {
				continue
			}
			scores[s.SessionKey] += m.Weight
			if requestFirstUserHash != "" && s.FirstUserHash == requestFirstUserHash {
				scores[s.SessionKey] += firstUserMatchBonus
			}
		}
	}
	return claudeThinkingReplayResolveBestAlias(scores)
}

func decodeClaudeThinkingReplayAliasIndex(raw []byte) (claudeThinkingReplayAliasIndex, bool) {
	if len(raw) == 0 {
		return claudeThinkingReplayAliasIndex{}, true
	}
	var index claudeThinkingReplayAliasIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		return claudeThinkingReplayAliasIndex{}, false
	}
	return index, true
}

func purgeExpiredClaudeThinkingReplayAliasIndex(records []claudeThinkingReplayAliasIndexRecord, now time.Time) []claudeThinkingReplayAliasIndexRecord {
	kept := records[:0]
	for _, r := range records {
		if now.Sub(r.Timestamp) <= ClaudeThinkingReplayCacheTTL {
			kept = append(kept, r)
		}
	}
	return kept
}

func claudeThinkingReplayAliasIndexUpsert(records []claudeThinkingReplayAliasIndexRecord, aliasKey string, now time.Time) []claudeThinkingReplayAliasIndexRecord {
	for i := range records {
		if records[i].AliasKey == aliasKey {
			records[i].Timestamp = now
			return records
		}
	}
	return append(records, claudeThinkingReplayAliasIndexRecord{AliasKey: aliasKey, Timestamp: now})
}

func claudeThinkingReplayAliasHomeValueUpsert(sessions []claudeThinkingReplayAliasHomeSession, sessionKey, firstUserHash string, now time.Time) []claudeThinkingReplayAliasHomeSession {
	for i := range sessions {
		if sessions[i].SessionKey == sessionKey {
			sessions[i].Timestamp = now
			sessions[i].FirstUserHash = firstUserHash
			return sessions
		}
	}
	return append(sessions, claudeThinkingReplayAliasHomeSession{SessionKey: sessionKey, FirstUserHash: firstUserHash, Timestamp: now})
}

func readOrReserveClaudeThinkingReplayHomeValue(ctx context.Context, client kimiThinkingReplayKVClient, key string) ([]byte, error) {
	for attempt := 0; attempt < 4; attempt++ {
		raw, found, errGet := client.KVGet(ctx, key)
		if errGet != nil {
			return nil, errGet
		}
		if found {
			if len(raw) > claudeThinkingReplayCacheMaxSerializedBytes {
				return nil, fmt.Errorf("Claude thinking replay value exceeds size limit")
			}
			return raw, nil
		}
		tombstone, errMarshal := marshalClaudeThinkingReplayHomeValue(uuid.NewString(), true, nil)
		if errMarshal != nil {
			return nil, errMarshal
		}
		swapped, errReserve := client.KVCompareAndSwap(ctx, key, nil, false, tombstone, ClaudeThinkingReplayCacheTTL)
		if errReserve != nil {
			return nil, errReserve
		}
		if swapped {
			return tombstone, nil
		}
	}
	return nil, fmt.Errorf("could not reserve absent Claude thinking replay state")
}

func marshalClaudeThinkingReplayHomeValue(generation string, deleted bool, contents [][]byte) ([]byte, error) {
	value := claudeThinkingReplayHomeValue{Generation: generation, Deleted: deleted}
	if !deleted {
		value.Contents = make([]json.RawMessage, 0, len(contents))
		for _, content := range contents {
			value.Contents = append(value.Contents, json.RawMessage(append([]byte(nil), content...)))
		}
	}
	return json.Marshal(value)
}

func decodeClaudeThinkingReplayHomeValue(raw []byte) ([][]byte, string, bool, bool) {
	if len(raw) == 0 || len(raw) > claudeThinkingReplayCacheMaxSerializedBytes || !gjson.ValidBytes(raw) {
		return nil, "", false, false
	}
	var value claudeThinkingReplayHomeValue
	if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal != nil || strings.TrimSpace(value.Generation) == "" {
		return nil, "", false, false
	}
	if value.Deleted {
		return nil, value.Generation, true, true
	}
	contents := make([][]byte, 0, len(value.Contents))
	for _, content := range value.Contents {
		if !validClaudeThinkingReplayContent(content) {
			return nil, "", false, false
		}
		contents = append(contents, append([]byte(nil), content...))
	}
	if len(contents) == 0 {
		return nil, "", false, false
	}
	return contents, value.Generation, false, true
}

func reserveClaudeThinkingReplayLocalLocked(key string, now time.Time) claudeThinkingReplayEntry {
	entry := claudeThinkingReplayEntry{Timestamp: now, Generation: uuid.NewString(), Deleted: true}
	claudeThinkingReplayEntries[key] = entry
	enforceClaudeThinkingReplayLimitsLocked()
	return entry
}

func storeClaudeThinkingReplayLocal(key string, contents [][]byte, generation string, deleted bool, now time.Time) {
	cacheCleanupOnce.Do(startCacheCleanup)
	claudeThinkingReplayMu.Lock()
	defer claudeThinkingReplayMu.Unlock()
	if previous, found := claudeThinkingReplayEntries[key]; found {
		claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(previous.Contents)
	}
	cloned := cloneClaudeThinkingReplayContents(contents)
	claudeThinkingReplayTotalBytes += claudeThinkingReplayEntryBytes(cloned)
	claudeThinkingReplayEntries[key] = claudeThinkingReplayEntry{Contents: cloned, Timestamp: now, Generation: generation, Deleted: deleted}
	enforceClaudeThinkingReplayLimitsLocked()
}

func appendClaudeThinkingReplayContent(contents [][]byte, content []byte) [][]byte {
	cloned := cloneClaudeThinkingReplayContents(contents)
	for _, existing := range cloned {
		if claudeThinkingReplayJSONEqual(existing, content) {
			return cloned
		}
	}
	cloned = append(cloned, append([]byte(nil), content...))
	for len(cloned) > ClaudeThinkingReplayCacheMaxTurnsPerSession || claudeThinkingReplayEntryBytes(cloned) > ClaudeThinkingReplayCacheMaxBytesPerSession {
		if len(cloned) == 0 {
			break
		}
		cloned = cloned[1:]
	}
	return cloned
}

func cloneClaudeThinkingReplayContents(contents [][]byte) [][]byte {
	cloned := make([][]byte, 0, len(contents))
	for _, content := range contents {
		cloned = append(cloned, append([]byte(nil), content...))
	}
	return cloned
}

func claudeThinkingReplayEntryBytes(contents [][]byte) int {
	total := 0
	for _, content := range contents {
		total += len(content)
	}
	return total
}

func claudeThinkingReplayCacheKey(modelFamily, sessionKey string) string {
	modelFamily = strings.TrimSpace(modelFamily)
	sessionKey = strings.TrimSpace(sessionKey)
	if modelFamily == "" || sessionKey == "" {
		return ""
	}
	return strings.Join([]string{"claude-thinking-replay", modelFamily, sessionKey}, "\x00")
}

func claudeThinkingReplayKVKey(modelFamily, sessionKey string) string {
	return "cpa:claude:thinking-replay:" + homekv.HashKeyPart(strings.TrimSpace(modelFamily)) + ":" + homekv.HashKeyPart(strings.TrimSpace(sessionKey))
}

func validClaudeThinkingReplayContent(content []byte) bool {
	if len(content) == 0 || len(content) > ClaudeThinkingReplayCacheMaxBytesPerSession || !gjson.ValidBytes(content) {
		return false
	}
	root := gjson.ParseBytes(content)
	return root.IsArray() && len(root.Array()) > 0 && len(root.Array()) <= ClaudeThinkingReplayCacheMaxBlocksPerTurn
}

func claudeThinkingReplayJSONEqual(left, right []byte) bool {
	leftCanonical, leftOK := claudeThinkingReplayCanonicalJSON(left)
	rightCanonical, rightOK := claudeThinkingReplayCanonicalJSON(right)
	return leftOK && rightOK && bytes.Equal(leftCanonical, rightCanonical)
}

func claudeThinkingReplayCanonicalJSON(raw []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, false
	}
	canonical, errMarshal := json.Marshal(value)
	return canonical, errMarshal == nil
}

func enforceClaudeThinkingReplayLimitsLocked() {
	for len(claudeThinkingReplayEntries) > ClaudeThinkingReplayCacheMaxEntries || claudeThinkingReplayTotalBytes > ClaudeThinkingReplayCacheMaxTotalBytes {
		if len(claudeThinkingReplayEntries) == 0 {
			claudeThinkingReplayTotalBytes = 0
			return
		}
		evictOldestClaudeThinkingReplayEntriesLocked(ClaudeThinkingReplayCacheEvictBatchSize)
	}
}

func evictOldestClaudeThinkingReplayEntriesLocked(count int) {
	if count <= 0 || len(claudeThinkingReplayEntries) == 0 {
		return
	}
	type candidate struct {
		key       string
		timestamp time.Time
	}
	candidates := make([]candidate, 0, len(claudeThinkingReplayEntries))
	for key, entry := range claudeThinkingReplayEntries {
		candidates = append(candidates, candidate{key: key, timestamp: entry.Timestamp})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].timestamp.Before(candidates[j].timestamp)
	})
	if count > len(candidates) {
		count = len(candidates)
	}
	for i := 0; i < count; i++ {
		entry := claudeThinkingReplayEntries[candidates[i].key]
		claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
		delete(claudeThinkingReplayEntries, candidates[i].key)
	}
}

func purgeExpiredClaudeThinkingReplayCache(now time.Time) {
	claudeThinkingReplayMu.Lock()
	for key, entry := range claudeThinkingReplayEntries {
		if now.Sub(entry.Timestamp) > ClaudeThinkingReplayCacheTTL {
			claudeThinkingReplayTotalBytes -= claudeThinkingReplayEntryBytes(entry.Contents)
			delete(claudeThinkingReplayEntries, key)
		}
	}
	claudeThinkingReplayMu.Unlock()

	claudeThinkingReplayAliasMu.Lock()
	purgeExpiredClaudeThinkingReplayAliasesLocked(now)
	claudeThinkingReplayAliasMu.Unlock()
}
