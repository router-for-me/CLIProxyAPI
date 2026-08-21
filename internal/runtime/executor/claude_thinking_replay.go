package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
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
func claudeThinkingReplayScopeFromRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) claudeThinkingReplayScope {
	sessionKey := codexReasoningReplaySessionKey(ctx, sdktranslator.FormatClaude, req, opts, req.Payload)
	sessionKey = xaiReasoningReplayIsolateSessionKey(ctx, sessionKey)
	return claudeThinkingReplayScope{
		modelFamily: claudeThinkingReplayModelFamily(auth, req.Model),
		sessionKey:  sessionKey,
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

// prepareClaudeThinkingReplayRequest restores cached assistant content into
// req.Payload before translation and sanitization. Each restored thinking part
// carries an internal _cliproxy_replay_provenance marker so the compat
// sanitizer can preserve trusted replay signatures while still rejecting
// unprovenanced client-provided signatures.
func prepareClaudeThinkingReplayRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, claudeThinkingReplayScope) {
	scope := claudeThinkingReplayScopeFromRequest(ctx, auth, req, opts)
	if !scope.valid() {
		return req, scope
	}
	contents, snapshot, found, errGet := internalcache.GetClaudeThinkingReplayWithSnapshotRequired(ctx, scope.modelFamily, scope.sessionKey)
	scope.snapshot = snapshot
	scope.cacheReady = errGet == nil
	if errGet != nil {
		log.Warnf("claude compatible thinking replay cache read failed: %v", errGet)
		return req, scope
	}
	if !found {
		return req, scope
	}
	markedContents := make([][]byte, len(contents))
	for i, content := range contents {
		markedContents[i] = claudeThinkingReplayMarkProvenance(content)
	}
	updated, restored := restoreClaudeThinkingReplayContents(req.Payload, markedContents)
	if restored {
		req.Payload = updated
		scope.replayApplied = true
	}
	return req, scope
}

// claudeThinkingReplayMarkProvenance adds a transient _cliproxy_replay_provenance
// marker to each thinking part in a cached assistant content array. The
// sanitizer uses the marker to preserve trusted replay signatures and removes
// it before the body is sent upstream.
func claudeThinkingReplayMarkProvenance(content []byte) []byte {
	root := gjson.ParseBytes(content)
	if !root.IsArray() {
		return content
	}
	parts := root.Array()
	modified := false
	outParts := make([]string, len(parts))
	for i, part := range parts {
		if strings.TrimSpace(part.Get("type").String()) == "thinking" {
			updated, _ := sjson.Set(part.Raw, "_cliproxy_replay_provenance", true)
			outParts[i] = updated
			modified = true
		} else {
			outParts[i] = part.Raw
		}
	}
	if !modified {
		return content
	}
	return []byte("[" + strings.Join(outParts, ",") + "]")
}

func restoreClaudeThinkingReplayContents(body []byte, cachedContents [][]byte) ([]byte, bool) {
	updated := body
	restored := false
	for _, cachedContent := range cachedContents {
		var restoredTurn bool
		updated, restoredTurn = restoreKimiThinkingReplayContent(updated, cachedContent)
		restored = restored || restoredTurn
	}
	return updated, restored
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
	if kimiThinkingReplayContentIsReplayable(content) {
		if _, errReplace := internalcache.ReplaceClaudeThinkingReplayIfUnchanged(ctx, scope.modelFamily, scope.sessionKey, scope.snapshot, content); errReplace != nil {
			log.Warnf("claude compatible thinking replay cache replace failed: %v", errReplace)
		}
		return
	}
	clearClaudeThinkingReplayContent(ctx, scope)
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
