package helps

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	h.Write([]byte("conversation"))

	if auth != nil {
		if id := strings.TrimSpace(auth.ID); id != "" {
			h.Write([]byte(id))
		} else if apiKey, _ := claudeCredentialKey(auth); apiKey != "" {
			h.Write([]byte(apiKey))
		}
	}

	if scope := metadataString(opts.Metadata, cliproxyexecutor.CallerScopeMetadataKey); scope != "" {
		h.Write([]byte(scope))
	}
	if scope := metadataString(req.Metadata, cliproxyexecutor.CallerScopeMetadataKey); scope != "" {
		h.Write([]byte(scope))
	}
	if derived := metadataString(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey); derived != "" {
		h.Write([]byte(derived))
	}
	if derived := metadataString(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey); derived != "" {
		h.Write([]byte(derived))
	}

	if opts.Headers != nil {
		h.Write([]byte(opts.Headers.Get("User-Agent")))
		h.Write([]byte(opts.Headers.Get("X-App")))
		h.Write([]byte(opts.Headers.Get("X-Codex-Client-Id")))
	}

	if len(req.Payload) == 0 {
		return ""
	}
	for _, path := range []string{"messages.0", "system", "tools"} {
		part := gjson.GetBytes(req.Payload, path)
		if !part.Exists() {
			continue
		}
		if canon, ok := claudeReplayCanonicalJSON([]byte(part.Raw)); ok {
			h.Write(canon)
		} else {
			h.Write([]byte(part.Raw))
		}
	}
	return "conversation:" + hex.EncodeToString(h.Sum(nil)[:16])
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
