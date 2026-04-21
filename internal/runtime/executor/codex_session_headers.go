// Package executor — codex_session_headers.go wires the Codex logical-session
// cache (helps.CodexSessionState) into the HTTP request/response pipeline so
// the proxy behaves like the native Codex CLI with respect to sticky routing.
//
// On the request side, injectCodexSessionHeaders replays the cached
// Session_id / x-codex-turn-state / x-codex-turn-metadata values — but only
// when the caller has not provided them explicitly (client-provided headers
// always win, matching codex-rs ModelClientSession behavior).
//
// On the response side, captureCodexSessionHeaders reads back the turn-state
// and turn-metadata values the upstream emitted and stores them under the
// same key so the next request of the same logical session can replay them.
package executor

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	codexHeaderSessionID    = "Session_id"
	codexHeaderTurnState    = "X-Codex-Turn-State"
	codexHeaderTurnMetadata = "X-Codex-Turn-Metadata"
)

// codexSessionKey derives the logical-session key from the authenticated
// identity and the prompt-cache ID that prepareCodexRequest computed. The
// prompt-cache ID is already stable per (api_key | user_id) pair, so pairing
// it with auth.ID gives us a key that isolates different proxy auth entries
// sharing the same upstream credential set.
//
// Returns an empty string when there is not enough information to build a
// stable key; callers must skip injection/capture in that case.
func codexSessionKey(auth *cliproxyauth.Auth, promptCacheID string) string {
	promptCacheID = strings.TrimSpace(promptCacheID)
	if promptCacheID == "" {
		return ""
	}
	authID := ""
	if auth != nil {
		authID = strings.TrimSpace(auth.ID)
	}
	if authID == "" {
		return "anon|" + promptCacheID
	}
	return authID + "|" + promptCacheID
}

// injectCodexSessionHeaders pre-populates the target request headers with
// cached session-scoped values for the given key. Client-provided headers
// (values already present on target) are never overridden — this preserves the
// Phase 1 behavior where explicit client intent always wins.
//
// Returns true when at least one header was populated from cache; useful for
// observability but not required by callers.
func injectCodexSessionHeaders(target http.Header, key string) bool {
	if target == nil || key == "" {
		return false
	}
	state, ok := helps.GetCodexSession(key)
	if !ok {
		return false
	}

	populated := false
	if state.SessionID != "" && strings.TrimSpace(target.Get(codexHeaderSessionID)) == "" {
		target.Set(codexHeaderSessionID, state.SessionID)
		populated = true
	}
	if state.TurnState != "" && strings.TrimSpace(target.Get(codexHeaderTurnState)) == "" {
		target.Set(codexHeaderTurnState, state.TurnState)
		populated = true
	}
	if state.TurnMetadata != "" && strings.TrimSpace(target.Get(codexHeaderTurnMetadata)) == "" {
		target.Set(codexHeaderTurnMetadata, state.TurnMetadata)
		populated = true
	}
	return populated
}

// captureCodexWebsocketSessionHeaders is the WebSocket analogue of
// captureCodexSessionHeaders. The logical-session key is derived from the
// "Conversation_id" header that applyCodexPromptCacheHeaders previously
// attached to the outbound handshake request, and the freshly captured
// turn-state / turn-metadata values come from the upstream's WebSocket
// handshake response headers.
//
// Both arguments may be nil/empty; the function is a no-op in that case.
func captureCodexWebsocketSessionHeaders(auth *cliproxyauth.Auth, reqHeaders http.Header, resp *http.Response) {
	if reqHeaders == nil {
		return
	}
	promptCacheID := strings.TrimSpace(reqHeaders.Get("Conversation_id"))
	if promptCacheID == "" {
		return
	}
	var respHeaders http.Header
	if resp != nil {
		respHeaders = resp.Header
	}
	sentSessionID := strings.TrimSpace(reqHeaders.Get(codexHeaderSessionID))
	captureCodexSessionHeaders(codexSessionKey(auth, promptCacheID), sentSessionID, respHeaders)
}

// captureCodexSessionHeaders writes back sticky session state discovered on
// the upstream response. It is safe to call with a nil or empty response
// header; it is a no-op in those cases. The Session_id we send upstream is
// derived from our own prompt_cache_id, so we also preserve it explicitly in
// the entry alongside the freshly captured turn-state/turn-metadata.
func captureCodexSessionHeaders(key, sentSessionID string, respHeaders http.Header) {
	if key == "" {
		return
	}
	turnState := ""
	turnMetadata := ""
	if respHeaders != nil {
		turnState = strings.TrimSpace(respHeaders.Get(codexHeaderTurnState))
		turnMetadata = strings.TrimSpace(respHeaders.Get(codexHeaderTurnMetadata))
	}
	if turnState == "" && turnMetadata == "" && sentSessionID == "" {
		return
	}
	helps.UpdateCodexSession(key, func(s *helps.CodexSessionState) {
		if sentSessionID != "" {
			s.SessionID = sentSessionID
		}
		if turnState != "" {
			s.TurnState = turnState
		}
		if turnMetadata != "" {
			s.TurnMetadata = turnMetadata
		}
	})
}
