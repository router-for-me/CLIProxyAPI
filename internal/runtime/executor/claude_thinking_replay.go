package executor

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
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

// A missing session identity or conversation nonce intentionally disables
// replay instead of sharing hidden reasoning across callers. When the caller
// provides a conversation nonce (client_metadata.conversation_id,
// conversation_id body field, or X-Conversation-Id header) but no explicit
// session, we fall back to a conversation-scoped key that mixes the nonce,
// caller identity and first message.
func claudeThinkingReplayScopeFromRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) claudeThinkingReplayScope {
	modelFamily := claudeThinkingReplayModelFamily(auth, req.Model)
	callerHash := claudeThinkingReplayCallerHash(auth, req, opts)
	firstUserHash := claudeThinkingReplayFirstUserHash(modelFamily, callerHash, req.Payload)
	sessionKey := codexReasoningReplaySessionKey(ctx, sdktranslator.FormatClaude, req, opts, req.Payload)
	fallback := false
	if sessionKey != "" {
		sessionKey = xaiReasoningReplayIsolateSessionKey(ctx, sessionKey)
	}
	if sessionKey == "" {
		sessionKey = helps.ClaudeThinkingReplayConversationSessionKey(auth, req, opts)
		fallback = sessionKey != ""
	}
	return claudeThinkingReplayScope{
		modelFamily:   modelFamily,
		sessionKey:    sessionKey,
		fallbackKey:   fallback,
		callerHash:    callerHash,
		firstUserHash: firstUserHash,
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
		for _, m := range claudeThinkingReplayMessageHashes(scope.modelFamily, scope.callerHash, req.Payload) {
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

	// Collect the assistant messages whose content we may be able to restore.
	var assistantContents []gjson.Result
	var assistantMsgIndices []int
	for i, message := range msgList {
		if !strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "assistant") {
			continue
		}
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		assistantContents = append(assistantContents, content)
		assistantMsgIndices = append(assistantMsgIndices, i)
	}

	// Anchor the match window to the latest suffix of cached turns that matches
	// the request's assistant sequence. When clients compact or truncate
	// earlier history, the remaining sequence is a suffix of the conversation;
	// duplicate visible content must resolve to the correct retained turn.
	start := -1
	if len(assistantContents) > 0 {
		start = claudeThinkingReplayFindStartIndex(assistantContents, cachedContents)
	}
	if start >= 0 {
		for j := 0; j < start; j++ {
			consumed[j] = true
		}
	}

	from := 0
	if start >= 0 {
		from = start
	}

	for ai, i := range assistantMsgIndices {
		content := assistantContents[ai]
		matchedJ := -1
		// When anchored, the aligned cached turn should be at start+ai.
		if start >= 0 && start+ai < len(cachedContents) {
			if claudeThinkingReplayContentsMatch(content, gjson.ParseBytes(cachedContents[start+ai])) {
				matchedJ = start + ai
			}
		}
		if matchedJ < 0 {
			for j := from; j < len(cachedContents); j++ {
				if consumed[j] {
					continue
				}
				cached := gjson.ParseBytes(cachedContents[j])
				if claudeThinkingReplayContentsMatch(content, cached) {
					matchedJ = j
					break
				}
			}
		}
		if matchedJ < 0 {
			continue
		}
		if !kimiJSONEqual([]byte(content.Raw), cachedContents[matchedJ]) {
			var errSet error
			updated, errSet = sjson.SetRawBytes(updated, fmt.Sprintf("messages.%d.content", i), cachedContents[matchedJ])
			if errSet != nil {
				return body, false
			}
			restored = true
		}
		consumed[matchedJ] = true
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

// claudeThinkingReplayFindStartIndex finds the latest starting index in
// cachedContents such that the full assistantContents sequence can be matched
// as a subsequence in order. This anchors the replay window to the retained
// suffix of the conversation, so duplicate visible assistant turns resolve to
// the correct cached thinking/signature after compaction or truncation.
// It returns -1 when no such anchor exists.
func claudeThinkingReplayFindStartIndex(assistantContents []gjson.Result, cachedContents [][]byte) int {
	if len(assistantContents) == 0 || len(cachedContents) == 0 {
		return -1
	}
	bestStart := -1
	bestLen := 0
	for start := 0; start < len(cachedContents); start++ {
		j := start
		prefixLen := 0
		for i := 0; i < len(assistantContents) && j < len(cachedContents); i++ {
			matched := false
			for j < len(cachedContents) {
				if claudeThinkingReplayContentsMatch(assistantContents[i], gjson.ParseBytes(cachedContents[j])) {
					matched = true
					j++
					break
				}
				j++
			}
			if !matched {
				break
			}
			prefixLen++
		}
		if prefixLen > bestLen || (prefixLen == bestLen && start > bestStart) {
			bestLen = prefixLen
			bestStart = start
		}
	}
	if bestLen == 0 {
		return -1
	}
	return bestStart
}

// claudeThinkingReplayMessageHashes returns a stable weighted hash for each
// user and assistant message in the payload. User messages receive a higher
// weight because they are the strongest conversation anchor; an echoed
// assistant can be shared across conversations and is a weaker signal.
func claudeThinkingReplayMessageHashes(modelFamily, callerHash string, payload []byte) []internalcache.ClaudeThinkingReplayAliasMessage {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return nil
	}
	var out []internalcache.ClaudeThinkingReplayAliasMessage
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
		if h == "" {
			continue
		}
		weight := 2
		if role == "assistant" {
			weight = 1
		}
		out = append(out, internalcache.ClaudeThinkingReplayAliasMessage{Hash: h, Weight: weight})
	}
	return out
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

func claudeThinkingReplayFirstUserHash(modelFamily, callerHash string, payload []byte) string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return ""
	}
	for _, msg := range messages.Array() {
		if strings.ToLower(strings.TrimSpace(msg.Get("role").String())) != "user" {
			continue
		}
		if h := claudeThinkingReplayUserMessageHash(modelFamily, callerHash, msg); h != "" {
			return h
		}
	}
	return ""
}

