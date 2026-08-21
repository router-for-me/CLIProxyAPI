package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

// A missing session identity intentionally disables replay instead of sharing hidden reasoning across callers.
// When no caller session is available, we fall back to a conversation-scoped
// key derived from the first user message and system content, so distinct
// conversations through the same credential cannot see each other's cached
// signatures.
func claudeThinkingReplayScopeFromRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) claudeThinkingReplayScope {
	modelFamily := claudeThinkingReplayModelFamily(auth, req.Model)
	callerHash := claudeThinkingReplayCallerHash(auth, req, opts)
	sessionKey := codexReasoningReplaySessionKey(ctx, sdktranslator.FormatClaude, req, opts, req.Payload)
	fallback := false
	if sessionKey != "" {
		sessionKey = xaiReasoningReplayIsolateSessionKey(ctx, sessionKey)
	}
	if sessionKey == "" {
		sessionKey = helps.ClaudeThinkingReplayConversationSessionKey(auth, req, opts)
		fallback = true
	}
	// When the sessionless fallback key is based on messages.0, a compacted
	// history can change the key and orphan cached turns. Try to resolve the
	// original conversation scope through any remaining message.
	if fallback && sessionKey != "" {
		if resolved, ok := internalcache.ResolveClaudeThinkingReplaySessionKey(modelFamily, claudeThinkingReplayMessageHashes(modelFamily, callerHash, req.Payload)); ok {
			sessionKey = resolved
		}
	}
	return claudeThinkingReplayScope{
		modelFamily: modelFamily,
		sessionKey:  sessionKey,
		fallbackKey: fallback,
		callerHash:  callerHash,
	}
}

func claudeThinkingReplayModelFamily(auth *cliproxyauth.Auth, model string) string {
	baseModel := thinking.ParseSuffix(strings.TrimSpace(model)).ModelName
	if baseModel == "" {
		return ""
	}
	identity := ""
	if auth != nil {
		identity = strings.TrimSpace(auth.ID)
		if identity == "" {
			apiKey, baseURL := claudeCreds(auth)
			identity = strings.TrimSpace(baseURL)
			if identity == "" {
				identity = strings.TrimSpace(apiKey)
			}
		}
	}
	if identity == "" {
		return "claude:" + baseModel
	}
	sum := sha256.Sum256([]byte(identity))
	return "claude:" + hex.EncodeToString(sum[:8]) + ":" + baseModel
}

// obfuscateClaudeThinkingReplayContents applies the same sensitive-word
// obfuscation to cached assistant content that applyCloaking applies to the
// upstream body. This lets the post-cloak replay match compare like-for-like
// bytes instead of failing because the caller body is obfuscated and the cache
// is not.
func obfuscateClaudeThinkingReplayContents(contents [][]byte, words []string) [][]byte {
	matcher := helps.BuildSensitiveWordMatcher(words)
	if matcher == nil {
		return contents
	}
	out := make([][]byte, len(contents))
	for i, content := range contents {
		wrapper, _ := sjson.SetRawBytes([]byte(`{"messages":[{"role":"assistant"}]}`), "messages.0.content", content)
		obfuscated := helps.ObfuscateSensitiveWords(wrapper, matcher)
		obfuscatedContent := gjson.GetBytes(obfuscated, "messages.0.content")
		if !obfuscatedContent.Exists() {
			out[i] = content
			continue
		}
		out[i] = []byte(obfuscatedContent.Raw)
	}
	return out
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

	req.Payload = stripClaudeThinkingReplayProvenanceMarkers(req.Payload)

	contents, snapshot, found, errGet := internalcache.GetClaudeThinkingReplayWithSnapshotRequired(ctx, scope.modelFamily, scope.sessionKey)
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
		for _, h := range claudeThinkingReplayMessageHashes(scope.modelFamily, scope.callerHash, req.Payload) {
			internalcache.RegisterClaudeThinkingReplayAlias(scope.modelFamily, scope.sessionKey, h)
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
		normalized[i] = claudeThinkingReplayNormalizeCachedContent(content)
	}
	return scope, normalized, true
}

// claudeThinkingReplayNormalizeCachedContent strips tool-use signature/provenance
// fields from a cached assistant content array. This lets the replay match compare
// the same normalized shape the upstream sanitizer produces, while the restored
// content still carries the trusted thinking signature.
func claudeThinkingReplayNormalizeCachedContent(content []byte) []byte {
	root := gjson.ParseBytes(content)
	if !root.IsArray() {
		return content
	}
	parts := root.Array()
	outParts := make([]string, len(parts))
	modified := false
	for i, part := range parts {
		if strings.TrimSpace(part.Get("type").String()) == "tool_use" {
			updated, changed := signature.StripClaudeToolUseSignatureFields(part)
			outParts[i] = updated
			modified = modified || changed
			continue
		}
		outParts[i] = part.Raw
	}
	if !modified {
		return content
	}
	return []byte("[" + strings.Join(outParts, ",") + "]")
}

