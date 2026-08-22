package executor

import (
	"context"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// claudeThinkingReplayScope reuses the bounded replay state shape shared with Kimi.
type claudeThinkingReplayScope = kimiThinkingReplayScope

func claudeThinkingReplayEnabled(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	if auth == nil || !sourceFormatEqual(opts.SourceFormat, sdktranslator.FormatClaude) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") || auth.AuthKind() != cliproxyauth.AuthKindAPIKey {
		return false
	}
	if !helps.APIKeyModelIsCompat(req) {
		return false
	}
	apiKey, _ := claudeCreds(auth)
	return strings.TrimSpace(apiKey) != "" && !isClaudeOAuthToken(apiKey)
}

// claudeThinkingReplayScopeFromRequest selects a conversation replay scope.
// It prefers an explicit execution/session metadata or prompt-cache/window key,
// then a conversation nonce (client_metadata.conversation_id, conversation_id,
// or X-Conversation-Id), and finally a content-derived fallback keyed by the
// first user message and system prompt.
//
// When the fallback key is content-derived, history compaction changes
// messages.0 and can orphan the cache. Resolve the original scope through any
// remaining message aliases, then continue using that key for this request.
func claudeThinkingReplayScopeFromRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) claudeThinkingReplayScope {
	modelFamily := helps.ClaudeThinkingReplayModelFamily(auth, req.Model)
	callerHash := helps.ClaudeThinkingReplayCallerHash(auth, req, opts)
	firstUserHash := helps.ClaudeThinkingReplayFirstUserHash(modelFamily, callerHash, req.Payload)
	sessionKey := codexReasoningReplaySessionKey(ctx, sdktranslator.FormatClaude, req, opts, req.Payload)
	fallback := false
	if sessionKey != "" {
		sessionKey = xaiReasoningReplayIsolateSessionKey(ctx, sessionKey)
	}
	if sessionKey == "" {
		var usedNonce bool
		sessionKey, usedNonce = helps.ClaudeThinkingReplayConversationSessionKey(auth, req, opts)
		fallback = sessionKey != "" && !usedNonce
		if fallback {
			resolvedMessages := capClaudeThinkingReplayAliasMessages(helps.ClaudeThinkingReplayMessageHashes(modelFamily, callerHash, req.Payload))
			if resolved, ok := internalcache.ResolveClaudeThinkingReplaySessionKey(ctx, modelFamily, resolvedMessages, firstUserHash); ok {
				sessionKey = resolved
			}
		}
	}
	return claudeThinkingReplayScope{
		modelFamily:   modelFamily,
		sessionKey:    sessionKey,
		fallbackKey:   fallback,
		callerHash:    callerHash,
		firstUserHash: firstUserHash,
	}
}

// claudeThinkingReplayMaxAliasesPerRequest caps how many message hashes are
// registered as scope aliases for a single request. This prevents long
// histories from generating unbounded alias registration round trips and
// evicting useful earlier aliases.
const claudeThinkingReplayMaxAliasesPerRequest = 64

func capClaudeThinkingReplayAliasMessages(hashes []internalcache.ClaudeThinkingReplayAliasMessage) []internalcache.ClaudeThinkingReplayAliasMessage {
	if len(hashes) <= claudeThinkingReplayMaxAliasesPerRequest {
		return hashes
	}
	keep := make([]internalcache.ClaudeThinkingReplayAliasMessage, 0, claudeThinkingReplayMaxAliasesPerRequest)
	keep = append(keep, hashes[0])
	keep = append(keep, hashes[len(hashes)-claudeThinkingReplayMaxAliasesPerRequest+1:]...)
	return keep
}

