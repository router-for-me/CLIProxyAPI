package helps

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"net/http"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// conversationNonceSources lists the gjson paths and header names that may
// carry an explicit conversation identifier. An explicit nonce is required for
// the fallback conversation key so two identical conversation openings do not
// share a replay scope.
var conversationNonceSources = struct {
	paths  []string
	header []string
}{
	paths:  []string{"client_metadata.conversation_id", "client_metadata.conversationId", "conversation_id"},
	header: []string{"X-Conversation-Id", "Conversation-Id", "Conversation_id"},
}

// claudeReplayConversationNonce returns a genuine conversation nonce when one
// is explicitly provided by the caller. It returns an empty string when no
// nonce exists, signaling that fallback replay should not be used.
func claudeReplayConversationNonce(payload []byte, headers http.Header) string {
	for _, path := range conversationNonceSources.paths {
		if value := strings.TrimSpace(gjson.GetBytes(payload, path).String()); value != "" {
			return value
		}
	}
	for _, key := range conversationNonceSources.header {
		if value := headerFirstValue(headers, key); value != "" {
			return value
		}
	}
	return ""
}

// ClaudeThinkingReplayConversationSessionKey returns a stable per-conversation
// key for sessionless clients. It returns usedNonce=true when an explicit
// conversation nonce (client_metadata.conversation_id, conversation_id, or
// X-Conversation-Id) was used. A nonce-based key is derived from stable
// caller fields only, so it survives history compaction and gives two
// identical openings with different nonces distinct scopes.
//
// When no nonce is present, the key falls back to the first user message and
// system prompt so replay still works for stateless clients; alias resolution
// in the executor can then recover the original conversation after history
// compaction.
func ClaudeThinkingReplayConversationSessionKey(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (string, bool) {
	if len(req.Payload) == 0 {
		return "", false
	}

	h := sha256.New()
	hashString(h, "conversation")

	if auth != nil {
		if id := strings.TrimSpace(auth.ID); id != "" {
			hashString(h, id)
		} else if apiKey, _ := claudeCredentialKey(auth); apiKey != "" {
			hashString(h, apiKey)
		} else {
			hashString(h, "")
		}
	} else {
		hashString(h, "")
	}

	hashString(h, metadataString(opts.Metadata, cliproxyexecutor.CallerScopeMetadataKey))
	hashString(h, metadataString(req.Metadata, cliproxyexecutor.CallerScopeMetadataKey))
	hashString(h, metadataString(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey))
	hashString(h, metadataString(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey))

	// Read identity headers case-insensitively so callers that supply lowercase
	// keys (e.g. x-codex-client-id) are not collapsed with missing values.
	hashString(h, headerFirstValue(opts.Headers, "User-Agent"))
	hashString(h, headerFirstValue(opts.Headers, "X-App"))
	hashString(h, headerFirstValue(opts.Headers, "X-Codex-Client-Id"))

	nonce := claudeReplayConversationNonce(req.Payload, opts.Headers)
	if nonce != "" {
		hashString(h, nonce)
		return "conversation:" + hex.EncodeToString(h.Sum(nil)[:16]), true
	}

	// No explicit nonce: fall back to the first user message and system prompt.
	// Two different callers with the same opening are still separated by the
	// caller fields above; two conversations from the same caller with the same
	// opening share a scope. Use a conversation nonce to avoid that.
	for _, path := range []string{"messages.0", "system"} {
		part := gjson.GetBytes(req.Payload, path)
		if !part.Exists() {
			hashBytes(h, nil)
			continue
		}
		if canon, ok := claudeReplayCanonicalJSON([]byte(part.Raw)); ok {
			hashBytes(h, canon)
		} else {
			hashBytes(h, []byte(part.Raw))
		}
	}
	return "conversation:" + hex.EncodeToString(h.Sum(nil)[:16]), false
}

// headerFirstValue returns the first non-empty, trimmed value for key from
// headers, matching the key case-insensitively to tolerate callers that use
// lowercase header names. Whitespace-only values are treated as missing.
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

// hashString writes s to h as a length-prefixed UTF-8 string so adjacent
// fields cannot be confused when concatenated.
func hashString(h hash.Hash, s string) {
	hashBytes(h, []byte(s))
}

// hashBytes writes b to h as a length-prefixed byte slice.
func hashBytes(h hash.Hash, b []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(b)))
	h.Write(length[:])
	h.Write(b)
}

// claudeCredentialKey returns the most identifying credential value available
// for an auth without importing the executor package.
func claudeCredentialKey(auth *cliproxyauth.Auth) (apiKey, baseURL string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		apiKey = auth.Attributes["api_key"]
		baseURL = auth.Attributes["base_url"]
	}
	return apiKey, baseURL
}

// claudeReplayCanonicalJSON returns a stable JSON encoding for the given value,
// matching the ordering used by the executor's replay match.
func claudeReplayCanonicalJSON(raw []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	canon, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	return canon, true
}