// stripClaudeThinkingReplayProvenanceMarkers removes any client-supplied
// _cliproxy_replay_provenance fields from thinking blocks in the request payload
// before the sanitizer runs. The marker is internal-only.
func stripClaudeThinkingReplayProvenanceMarkers(payload []byte) []byte {
	root := gjson.GetBytes(payload, "messages")
	if !root.IsArray() {
		return payload
	}
	updated := payload
	modified := false
	for i, message := range root.Array() {
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		for j, part := range content.Array() {
			if strings.TrimSpace(part.Get("type").String()) != "thinking" {
				continue
			}
			if !part.Get("_cliproxy_replay_provenance").Exists() {
				continue
			}
			path := fmt.Sprintf("messages.%d.content.%d._cliproxy_replay_provenance", i, j)
			out, _ := sjson.DeleteBytes(updated, path)
			updated = out
			modified = true
		}
	}
	if !modified {
		return payload
	}
	return updated
}

func restoreClaudeThinkingReplayContents(body []byte, cachedContents [][]byte) ([]byte, bool) {
	updated := body
	restored := false
	consumed := make([]bool, len(cachedContents))
	messages := gjson.GetBytes(updated, "messages")
	if !messages.IsArray() {
		return body, false
	}
	msgList := messages.Array()

	// Anchor the match window to the first assistant message present in the
	// incoming request. When clients compact or truncate earlier history, cached
	// turns older than the first echoed assistant message must not be replayed
	// into a later matching turn.
	firstAssistant := -1
	for i, message := range msgList {
		if strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "assistant") {
			firstAssistant = i
			break
		}
	}
	if firstAssistant >= 0 {
		start := claudeThinkingReplayFindStartIndex(msgList[firstAssistant].Get("content"), cachedContents)
		for j := 0; j < start; j++ {
			consumed[j] = true
		}
	}

	for i, message := range msgList {
		if !strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "assistant") {
			continue
		}
		currentContent := message.Get("content")
		if !currentContent.IsArray() {
			continue
		}
		for j, cachedContent := range cachedContents {
			if consumed[j] {
				continue
			}
			cached := gjson.ParseBytes(cachedContent)
			if !claudeThinkingReplayContentsMatch(currentContent, cached) {
				continue
			}
			if !kimiJSONEqual([]byte(currentContent.Raw), cachedContent) {
				var errSet error
				updated, errSet = sjson.SetRawBytes(updated, fmt.Sprintf("messages.%d.content", i), cachedContent)
				if errSet != nil {
					return body, false
				}
				restored = true
			}
			consumed[j] = true
			break
		}
	}
	return updated, restored
}

// claudeThinkingReplayContentsMatch reports whether an incoming assistant
// content array matches a cached assistant turn. It accepts exact equality or
// non-thinking parts equal and, when the incoming content already contains a
// thinking block, the thinking text matching the cached one.
func claudeThinkingReplayContentsMatch(currentContent, cachedContent gjson.Result) bool {
	if !currentContent.IsArray() || !cachedContent.IsArray() {
		return false
	}
	if kimiJSONEqual([]byte(currentContent.Raw), []byte(cachedContent.Raw)) {
		return true
	}
	cachedParts, ok := kimiNonThinkingContentParts(cachedContent)
	if !ok {
		return false
	}
	currentParts, ok := kimiNonThinkingContentParts(currentContent)
	if !ok || !kimiCanonicalPartsEqual(currentParts, cachedParts) {
		return false
	}
	if kimiContentHasThinking(currentContent) && !kimiThinkingMatchesCachedIgnoringSignature(currentContent, cachedContent) {
		return false
	}
	return true
}

// claudeThinkingReplayFindStartIndex finds the index of the first cached turn
// that matches the first assistant message present in the request. Cached
// entries before this index are older than the client's oldest echoed
// assistant message and must not be replayed into later turns.
func claudeThinkingReplayFindStartIndex(firstContent gjson.Result, cachedContents [][]byte) int {
	if !firstContent.IsArray() {
		return 0
	}
	for j, cachedContent := range cachedContents {
		if claudeThinkingReplayContentsMatch(firstContent, gjson.ParseBytes(cachedContent)) {
			return j
		}
	}
	return 0
}

