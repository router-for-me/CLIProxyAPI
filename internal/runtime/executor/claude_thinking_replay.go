package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
// When no caller session is available, we fall back to a credential-scoped key
// so a standard Claude Messages client that provides no session metadata can
// still replay same-upstream signatures for the same credential.
func claudeThinkingReplayScopeFromRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) claudeThinkingReplayScope {
	sessionKey := codexReasoningReplaySessionKey(ctx, sdktranslator.FormatClaude, req, opts, req.Payload)
	if sessionKey != "" {
		sessionKey = xaiReasoningReplayIsolateSessionKey(ctx, sessionKey)
	}
	if sessionKey == "" {
		sessionKey = claudeThinkingReplayCredentialSessionKey(auth)
	}
	return claudeThinkingReplayScope{
		modelFamily: claudeThinkingReplayModelFamily(auth, req.Model),
		sessionKey:  sessionKey,
	}
}

func claudeThinkingReplayCredentialSessionKey(auth *cliproxyauth.Auth) string {
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
		return ""
	}
	sum := sha256.Sum256([]byte(identity))
	return "credential:" + hex.EncodeToString(sum[:8])
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
