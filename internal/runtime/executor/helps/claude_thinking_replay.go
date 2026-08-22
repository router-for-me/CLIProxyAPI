package helps

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ClaudeThinkingReplayModelFamily returns a stable per-credential, per-model
// family name used to namespace replay state.
func ClaudeThinkingReplayModelFamily(auth *cliproxyauth.Auth, model string) string {
	baseModel := thinking.ParseSuffix(strings.TrimSpace(model)).ModelName
	if baseModel == "" {
		return ""
	}
	identity := ""
	if auth != nil {
		identity = strings.TrimSpace(auth.ID)
		if identity == "" {
			apiKey, baseURL := ClaudeCredentialKey(auth)
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

// ObfuscateClaudeThinkingReplayContents applies the same sensitive-word
// obfuscation to cached assistant content that applyCloaking applies to the
// upstream body. This lets the post-cloak replay match compare like-for-like
// bytes instead of failing because the caller body is obfuscated and the cache
// is not.
func ObfuscateClaudeThinkingReplayContents(contents [][]byte, words []string) [][]byte {
	matcher := BuildSensitiveWordMatcher(words)
	if matcher == nil {
		return contents
	}
	out := make([][]byte, len(contents))
	for i, content := range contents {
		wrapper, _ := sjson.SetRawBytes([]byte(`{"messages":[{"role":"assistant"}]}`), "messages.0.content", content)
		obfuscated := ObfuscateSensitiveWords(wrapper, matcher)
		obfuscatedContent := gjson.GetBytes(obfuscated, "messages.0.content")
		if !obfuscatedContent.Exists() {
			out[i] = content
			continue
		}
		out[i] = []byte(obfuscatedContent.Raw)
	}
	return out
}

// ClaudeThinkingReplayNormalizeCachedContent strips tool-use signature/provenance
// fields from a cached assistant content array. This lets the replay match compare
// the same normalized shape the upstream sanitizer produces, while the restored
// content still carries the trusted thinking signature.
func ClaudeThinkingReplayNormalizeCachedContent(content []byte) []byte {
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

// StripClaudeThinkingReplayProvenanceMarkers removes any client-supplied
// _cliproxy_replay_provenance fields from thinking blocks in the request payload
// before the sanitizer runs. The marker is internal-only.
func StripClaudeThinkingReplayProvenanceMarkers(payload []byte) []byte {
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

// RestoreClaudeThinkingReplayContents replaces visible assistant content in the
// request body with cached normalized content when the visible parts match.
func RestoreClaudeThinkingReplayContents(body []byte, cachedContents [][]byte) ([]byte, bool) {
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
		if content.Type == gjson.String {
			normalized, err := json.Marshal([]map[string]string{{"type": "text", "text": content.String()}})
			if err != nil {
				continue
			}
			content = gjson.ParseBytes(normalized)
		} else if !content.IsArray() {
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
		start = ClaudeThinkingReplayFindStartIndex(assistantContents, cachedContents)
	}
	if start >= 0 {
		for j := 0; j < start; j++ {
			consumed[j] = true
		}
	}

	from := 0
	if start >= 0 {
		from = start
	} else {
		// No cached suffix matches a contiguous block; refuse partial fallback
		// that could pair a retained turn with the wrong hidden signature.
		from = len(cachedContents)
	}

	for ai, i := range assistantMsgIndices {
		content := assistantContents[ai]
		matchedJ := -1
		// When anchored, the aligned cached turn should be at start+ai.
		if start >= 0 && start+ai < len(cachedContents) {
			if ClaudeThinkingReplayContentsMatch(content, gjson.ParseBytes(cachedContents[start+ai])) {
				matchedJ = start + ai
			}
		}
		if matchedJ < 0 {
			for j := from; j < len(cachedContents); j++ {
				if consumed[j] {
					continue
				}
				cached := gjson.ParseBytes(cachedContents[j])
				if ClaudeThinkingReplayContentsMatch(content, cached) {
					matchedJ = j
					break
				}
			}
		}
		if matchedJ < 0 {
			continue
		}
		if !JSONEqual([]byte(content.Raw), cachedContents[matchedJ]) {
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

// ClaudeThinkingReplayContentsMatch reports whether an incoming assistant
// content array matches a cached assistant turn. It accepts exact equality or
// non-thinking parts equal and, when the incoming content already contains a
// thinking block, the thinking text matching the cached one.
func ClaudeThinkingReplayContentsMatch(currentContent, cachedContent gjson.Result) bool {
	if !currentContent.IsArray() || !cachedContent.IsArray() {
		return false
	}
	if JSONEqual([]byte(currentContent.Raw), []byte(cachedContent.Raw)) {
		return true
	}
	cachedParts, ok := NonThinkingContentParts(cachedContent)
	if !ok {
		return false
	}
	currentParts, ok := NonThinkingContentParts(currentContent)
	if !ok || !CanonicalPartsEqual(currentParts, cachedParts) {
		return false
	}
	if ContentHasThinking(currentContent) && !ThinkingMatchesCachedIgnoringSignature(currentContent, cachedContent) {
		return false
	}
	return true
}

// ClaudeThinkingReplayFindStartIndex finds the latest starting index in
// cachedContents such that the full assistantContents sequence can be matched
// as a subsequence in order. This anchors the replay window to the retained
// suffix of the conversation, so duplicate visible assistant turns resolve to
// the correct cached thinking/signature after compaction or truncation.
// It returns -1 when no such anchor exists.
func ClaudeThinkingReplayFindStartIndex(assistantContents []gjson.Result, cachedContents [][]byte) int {
	if len(assistantContents) == 0 || len(cachedContents) == 0 {
		return -1
	}
	maxL := len(assistantContents)
	if maxL > len(cachedContents) {
		maxL = len(cachedContents)
	}
	for l := maxL; l >= 1; l-- {
		start := len(cachedContents) - l
		for off := 0; off <= len(assistantContents)-l; off++ {
			matched := true
			for k := 0; k < l; k++ {
				if !ClaudeThinkingReplayContentsMatch(assistantContents[off+k], gjson.ParseBytes(cachedContents[start+k])) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			// A partial suffix that leaves trailing turns unmatched is
			// ambiguous when another cached block matches the same request
			// segment; a duplicate visible turn could supply the wrong hidden
			// signature. Suffix-of-request matches are still allowed.
			if off+l < len(assistantContents) {
				ambiguous := false
				for d := 0; d <= len(cachedContents)-l; d++ {
					if d == start {
						continue
					}
					otherMatched := true
					for k := 0; k < l; k++ {
						if !ClaudeThinkingReplayContentsMatch(assistantContents[off+k], gjson.ParseBytes(cachedContents[d+k])) {
							otherMatched = false
							break
						}
					}
					if otherMatched {
						ambiguous = true
						break
					}
				}
				if ambiguous {
					continue
				}
			}
			return start
		}
	}
	return -1
}

// ClaudeThinkingReplayMessageHashes returns a stable weighted hash for each
// user and assistant message in the payload. User messages receive a higher
// weight because they are the strongest conversation anchor; an echoed
// assistant can be shared across conversations and is a weaker signal.
func ClaudeThinkingReplayMessageHashes(modelFamily, callerHash string, payload []byte) []internalcache.ClaudeThinkingReplayAliasMessage {
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
			h = ClaudeThinkingReplayAssistantMessageHash(modelFamily, callerHash, []byte(msg.Get("content").Raw))
		} else {
			h = ClaudeThinkingReplayUserMessageHash(modelFamily, callerHash, msg)
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

// ClaudeThinkingReplayUserMessageHash returns a stable hash for a user message.
func ClaudeThinkingReplayUserMessageHash(modelFamily, callerHash string, msg gjson.Result) string {
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
	canon, ok := CanonicalJSON(raw)
	if !ok {
		return ""
	}
	return ClaudeThinkingReplayHash(modelFamily, callerHash, canon)
}

// ClaudeThinkingReplayAssistantMessageHash returns a stable hash for the
// non-thinking parts of an assistant message.
func ClaudeThinkingReplayAssistantMessageHash(modelFamily, callerHash string, content []byte) string {
	parts, ok := NonThinkingContentParts(gjson.ParseBytes(content))
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
	canon, ok := CanonicalJSON(raw)
	if !ok {
		return ""
	}
	return ClaudeThinkingReplayHash(modelFamily, callerHash, canon)
}

// ClaudeThinkingReplayHash returns a length-prefixed SHA-256 hash of the model
// family, caller hash, and canonical content.
func ClaudeThinkingReplayHash(modelFamily, callerHash string, canon []byte) string {
	h := sha256.New()
	h.Write([]byte(modelFamily))
	h.Write([]byte{0})
	h.Write([]byte(callerHash))
	h.Write([]byte{0})
	h.Write(canon)
	return hex.EncodeToString(h.Sum(nil))
}

// ClaudeThinkingReplayFirstUserHash returns the hash of the first user message
// in the payload.
func ClaudeThinkingReplayFirstUserHash(modelFamily, callerHash string, payload []byte) string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return ""
	}
	for _, msg := range messages.Array() {
		if strings.ToLower(strings.TrimSpace(msg.Get("role").String())) != "user" {
			continue
		}
		if h := ClaudeThinkingReplayUserMessageHash(modelFamily, callerHash, msg); h != "" {
			return h
		}
	}
	return ""
}

// ClaudeThinkingReplayCallerHash returns a stable hash of the caller identity
// and scope headers.
func ClaudeThinkingReplayCallerHash(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	h := sha256.New()
	var identity string
	if auth != nil {
		if id := strings.TrimSpace(auth.ID); id != "" {
			identity = id
		} else if apiKey, _ := ClaudeCredentialKey(auth); apiKey != "" {
			identity = apiKey
		}
	}
	claudeThinkingReplayHashString(h, identity)
	claudeThinkingReplayHashString(h, MetadataString(opts.Metadata, cliproxyexecutor.CallerScopeMetadataKey))
	claudeThinkingReplayHashString(h, MetadataString(req.Metadata, cliproxyexecutor.CallerScopeMetadataKey))
	claudeThinkingReplayHashString(h, MetadataString(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey))
	claudeThinkingReplayHashString(h, MetadataString(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey))
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

// ClaudeThinkingReplayContentIsReplayable reports whether a content array
// carries a decodable Claude thinking signature. Only provenanced signed turns
// are cached; unsigned or malformed-signature responses must not evict earlier
// replay state.
func ClaudeThinkingReplayContentIsReplayable(content []byte) bool {
	root := gjson.ParseBytes(content)
	if !root.IsArray() {
		return false
	}
	for _, part := range root.Array() {
		if strings.TrimSpace(part.Get("type").String()) != "thinking" {
			continue
		}
		if signature.HasDecodableClaudeThinkingSignature(part.Get("signature").String()) {
			return true
		}
	}
	return false
}