// claudeThinkingReplayMessageHashes returns a stable hash for each user and
// assistant message in the payload. These hashes are used to resolve and
// register conversation-scope aliases when a sessionless client compacts
// history so messages.0 no longer matches the original key.
func claudeThinkingReplayMessageHashes(modelFamily, callerHash string, payload []byte) []string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return nil
	}
	var hashes []string
	for _, msg := range messages.Array() {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		if role != "user" && role != "assistant" {
			continue
		}
		var h string
		if role == "assistant" {
			h = claudeThinkingReplayAssistantMessageHash(modelFamily, callerHash, []byte(msg.Get("content").Raw))
		} else {
			h = claudeThinkingReplayUserMessageHash(modelFamily, callerHash, msg)
		}
		if h != "" {
			hashes = append(hashes, h)
		}
	}
	return hashes
}

func claudeThinkingReplayUserMessageHash(modelFamily, callerHash string, msg gjson.Result) string {
	role := strings.TrimSpace(msg.Get("role").String())
	content := msg.Get("content")
	if role == "" {
		return ""
	}
	m := map[string]json.RawMessage{
		"role":    json.RawMessage(`"` + role + `"`),
		"content": json.RawMessage(content.Raw),
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	canon, ok := kimiCanonicalJSON(raw)
	if !ok {
		return ""
	}
	return claudeThinkingReplayHash(modelFamily, callerHash, canon)
}

func claudeThinkingReplayAssistantMessageHash(modelFamily, callerHash string, content []byte) string {
	parts, ok := kimiNonThinkingContentParts(gjson.ParseBytes(content))
	if !ok || len(parts) == 0 {
		return ""
	}
	partsJSON, err := json.Marshal(parts)
	if err != nil {
		return ""
	}
	m := map[string]json.RawMessage{
		"role":    json.RawMessage(`"assistant"`),
		"content": json.RawMessage(partsJSON),
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	canon, ok := kimiCanonicalJSON(raw)
	if !ok {
		return ""
	}
	return claudeThinkingReplayHash(modelFamily, callerHash, canon)
}

func claudeThinkingReplayHash(modelFamily, callerHash string, canon []byte) string {
	h := sha256.New()
	h.Write([]byte(modelFamily))
	h.Write([]byte{0})
	h.Write([]byte(callerHash))
	h.Write([]byte{0})
	h.Write(canon)
	return hex.EncodeToString(h.Sum(nil))
}

func claudeThinkingReplayCallerHash(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	h := sha256.New()
	if auth != nil {
		if id := strings.TrimSpace(auth.ID); id != "" {
			h.Write([]byte(id))
		} else if apiKey, _ := claudeCreds(auth); apiKey != "" {
			h.Write([]byte(apiKey))
		}
	}
	h.Write([]byte(metadataString(opts.Metadata, cliproxyexecutor.CallerScopeMetadataKey)))
	h.Write([]byte(metadataString(req.Metadata, cliproxyexecutor.CallerScopeMetadataKey)))
	h.Write([]byte(metadataString(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)))
	h.Write([]byte(metadataString(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)))
	h.Write([]byte(headerFirstValue(opts.Headers, "User-Agent")))
	h.Write([]byte(headerFirstValue(opts.Headers, "X-App")))
	h.Write([]byte(headerFirstValue(opts.Headers, "X-Codex-Client-Id")))
	return hex.EncodeToString(h.Sum(nil))
}

func headerFirstValue(headers http.Header, key string) string {
	if headers == nil {
		return ""
	}
	for k, vv := range headers {
		if strings.EqualFold(k, key) && len(vv) > 0 {
			return vv[0]
		}
	}
	return ""
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

func cacheClaudeThinkingReplayContent(ctx context.Context, scope claudeThinkingReplayScope, content []byte) {
	if !scope.valid() || !scope.cacheReady {
		return
	}
	// Unsigned or non-replayable responses must not evict earlier signed turns.
	// Only append turns that carry signed thinking; prior replay state is retained
	// for the next request that echoes an earlier assistant message.
	if kimiThinkingReplayContentIsReplayable(content) {
		if _, errReplace := internalcache.ReplaceClaudeThinkingReplayIfUnchanged(ctx, scope.modelFamily, scope.sessionKey, scope.snapshot, content); errReplace != nil {
			log.Warnf("claude compatible thinking replay cache replace failed: %v", errReplace)
		}
		// Register the client-visible assistant shape as an alias so a later
		// compacted request that leads with this assistant can resolve the
		// original conversation scope.
		if scope.fallbackKey {
			if h := claudeThinkingReplayAssistantMessageHash(scope.modelFamily, scope.callerHash, content); h != "" {
				internalcache.RegisterClaudeThinkingReplayAlias(scope.modelFamily, scope.sessionKey, h)
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