// prepareClaudeThinkingReplayRequest loads cached assistant content for this
// request and strips any client-supplied _cliproxy_replay_provenance markers
// from req.Payload. The actual restore is applied to bodyForUpstream after
// signature sanitization and before MCP tool-name remapping, so cache-provenanced
// signatures bypass the sanitizer while matching against the caller-facing body.
func prepareClaudeThinkingReplayRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (claudeThinkingReplayScope, [][]byte, bool) {
	scope := claudeThinkingReplayScopeFromRequest(ctx, auth, req, opts)
	if !scope.valid() {
		return scope, nil, false
	}

	req.Payload = helps.StripClaudeThinkingReplayProvenanceMarkers(req.Payload)

	// Both content-derived fallback scopes and caller-controlled nonce scopes can
	// supply arbitrary openings per request; avoid reserving a Home KV tombstone
	// until a replayable response is actually cached.
	contents, snapshot, found, errGet := internalcache.GetClaudeThinkingReplayWithSnapshotIfExists(ctx, scope.modelFamily, scope.sessionKey)
	scope.snapshot = snapshot
	scope.cacheReady = errGet == nil
	if errGet != nil {
		log.Warnf("claude compatible thinking replay cache read failed: %v", errGet)
		return scope, nil, false
	}
	// Register the messages in this payload as aliases for this conversation
	// scope, so later compacted requests can resolve the same scope even when
	// messages.0 has changed. This is done even when the cache is empty so the
	// first request in a conversation can be rediscovered after compaction.
	if scope.fallbackKey {
		hashes := capClaudeThinkingReplayAliasMessages(helps.ClaudeThinkingReplayMessageHashes(scope.modelFamily, scope.callerHash, req.Payload))
		for _, m := range hashes {
			internalcache.RegisterClaudeThinkingReplayAlias(ctx, scope.modelFamily, scope.sessionKey, m.Hash, scope.firstUserHash)
		}
	}
	if !found {
		return scope, nil, false
	}
	// Normalize cached tool_use parts to match the shape the sanitizer will apply
	// to the upstream body, so an echo'd tool_use with provenance fields does not
	// fail the canonical comparison.
	normalized := make([][]byte, len(contents))
	for i, content := range contents {
		normalized[i] = helps.ClaudeThinkingReplayNormalizeCachedContent(content)
	}
	return scope, normalized, true
}

func cacheClaudeThinkingReplayResponse(ctx context.Context, scope claudeThinkingReplayScope, response []byte) {
	content := gjson.GetBytes(response, "content")
	if content.IsArray() {
		cacheClaudeThinkingReplayContent(ctx, scope, []byte(content.Raw))
		return
	}
	accumulator := newKimiThinkingReplayStreamAccumulator()
	accumulator.observe(response)
	if content, completed := accumulator.content(); completed {
		cacheClaudeThinkingReplayContent(ctx, scope, content)
	}
}

// claudeThinkingReplayContentIsReplayable reports whether a content array
// carries a decodable Claude thinking signature. Only provenanced signed turns
// are cached; unsigned or malformed-signature responses must not evict earlier
// replay state.

func cacheClaudeThinkingReplayContent(ctx context.Context, scope claudeThinkingReplayScope, content []byte) {
	if !scope.valid() || !scope.cacheReady {
		return
	}
	// Unsigned or non-replayable responses must not evict earlier signed turns.
	// Only append turns that carry signed thinking; prior replay state is retained
	// for the next request that echoes an earlier assistant message.
	if helps.ClaudeThinkingReplayContentIsReplayable(content) {
		replaced, errReplace := internalcache.ReplaceClaudeThinkingReplayIfUnchanged(ctx, scope.modelFamily, scope.sessionKey, scope.snapshot, content)
		if errReplace != nil {
			log.Warnf("claude compatible thinking replay cache replace failed: %v", errReplace)
		} else if replaced && scope.fallbackKey {
			// Register the client-visible assistant shape as an alias only after a
			// successful cache write, so aliases do not point at a missing or
			// failed replay record.
			if h := helps.ClaudeThinkingReplayAssistantMessageHash(scope.modelFamily, scope.callerHash, content); h != "" {
				internalcache.RegisterClaudeThinkingReplayAlias(ctx, scope.modelFamily, scope.sessionKey, h, scope.firstUserHash)
			}
		}
	}
}

func clearClaudeThinkingReplayContent(ctx context.Context, scope claudeThinkingReplayScope) {
	if !scope.valid() || !scope.cacheReady {
		return
	}
	if _, errDelete := internalcache.DeleteClaudeThinkingReplayIfUnchanged(ctx, scope.modelFamily, scope.sessionKey, scope.snapshot); errDelete != nil {
		log.Warnf("claude compatible thinking replay cache delete failed: %v", errDelete)
	}
}

func wrapClaudeThinkingReplayStream(ctx context.Context, result *cliproxyexecutor.StreamResult, scope claudeThinkingReplayScope) *cliproxyexecutor.StreamResult {
	return wrapThinkingReplayStream(ctx, result, scope, cacheClaudeThinkingReplayContent, clearClaudeThinkingReplayContent)
}
