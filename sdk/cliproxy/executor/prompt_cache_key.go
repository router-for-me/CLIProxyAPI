package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// Prompt-cache-key attribution labels.
//
// OpenAI-style upstreams (codex, xAI, ...) route prompt-cache reads by prompt_cache_key or a
// vendor-native routing header. When a request misses the cache, an operator needs to know
// who chose the routing key and which requests shared it. ResolvePromptCacheKey answers that
// once per request, and the usage record carries two log-safe labels:
//
//   - cache_key_source: who supplied the routing identity.
//   - cache_key_id:     sha256(identity)[:16] (hex). Never the key or the session id itself.
//     Requests with the same id share a routing key (caller, session) or a stable prompt
//     prefix (derived), so a cache_key_id seen only once marks a prefix that no other
//     request could have read from the cache.
//
// The labels are observation only. They do not change the request body, the headers sent
// upstream or credential selection.
//
// Resolution (first hit wins):
//  1. passthrough - the body carries an explicit empty prompt_cache_key; no id.
//  2. caller      - the body carries prompt_cache_key, or the client sent a vendor-native
//     routing header (codex Session_id / Session-Id, xAI X-Grok-Conv-Id).
//  3. session     - the client sent a session id that is not a vendor cache key (execution
//     session, Claude Code session in metadata.user_id, X-Session-ID, Conversation_id,
//     conversation / conversation_id, a bare metadata.user_id). The identity is a hash of it.
//  4. derived     - "pck-" + sha256(stable prefix)[:32], see DerivePromptCacheKey.
//     When the payload has no prompt to hash, the source is passthrough.
const (
	// PromptCacheKeySourceMetadataKey carries the resolution source in Options.Metadata.
	PromptCacheKeySourceMetadataKey = "prompt_cache_key_source"
	// PromptCacheKeyIDMetadataKey carries sha256(identity)[:16] in Options.Metadata. Never the key.
	PromptCacheKeyIDMetadataKey = "prompt_cache_key_id"

	PromptCacheKeySourceCaller      = "caller"
	PromptCacheKeySourceSession     = "session"
	PromptCacheKeySourceDerived     = "derived"
	PromptCacheKeySourcePassthrough = "passthrough"

	promptCacheKeyPrefix         = "pck-"
	promptCacheFirstUserHeadSize = 4096
)

// PromptCacheKeyResolution is the outcome of ResolvePromptCacheKey.
type PromptCacheKeyResolution struct {
	// Source is caller, session, derived or passthrough.
	Source string
	// Key is the routing identity: the caller's own key for caller, a hash of the session id
	// for session, the prefix hash for derived, empty for passthrough. It is not logged.
	Key string
	// ID is PromptCacheKeyID(Key), empty when Key is empty.
	ID string
}

var claudeCodeSessionPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

// ResolvePromptCacheKey decides the routing identity of one request. provider is the
// executor identity (codex, xai, ...); payload is the source-format body; headers and
// metadata are the inbound request's.
func ResolvePromptCacheKey(provider string, payload []byte, headers http.Header, metadata map[string]any) PromptCacheKeyResolution {
	if explicitEmptyPromptCacheKey(payload) {
		return PromptCacheKeyResolution{Source: PromptCacheKeySourcePassthrough}
	}
	if len(payload) > 0 {
		if v := gjson.GetBytes(payload, "prompt_cache_key"); v.Exists() && strings.TrimSpace(v.String()) != "" {
			key := strings.TrimSpace(v.String())
			return PromptCacheKeyResolution{Source: PromptCacheKeySourceCaller, Key: key, ID: PromptCacheKeyID(key)}
		}
	}
	if key := nativeRoutingHeader(headers); key != "" {
		return PromptCacheKeyResolution{Source: PromptCacheKeySourceCaller, Key: key, ID: PromptCacheKeyID(key)}
	}
	if sessionID, ok := callerSessionID(payload, headers, metadata); ok {
		key := hashedPromptCacheKey("session", sessionID)
		return PromptCacheKeyResolution{Source: PromptCacheKeySourceSession, Key: key, ID: PromptCacheKeyID(key)}
	}
	key := DerivePromptCacheKey(provider, payload)
	if key == "" {
		return PromptCacheKeyResolution{Source: PromptCacheKeySourcePassthrough}
	}
	return PromptCacheKeyResolution{Source: PromptCacheKeySourceDerived, Key: key, ID: PromptCacheKeyID(key)}
}

