package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// ClaudeThreadStateTTL bounds recoverable Thread routing and wire-profile state.
const ClaudeThreadStateTTL = 24 * time.Hour

const claudeThreadAuthLimit = 1024

type claudeThreadAuthEntry struct {
	authID    string
	expiresAt time.Time
}

var claudeThreadAuthCache = struct {
	sync.Mutex
	entries map[string]claudeThreadAuthEntry
	order   []string
}{entries: make(map[string]claudeThreadAuthEntry)}

// ClaudeThreadCallerScope uses the same authenticated caller namespace at the
// execution and routing boundaries, without depending on a conversation ID.
func ClaudeThreadCallerScope(apiKey string, metadataSets ...map[string]any) string {
	for _, metadata := range metadataSets {
		if scope := metadataString(metadata, cliproxyexecutor.CallerScopeMetadataKey); scope != "" {
			return scope
		}
	}
	if scope := CallerScope(apiKey); scope != "" {
		return scope
	}
	return "cpa-claude-thread-default-caller"
}

func claudeThreadAuthKey(callerScope, messageID string) string {
	return "claude:thread-auth:v1:" + kvstore.HashKeyPart(callerScope) + ":" + kvstore.HashKeyPart(messageID)
}

func cacheClaudeThreadAuth(key, authID string) {
	now := time.Now()
	claudeThreadAuthCache.Lock()
	defer claudeThreadAuthCache.Unlock()
	entry, exists := claudeThreadAuthCache.entries[key]
	if exists && now.Before(entry.expiresAt) {
		return
	}
	if !exists {
		claudeThreadAuthCache.order = append(claudeThreadAuthCache.order, key)
	}
	claudeThreadAuthCache.entries[key] = claudeThreadAuthEntry{authID: strings.Clone(authID), expiresAt: now.Add(ClaudeThreadStateTTL)}
	if len(claudeThreadAuthCache.order) > claudeThreadAuthLimit {
		delete(claudeThreadAuthCache.entries, claudeThreadAuthCache.order[0])
		claudeThreadAuthCache.order = claudeThreadAuthCache.order[1:]
	}
}

// RememberClaudeThreadAuth publishes ownership only after upstream success.
// Existing generic KV persistence keeps the binding available after a restart.
func RememberClaudeThreadAuth(callerScope, messageID, authID string) {
	if callerScope == "" || messageID == "" || authID == "" {
		return
	}
	key := claudeThreadAuthKey(callerScope, messageID)
	cacheClaudeThreadAuth(key, authID)
	kvstore.KVSetNXBestEffort(context.Background(), key, []byte(authID), ClaudeThreadStateTTL)
}

// ClaudeThreadAuth returns a caller-isolated routing hint, not an authorization
// grant. The normal selector still validates credential availability and models.
func ClaudeThreadAuth(ctx context.Context, callerScope, messageID string) (string, bool, error) {
	if callerScope == "" || messageID == "" {
		return "", false, nil
	}
	key := claudeThreadAuthKey(callerScope, messageID)
	claudeThreadAuthCache.Lock()
	entry, found := claudeThreadAuthCache.entries[key]
	claudeThreadAuthCache.Unlock()
	if found && time.Now().Before(entry.expiresAt) {
		return entry.authID, true, nil
	}
	client, kvMode, errClient := kvstore.CurrentKVClient()
	if !kvMode || errClient != nil || client == nil {
		return "", false, errClient
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, foundKV, errGet := client.KVGet(ctx, key)
	if errGet != nil || !foundKV {
		return "", false, errGet
	}
	authID := strings.TrimSpace(string(raw))
	if authID == "" {
		return "", false, errors.New("empty Claude Thread credential binding")
	}
	cacheClaudeThreadAuth(key, authID)
	return authID, true, nil
}
