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

// ClaudeThinkingReplayConversationSessionKey returns a stable per-conversation
// key for sessionless clients. It mixes a caller identity (credential id,
// caller-scope metadata, selected headers) with the first message, system
// prompt, and tools so two callers sharing a credential and the same initial
// prompt cannot see each other's replay state.
func ClaudeThinkingReplayConversationSessionKey(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
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

	if len(req.Payload) == 0 {
		return ""
	}
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
	return "conversation:" + hex.EncodeToString(h.Sum(nil)[:16])
}

// headerFirstValue returns the first value for key from headers, matching the
// key case-insensitively to tolerate callers that use lowercase header names.
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