// PromptCacheKeyID is the log-safe identity of a routing key: sha256 hex, 16 chars.
func PromptCacheKeyID(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}

// ApplyPromptCacheKeyMetadata records the labels of a resolution in metadata and returns the
// map. The map is updated in place (created when nil) because the request pipeline and
// handlers may hold a reference to it. Only the source and the id are recorded, never the key.
func ApplyPromptCacheKeyMetadata(metadata map[string]any, res PromptCacheKeyResolution) map[string]any {
	out := metadata
	if out == nil {
		out = make(map[string]any, 2)
	}
	if res.Source == "" {
		res.Source = PromptCacheKeySourcePassthrough
	}
	out[PromptCacheKeySourceMetadataKey] = res.Source
	if res.ID != "" {
		out[PromptCacheKeyIDMetadataKey] = res.ID
	}
	return out
}

// PromptCacheKeySourceFromMetadata returns the recorded source, or "" when unresolved.
func PromptCacheKeySourceFromMetadata(metadata map[string]any) string {
	return promptCacheMetadataString(metadata, PromptCacheKeySourceMetadataKey)
}

// PromptCacheKeyIDFromMetadata returns the recorded id, or "".
func PromptCacheKeyIDFromMetadata(metadata map[string]any) string {
	return promptCacheMetadataString(metadata, PromptCacheKeyIDMetadataKey)
}

func promptCacheMetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

// nativeRoutingHeaderNames are headers the vendors themselves route by (codex Session_id,
// xAI x-grok-conv-id). A value here is the caller's own routing key (source caller).
var nativeRoutingHeaderNames = []string{"Session_id", "Session-Id", "X-Grok-Conv-Id"}