func claudeThinkingReplayCallerHash(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	h := sha256.New()
	var identity string
	if auth != nil {
		if id := strings.TrimSpace(auth.ID); id != "" {
			identity = id
		} else if apiKey, _ := claudeCreds(auth); apiKey != "" {
			identity = apiKey
		}
	}
	claudeThinkingReplayHashString(h, identity)
	claudeThinkingReplayHashString(h, metadataString(opts.Metadata, cliproxyexecutor.CallerScopeMetadataKey))
	claudeThinkingReplayHashString(h, metadataString(req.Metadata, cliproxyexecutor.CallerScopeMetadataKey))
	claudeThinkingReplayHashString(h, metadataString(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey))
	claudeThinkingReplayHashString(h, metadataString(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey))
	claudeThinkingReplayHashString(h, headerFirstValue(opts.Headers, "User-Agent"))
	claudeThinkingReplayHashString(h, headerFirstValue(opts.Headers, "X-App"))
	claudeThinkingReplayHashString(h, headerFirstValue(opts.Headers, "X-Codex-Client-Id"))
	return hex.EncodeToString(h.Sum(nil))
}

func claudeThinkingReplayHashString(h hash.Hash, s string) {
	claudeThinkingReplayHashBytes(h, []byte(s))
}

func claudeThinkingReplayHashBytes(h hash.Hash, b []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(b)))
	h.Write(length[:])
	h.Write(b)
}

func headerFirstValue(headers http.Header, key string) string {
	if headers == nil {
		return ""
	}
	for k, vv := range headers {
		if strings.EqualFold(k, key) && len(vv) > 0 {
			if v := strings.TrimSpace(vv[0]); v != "" {
				return v
			}
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