func nativeRoutingHeader(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for _, name := range nativeRoutingHeaderNames {
		if v := strings.TrimSpace(headers.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// callerSessionID returns a session identifier the client already sent that is not a vendor
// cache key. Per-request ids (X-Client-Request-Id) are not sessions.
func callerSessionID(payload []byte, headers http.Header, metadata map[string]any) (string, bool) {
	if v := promptCacheMetadataString(metadata, ExecutionSessionMetadataKey); v != "" {
		return v, true
	}
	if len(payload) > 0 {
		if userID := gjson.GetBytes(payload, "metadata.user_id").String(); userID != "" {
			if m := claudeCodeSessionPattern.FindStringSubmatch(userID); len(m) >= 2 {
				return m[1], true
			}
			if userID[0] == '{' {
				if sid := gjson.Get(userID, "session_id").String(); sid != "" {
					return sid, true
				}
			}
		}
	}
	if headers != nil {
		for _, name := range []string{"X-Session-ID", "Conversation_id"} {
			if v := strings.TrimSpace(headers.Get(name)); v != "" {
				return v, true
			}
		}
	}
	if len(payload) > 0 {
		if userID := strings.TrimSpace(gjson.GetBytes(payload, "metadata.user_id").String()); userID != "" {
			return userID, true
		}
		if conv := strings.TrimSpace(gjson.GetBytes(payload, "conversation_id").String()); conv != "" {
			return conv, true
		}
		// Responses API conversation object: "conversation": "conv_x" or {"id": "conv_x"}.
		if conv := gjson.GetBytes(payload, "conversation"); conv.Exists() {
			if conv.Type == gjson.String && strings.TrimSpace(conv.String()) != "" {
				return strings.TrimSpace(conv.String()), true
			}
			if id := strings.TrimSpace(conv.Get("id").String()); id != "" {
				return id, true
			}
		}
	}
	return "", false
}

// hashedPromptCacheKey turns a client session identifier into a routing identity that never
// carries the raw (possibly user- or account-bearing) value.
func hashedPromptCacheKey(kind, id string) string {
	h := sha256.Sum256([]byte(kind + ":" + id))
	return promptCacheKeyPrefix + hex.EncodeToString(h[:])[:32]
}

// explicitEmptyPromptCacheKey reports a body prompt_cache_key that is present but blank:
// the caller asked for no routing key.
func explicitEmptyPromptCacheKey(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	v := gjson.GetBytes(payload, "prompt_cache_key")
	return v.Exists() && strings.TrimSpace(v.String()) == ""
}

// DerivePromptCacheKey hashes the stable prefix of a source-format request: provider, model,
// system/developer text, canonical tool definitions and the first 4 KiB of the first user
// message. Requests that share those bytes share a key. It returns "" when the payload
// carries no prompt at all, so unrelated empty requests are never grouped together.
func DerivePromptCacheKey(provider string, payload []byte) string {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return ""
	}
	model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	system, tools, firstUser := promptCachePrefixParts(payload)
	if system == "" && tools == "" && firstUser == "" {
		return ""
	}
	h := sha256.New()
	for _, part := range []string{strings.TrimSpace(provider), model, system, tools, firstUser} {
		if part == "" {
			part = "-"
		}
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return promptCacheKeyPrefix + hex.EncodeToString(h.Sum(nil))[:32]
}

// promptCachePrefixParts extracts (system text, canonical tools JSON, first-user head) from
// chat/completions, Responses, Claude and Gemini shaped bodies.
func promptCachePrefixParts(payload []byte) (system, tools, firstUser string) {
	var systemParts []string
	addSystem := func(text string) {
		if text = strings.TrimSpace(text); text != "" {
			systemParts = append(systemParts, text)
		}
	}

	// Responses API: instructions + input[] ; chat: messages[] ; Gemini: contents[].
	if v := gjson.GetBytes(payload, "instructions"); v.Type == gjson.String {
		addSystem(v.String())
	}
	for _, listKey := range []string{"messages", "input", "contents"} {
		list := gjson.GetBytes(payload, listKey)
		if listKey == "input" && list.Type == gjson.String {
			// Responses API shorthand: input is the user text.
			if firstUser == "" {
				firstUser = headOf(list.String())
			}
			continue
		}
		if !list.IsArray() {
			continue
		}
		list.ForEach(func(_, item gjson.Result) bool {
			role := item.Get("role").String()
			content := item.Get("content")
			if !content.Exists() {
				content = item.Get("parts")
			}
			text := promptCacheContentText(content)
			switch role {
			case "system", "developer":
				addSystem(text)
			case "user":
				if firstUser == "" && text != "" {
					firstUser = headOf(text)
				}
			}
			return true
		})
	}
	// Claude: top-level system (string or blocks). Gemini: systemInstruction.parts.
	if v := gjson.GetBytes(payload, "system"); v.Exists() {
		addSystem(promptCacheContentText(v))
	}
	if v := gjson.GetBytes(payload, "systemInstruction.parts"); v.IsArray() {
		addSystem(promptCacheContentText(v))
	}
	if v := gjson.GetBytes(payload, "system_instruction.parts"); v.IsArray() {
		addSystem(promptCacheContentText(v))
	}
	system = strings.Join(systemParts, "\n")

	if v := gjson.GetBytes(payload, "tools"); v.IsArray() && len(v.Array()) > 0 {
		tools = canonicalJSON(v.Raw)
	}
	return system, tools, firstUser
}

// promptCacheContentText flattens a content value (string, or an array of text parts) to text.
func promptCacheContentText(content gjson.Result) string {
	switch {
	case content.Type == gjson.String:
		return content.String()
	case content.IsArray():
		var b strings.Builder
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Type == gjson.String {
				b.WriteString(part.String())
				return true
			}
			if t := part.Get("text"); t.Type == gjson.String {
				b.WriteString(t.String())
			}
			return true
		})
		return b.String()
	case content.IsObject():
		if t := content.Get("text"); t.Type == gjson.String {
			return t.String()
		}
	}
	return ""
}

func headOf(text string) string {
	if len(text) > promptCacheFirstUserHeadSize {
		return text[:promptCacheFirstUserHeadSize]
	}
	return text
}

// canonicalJSON re-encodes raw JSON with sorted object keys and no insignificant
// whitespace, so two encodings of one tool list hash the same. Numbers are kept as their
// literal digits (no float64 round trip) and <, > and & are not escaped, so the canonical
// form of a schema with large integers or HTML in a description is its own bytes.
func canonicalJSON(raw string) string {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return strings.TrimSpace(raw)
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return strings.TrimSpace(raw)
	}
	return strings.TrimSpace(buf.String())
}
