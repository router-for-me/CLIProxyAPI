package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"

	"github.com/gin-gonic/gin"
)

func resolveIncomingClaudeHeaders(ctx context.Context, incoming http.Header) http.Header {
	resolved := make(http.Header)
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		resolved = ginCtx.Request.Header.Clone()
	}
	for key, values := range incoming {
		resolved[key] = append([]string(nil), values...)
	}
	return resolved
}

func detectIncomingClaudeCodeRequest(ctx context.Context, incoming http.Header, payload []byte, countTokens bool, cfg *config.Config) (http.Header, helps.ClaudeCodeRequestDetection) {
	resolved := resolveIncomingClaudeHeaders(ctx, incoming)
	return resolved, helps.DetectClaudeCodeRequest(resolved, payload, countTokens, cfg)
}

// getWorkloadFromContext extracts workload identifier from the gin request headers.
func getWorkloadFromContext(ctx context.Context) string {
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return strings.TrimSpace(ginCtx.GetHeader("X-CPA-Claude-Workload"))
	}
	return ""
}

// getCloakConfigFromAuth extracts cloak configuration from the auth's attributes,
// falling back to its stored metadata (the raw OAuth/token JSON). Returns
// (cloakMode, strictMode, sensitiveWords, cacheUserID); an empty cloakMode means
// the credential did not explicitly configure a mode.
func getCloakConfigFromAuth(auth *cliproxyauth.Auth) (cloakMode string, strictMode bool, sensitiveWords []string, cacheUserID bool) {
	if auth == nil {
		return "", false, nil, false
	}

	// lookupCloakAttr prefers the executor-facing Attributes, then falls back to the
	// raw metadata blob (e.g. the OAuth/token JSON) so file-based credentials can
	// carry cloak settings without a matching claude-api-key config entry.
	lookupCloakAttr := func(key string) string {
		if auth.Attributes != nil {
			if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
				return value
			}
		}
		if value := claudeauth.ReadMetadataString(&auth.Metadata, key); value != "" {
			return strings.TrimSpace(value)
		}
		return ""
	}

	// An empty cloakMode means this credential did not explicitly configure a mode,
	// allowing the caller to fall back to the global/default behavior.
	cloakMode = lookupCloakAttr("cloak_mode")

	strictMode = strings.EqualFold(lookupCloakAttr("cloak_strict_mode"), "true")

	if wordsStr := lookupCloakAttr("cloak_sensitive_words"); wordsStr != "" {
		sensitiveWords = strings.Split(wordsStr, ",")
		for i := range sensitiveWords {
			sensitiveWords[i] = strings.TrimSpace(sensitiveWords[i])
		}
	}

	cacheUserID = strings.EqualFold(lookupCloakAttr("cloak_cache_user_id"), "true")

	return cloakMode, strictMode, sensitiveWords, cacheUserID
}

// injectFakeUserID generates and injects a fake user ID into the request metadata.
// When useCache is false, a new user ID is generated for every call.
func injectFakeUserID(ctx context.Context, payload []byte, apiKey string, useCache bool) ([]byte, error) {
	doc := ojson.NewDoc(payload)
	changed, errInject := injectFakeUserIDDoc(ctx, doc, apiKey, useCache)
	if errInject != nil {
		return nil, errInject
	}
	if !changed {
		return payload, nil
	}
	return doc.Bytes(), nil
}

func injectFakeUserIDDoc(ctx context.Context, doc *ojson.Doc, apiKey string, useCache bool) (bool, error) {
	if doc == nil {
		return false, nil
	}
	generateID := func() (string, error) {
		if useCache {
			return helps.CachedUserIDRequired(ctx, apiKey)
		}
		sessionID, errSessionID := helps.CachedSessionIDRequired(ctx, apiKey)
		if errSessionID != nil {
			return "", errSessionID
		}
		return helps.GenerateFakeUserIDWithSessionID(sessionID), nil
	}

	metadata := doc.Get("metadata")
	if !metadata.Exists() {
		userID, errUserID := generateID()
		if errUserID != nil {
			return false, errUserID
		}
		_ = doc.Set("metadata.user_id", userID)
		return true, nil
	}

	existingUserID := doc.Get("metadata.user_id").String()
	if existingUserID == "" || !helps.IsValidUserID(existingUserID) {
		userID, errUserID := generateID()
		if errUserID != nil {
			return false, errUserID
		}
		_ = doc.Set("metadata.user_id", userID)
		return true, nil
	}
	return false, nil
}

// fingerprintSalt is the salt used by Claude Code to compute the 3-char build fingerprint.
const fingerprintSalt = "59cf53e54c78"

// computeFingerprint computes the 3-char build fingerprint that Claude Code embeds in cc_version.
// Algorithm: SHA256(salt + messageText[4] + messageText[7] + messageText[20] + version)[:3].
// JavaScript indexes UTF-16 code units; an isolated surrogate becomes U+FFFD
// when Node encodes the sampled string as UTF-8 for hashing.
func computeFingerprint(messageText, version string) string {
	units := utf16.Encode([]rune(messageText))
	var sampled [3]uint16
	for i, idx := range [...]int{4, 7, 20} {
		sampled[i] = '0'
		if idx < len(units) {
			sampled[i] = units[idx]
		}
	}
	input := fingerprintSalt + string(utf16.Decode(sampled[:])) + version
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:3]
}

// generateBillingHeader creates the x-anthropic-billing-header text block that
// Claude Code prepends to its system prompt. cch is present only on signed paths.
func generateBillingHeader(cchSigning bool, version, messageText, entrypoint, workload string, isSubagent bool, prevReq, promptID string, turnOrigin ...string) string {
	if entrypoint == "" {
		entrypoint = "cli"
	}
	buildHash := computeFingerprint(messageText, version)
	var b strings.Builder
	b.WriteString("x-anthropic-billing-header: cc_version=")
	b.WriteString(version)
	b.WriteByte('.')
	b.WriteString(buildHash)
	b.WriteString("; cc_entrypoint=")
	b.WriteString(entrypoint)
	b.WriteByte(';')

	if cchSigning {
		b.WriteString(" cch=00000;")
	}
	if workload != "" {
		b.WriteString(" cc_workload=")
		b.WriteString(workload)
		b.WriteByte(';')
	}
	if isSubagent {
		b.WriteString(" cc_is_subagent=true;")
	}
	if cchSigning {
		if prevReq != "" {
			b.WriteString(" cc_prev_req=")
			b.WriteString(prevReq)
			b.WriteByte(';')
		}
		if promptID != "" {
			b.WriteString(" cc_prompt_id=")
			b.WriteString(promptID)
			b.WriteByte(';')
		}
		if len(turnOrigin) > 0 && turnOrigin[0] == "human" {
			b.WriteString(" cc_turn_origin=human;")
		}
	}
	return b.String()
}

func resolveClaudeContinuityTags(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	incomingHeaders http.Header,
	payload []byte,
	confirmedClaudeCode bool,
	existingPrevReq, existingPromptID string,
) (prevReq, promptID string, cCtx helps.ClaudeContinuityContext, ok bool) {
	hasExecutionMetadata := helps.ClaudeExecutionMetadataFromContext(ctx)
	sessionID := helps.ClaudeSessionIDFromContext(ctx)
	if sessionID == "" && auth != nil {
		sessionID = helps.ClaudeAgentSessionUUIDForRequest(incomingHeaders, payload, payload, confirmedClaudeCode)
	}
	if sessionID == "" || auth == nil {
		return "", "", helps.ClaudeContinuityContext{}, false
	}

	credIdentity := claudeDiagnosticsCredentialIdentity(auth)
	isNewTurn := helps.IsClaudeNewPromptTurn(payload)
	continuityKey, seq, prevMsgID, storedPrevReq, storedPromptID := helps.BeginClaudeContinuity(credIdentity, sessionID, isNewTurn, existingPromptID)

	// The currentDate reminder is pinned to the session on its first request so
	// a local-midnight flip between requests cannot rewrite it and invalidate
	// the prompt-cache prefix in front of the whole conversation.
	pinnedDate := resolveClaudeSessionPinnedDate(cfg, auth, payload, continuityKey, sessionID)

	if existingPromptID != "" {
		promptID = existingPromptID
	} else if prevMsgID != "" && storedPromptID != "" && (hasExecutionMetadata || !isNewTurn) {
		promptID = storedPromptID
	} else if !hasExecutionMetadata {
		promptID = helps.ClaudeDeterministicPromptID("cpa:prompt:" + claudeBillingFingerprintMessageText(payload))
	} else {
		promptID = storedPromptID
	}

	if (hasExecutionMetadata || existingPrevReq != "") && storedPrevReq != "" {
		prevReq = storedPrevReq
	} else {
		prevReq = existingPrevReq
	}

	cCtx = helps.ClaudeContinuityContext{
		Key:         continuityKey,
		Sequence:    seq,
		PromptID:    promptID,
		PinnedDate:  pinnedDate,
		Initialized: true,
	}
	if hasExecutionMetadata || existingPrevReq != "" {
		cCtx.PreviousMessageID = prevMsgID
		cCtx.PreviousRequestID = storedPrevReq
	} else {
		cCtx.PreviousMessageID = ""
		cCtx.PreviousRequestID = ""
	}
	return prevReq, promptID, cCtx, true
}

func resolveClaudeContinuityTagsDoc(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	incomingHeaders http.Header,
	doc *ojson.Doc,
	confirmedClaudeCode bool,
	existingPrevReq, existingPromptID string,
) (prevReq, promptID string, cCtx helps.ClaudeContinuityContext, ok bool) {
	if doc == nil {
		return "", "", helps.ClaudeContinuityContext{}, false
	}
	hasExecutionMetadata := helps.ClaudeExecutionMetadataFromContext(ctx)
	sessionID := helps.ClaudeSessionIDFromContext(ctx)
	if sessionID == "" && auth != nil {
		payload := doc.Bytes()
		sessionID = helps.ClaudeAgentSessionUUIDForRequest(incomingHeaders, payload, payload, confirmedClaudeCode)
	}
	if sessionID == "" || auth == nil {
		return "", "", helps.ClaudeContinuityContext{}, false
	}

	credIdentity := claudeDiagnosticsCredentialIdentity(auth)
	isNewTurn := isClaudeNewPromptTurnDoc(doc)
	continuityKey, seq, prevMsgID, storedPrevReq, storedPromptID := helps.BeginClaudeContinuity(credIdentity, sessionID, isNewTurn, existingPromptID)

	pinnedDate := resolveClaudeSessionPinnedDateDoc(cfg, auth, doc, continuityKey, sessionID)

	if existingPromptID != "" {
		promptID = existingPromptID
	} else if prevMsgID != "" && storedPromptID != "" && (hasExecutionMetadata || !isNewTurn) {
		promptID = storedPromptID
	} else if !hasExecutionMetadata {
		promptID = helps.ClaudeDeterministicPromptID("cpa:prompt:" + claudeBillingFingerprintMessageTextDoc(doc))
	} else {
		promptID = storedPromptID
	}

	if (hasExecutionMetadata || existingPrevReq != "") && storedPrevReq != "" {
		prevReq = storedPrevReq
	} else {
		prevReq = existingPrevReq
	}

	cCtx = helps.ClaudeContinuityContext{
		Key:         continuityKey,
		Sequence:    seq,
		PromptID:    promptID,
		PinnedDate:  pinnedDate,
		Initialized: true,
	}
	if hasExecutionMetadata || existingPrevReq != "" {
		cCtx.PreviousMessageID = prevMsgID
		cCtx.PreviousRequestID = storedPrevReq
	} else {
		cCtx.PreviousMessageID = ""
		cCtx.PreviousRequestID = ""
	}
	return prevReq, promptID, cCtx, true
}

func claudeBillingFingerprintMessageText(payload []byte) string {
	idx := firstClaudeUserMessageIndex(payload)
	if idx < 0 {
		return ""
	}
	content := ojson.GetBytesNoCopy(payload, fmt.Sprintf("messages.%d.content", idx))
	if content.Type == ojson.String {
		return content.String()
	}
	if content.IsArray() {
		messageText := ""
		content.ForEach(func(_, part ojson.Result) bool {
			if part.Get("type").String() == "text" {
				text := part.Get("text").String()
				if !isClaudeCodeCurrentDateReminder(text) && !isClaudeCodeContextReminder(text) {
					messageText = text
					return false
				}
			}
			return true
		})
		return messageText
	}
	return ""
}

func claudeBillingFingerprintMessageTextDoc(doc *ojson.Doc) string {
	if doc == nil {
		return ""
	}
	idx := firstClaudeUserMessageIndexDoc(doc)
	if idx < 0 {
		return ""
	}
	content := doc.Get(fmt.Sprintf("messages.%d.content", idx))
	if content.Type == ojson.String {
		return content.String()
	}
	if content.IsArray() {
		messageText := ""
		content.ForEach(func(_, part ojson.Result) bool {
			if part.Get("type").String() == "text" {
				text := part.Get("text").String()
				if !isClaudeCodeCurrentDateReminder(text) && !isClaudeCodeContextReminder(text) {
					messageText = text
					return false
				}
			}
			return true
		})
		return messageText
	}
	return ""
}

func claudeCCHFallbackBillingHeader(ctx context.Context, cfg *config.Config, payload []byte, entrypoint string) string {
	isProbeOrHelper := helps.IsClaudeProbeOrHelperRequest(payload)
	prevReq, promptID := helps.ExtractClaudeBillingTags(payload)
	if !isProbeOrHelper {
		continuityCtx := helps.ClaudeContinuityContextFromContext(ctx)
		if prevReq == "" && continuityCtx != nil {
			prevReq = continuityCtx.PreviousRequestID
		}
		if promptID == "" && continuityCtx != nil {
			promptID = continuityCtx.PromptID
		}
	}
	incomingHeaders := resolveIncomingClaudeHeaders(ctx, helps.IncomingHeadersFromContext(ctx))
	isSubagent := helps.IsClaudeSubagentRequest(incomingHeaders, payload)
	return generateBillingHeader(
		true,
		helps.DefaultClaudeVersion(cfg),
		claudeBillingFingerprintMessageText(payload),
		entrypoint,
		getWorkloadFromContext(ctx),
		isSubagent,
		prevReq,
		promptID,
	)
}

func claudeCCHFallbackBillingHeaderDoc(ctx context.Context, cfg *config.Config, doc *ojson.Doc, entrypoint string) string {
	isProbeOrHelper := isClaudeProbeOrHelperRequestDoc(doc)
	prevReq, promptID := extractClaudeBillingTagsDoc(doc)
	if !isProbeOrHelper {
		continuityCtx := helps.ClaudeContinuityContextFromContext(ctx)
		if prevReq == "" && continuityCtx != nil {
			prevReq = continuityCtx.PreviousRequestID
		}
		if promptID == "" && continuityCtx != nil {
			promptID = continuityCtx.PromptID
		}
	}
	incomingHeaders := resolveIncomingClaudeHeaders(ctx, helps.IncomingHeadersFromContext(ctx))
	isSubagent := isClaudeSubagentRequestDoc(incomingHeaders, doc)
	return generateBillingHeader(
		true,
		helps.DefaultClaudeVersion(cfg),
		claudeBillingFingerprintMessageTextDoc(doc),
		entrypoint,
		getWorkloadFromContext(ctx),
		isSubagent,
		prevReq,
		promptID,
	)
}

func isClaudeProbeOrHelperRequestDoc(doc *ojson.Doc) bool {
	return isClaudeProbeRequestDoc(doc) || isClaudeTitleHelperRequestDoc(doc)
}

func isClaudeProbeRequestDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	maxTokens := doc.Get("max_tokens")
	if !maxTokens.Exists() || maxTokens.Int() != 1 {
		return false
	}
	tools := doc.Get("tools")
	if tools.Exists() && len(tools.Array()) > 0 {
		return false
	}
	messages := doc.Get("messages")
	if !messages.Exists() || !messages.IsArray() || len(messages.Array()) == 0 {
		return true
	}
	arr := messages.Array()
	if len(arr) != 1 {
		return false
	}
	firstMsg := arr[0]
	if firstMsg.Get("role").String() != "user" {
		return false
	}
	content := firstMsg.Get("content")
	if content.Type == ojson.String {
		str := strings.TrimSpace(content.String())
		return str == "quota" || str == "test" || str == "." || str == "probe"
	}
	if content.IsArray() {
		nonReminderCount := 0
		matched := false
		for _, part := range content.Array() {
			t := strings.TrimSpace(part.Get("text").String())
			if strings.Contains(t, "<system-reminder>") {
				continue
			}
			nonReminderCount++
			if t == "quota" || t == "test" || t == "." || t == "probe" || (t == "Hi" && part.Get("cache_control").Exists()) {
				matched = true
			}
		}
		return nonReminderCount == 1 && matched
	}
	return false
}

func isClaudeTitleHelperInstructionDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	matchesTitlePrompt := func(t string) bool {
		return strings.Contains(t, "Return a short title") ||
			strings.Contains(t, "naming a coding session") ||
			strings.Contains(t, "Write the title in the predominant language") ||
			strings.Contains(t, "<session>")
	}
	system := doc.Get("system")
	if system.IsArray() {
		for _, part := range system.Array() {
			if matchesTitlePrompt(part.Get("text").String()) {
				return true
			}
		}
	} else if matchesTitlePrompt(system.String()) {
		return true
	}
	messages := doc.Get("messages")
	if messages.IsArray() {
		for _, msg := range messages.Array() {
			content := msg.Get("content")
			if content.IsArray() {
				for _, part := range content.Array() {
					if matchesTitlePrompt(part.Get("text").String()) {
						return true
					}
				}
			} else if matchesTitlePrompt(content.String()) {
				return true
			}
		}
	}
	return false
}

func isClaudeTitleHelperRequestDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	props := doc.Get("output_config.format.schema.properties")
	if props.Exists() {
		if props.Get("title").Exists() && len(props.Map()) == 1 {
			return isClaudeTitleHelperInstructionDoc(doc)
		}
		return false
	}
	matchesSystemTitleInstruction := func(t string) bool {
		return strings.Contains(t, "naming a coding session") ||
			strings.Contains(t, "Return a short title") ||
			strings.Contains(t, "Write the title in the predominant language")
	}
	system := doc.Get("system")
	if system.IsArray() {
		for _, part := range system.Array() {
			if matchesSystemTitleInstruction(part.Get("text").String()) {
				return true
			}
		}
	} else if matchesSystemTitleInstruction(system.String()) {
		return true
	}
	messages := doc.Get("messages")
	if messages.IsArray() {
		for _, msg := range messages.Array() {
			if msg.Get("role").String() == "system" {
				content := msg.Get("content")
				if content.IsArray() {
					for _, part := range content.Array() {
						if matchesSystemTitleInstruction(part.Get("text").String()) {
							return true
						}
					}
				} else if matchesSystemTitleInstruction(content.String()) {
					return true
				}
			}
		}
	}
	return false
}

func isClaudeNewPromptTurnDoc(doc *ojson.Doc) bool {
	if doc == nil || isClaudeProbeOrHelperRequestDoc(doc) {
		return false
	}
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return true
	}
	arr := messages.Array()
	if len(arr) == 0 {
		return true
	}
	lastMsg := arr[len(arr)-1]
	if lastMsg.Get("role").String() != "user" {
		return false
	}
	content := lastMsg.Get("content")
	if content.IsArray() {
		for _, part := range content.Array() {
			if part.Get("type").String() == "tool_result" {
				return false
			}
		}
	}
	return true
}

func isClaudeSubagentRequestDoc(headers http.Header, doc *ojson.Doc) bool {
	if val := helps.HeaderValueCaseInsensitive(headers, "X-Claude-Code-Agent-Id"); val != "" {
		return true
	}
	if val := helps.HeaderValueCaseInsensitive(headers, "X-Claude-Code-Parent-Agent-Id"); val != "" {
		return true
	}
	if doc == nil {
		return false
	}
	if doc.Get("metadata.user_id.parent_session_id").Exists() {
		return true
	}
	if userID := doc.Get("metadata.user_id").String(); userID != "" {
		if strings.Contains(userID, `"parent_session_id"`) {
			return true
		}
	}
	system := doc.Get("system")
	if system.IsArray() && len(system.Array()) > 0 {
		if strings.Contains(system.Array()[0].Get("text").String(), "cc_is_subagent=true") {
			return true
		}
	} else if system.Type == ojson.String && strings.Contains(system.String(), "cc_is_subagent=true") {
		return true
	}
	return false
}

func claudePayloadHas1hTTLDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	has1h := false
	forEachClaudeCacheControlPathDoc(doc, func(_ string, cc ojson.Result) bool {
		if cc.IsObject() && cc.Get("ttl").String() == "1h" {
			has1h = true
			return false
		}
		return true
	})
	return has1h
}

func claudeSubagentRequests1hDoc(headers http.Header, doc *ojson.Doc) bool {
	if claudePayloadHas1hTTLDoc(doc) {
		return true
	}
	betas := strings.Join(helps.HeaderValuesCaseInsensitive(headers, "Anthropic-Beta"), ",")
	return strings.Contains(betas, "extended-cache-ttl-2025-04-11")
}

func extractClaudeBillingTagsDoc(doc *ojson.Doc) (prevReq, promptID string) {
	if doc == nil {
		return "", ""
	}
	system := doc.Get("system")
	var billingText string
	if system.IsArray() && len(system.Array()) > 0 {
		billingText = system.Array()[0].Get("text").String()
	} else if system.Type == ojson.String {
		billingText = system.String()
	}
	if billingText == "" || !strings.HasPrefix(billingText, "x-anthropic-billing-header:") {
		return "", ""
	}
	if idx := strings.Index(billingText, "cc_prev_req="); idx >= 0 {
		val := billingText[idx+len("cc_prev_req="):]
		if end := strings.IndexByte(val, ';'); end >= 0 {
			val = val[:end]
		}
		if val != "" && !strings.ContainsAny(val, " \t\r\n;") {
			prevReq = strings.Clone(val)
		}
	}
	if idx := strings.Index(billingText, "cc_prompt_id="); idx >= 0 {
		val := billingText[idx+len("cc_prompt_id="):]
		if end := strings.IndexByte(val, ';'); end >= 0 {
			val = val[:end]
		}
		if helps.IsValidClaudePromptID(val) {
			promptID = strings.Clone(strings.ToLower(val))
		}
	}
	return prevReq, promptID
}

const claudeCodeCLIIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

const claudeCodeFableReportingOutcomes = `# Reporting outcomes

Report what actually happened, not what you intended. When you say something is done, sent, saved, fixed, or verified, that claim must rest on a result you observed in this session — tool output, the file as it now reads, the page as it now loads — not on what the step should have produced. If you did not check, say you did not check. If any step failed, was skipped, or came back different from what you expected, say so in the first sentence of your report, before anything else, even when the rest of the work succeeded. Never quietly work around a failure in a way that makes it look resolved; a problem the user can see is recoverable, one your summary hides is not. When you stop before the task is complete, your first line says so plainly and names what is left. Do not describe partial work as done, and do not let a summary read as more certain than the evidence behind it.`

func checkSystemInstructionsWithMode(payload []byte, strictMode bool, keepCallerSystemTopLevel ...bool) []byte {
	keepAtTopLevel := len(keepCallerSystemTopLevel) > 0 && keepCallerSystemTopLevel[0]
	return checkSystemInstructionsWithSigningMode(payload, strictMode, false, "2.1.280", "cli", "", keepAtTopLevel)
}

func checkSystemInstructionsWithModeDoc(doc *ojson.Doc, strictMode bool, keepCallerSystemTopLevel ...bool) bool {
	keepAtTopLevel := len(keepCallerSystemTopLevel) > 0 && keepCallerSystemTopLevel[0]
	return checkSystemInstructionsWithSigningModeDoc(doc, strictMode, false, "2.1.280", "cli", "", keepAtTopLevel)
}

// checkSystemInstructionsWithSigningMode keeps the top-level system in Claude
// Code's minimal CLI shape. Each caller system block is preserved as a separate
// mid-conversation system message after the first user turn, where supported
// Claude models give it operator-level authority without changing the cached
// top-level prefix.
func checkSystemInstructionsWithSigningMode(payload []byte, strictMode bool, cchSigning bool, version, entrypoint, workload string, keepCallerSystemTopLevel ...bool) []byte {
	keepAtTopLevel := len(keepCallerSystemTopLevel) > 0 && keepCallerSystemTopLevel[0]
	return checkSystemInstructionsWithSigningModeAt(payload, strictMode, cchSigning, version, entrypoint, workload, claudeCodeLocalDate(time.Now()), false, "", "", keepAtTopLevel, "")
}

func checkSystemInstructionsWithSigningModeDoc(doc *ojson.Doc, strictMode bool, cchSigning bool, version, entrypoint, workload string, keepCallerSystemTopLevel ...bool) bool {
	keepAtTopLevel := len(keepCallerSystemTopLevel) > 0 && keepCallerSystemTopLevel[0]
	return checkSystemInstructionsWithSigningModeAtDoc(doc, strictMode, cchSigning, version, entrypoint, workload, claudeCodeLocalDate(time.Now()), false, "", "", keepAtTopLevel, "")
}

// isClaudeFable51Model reports whether the model is specifically Fable 5.1 / Mythos 5.1,
// matching native Claude Code 2.1.258 family/major/minor checks (AFo = {major:5, minor:1}).
func isClaudeFable51Model(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, target := range []string{"fable-5-1", "fable-5.1", "mythos-5-1", "mythos-5.1"} {
		idx := strings.Index(m, target)
		if idx != -1 {
			nextIdx := idx + len(target)
			if nextIdx >= len(m) || m[nextIdx] < '0' || m[nextIdx] > '9' {
				return true
			}
		}
	}
	return false
}

type forwardedClaudeSystemPromptBlock struct {
	Text         string
	CacheControl *claudeCacheControl
	RawCC        string
}

func buildForwardedSystemBlock(text string, block forwardedClaudeSystemPromptBlock, isExplicit bool) string {
	if isExplicit {
		if block.RawCC != "" {
			return `{"type":"text","text":` + marshalJSONStringWithoutHTMLEscape(text) + `,"cache_control":` + block.RawCC + `}`
		}
		return buildTextBlock(text, nil)
	}
	return buildTextBlock(text, &claudeCodeCacheControl)
}

func isValidClaudeCacheControl(cc ojson.Result) bool {
	if !cc.Exists() || cc.Type == ojson.Null || !cc.IsObject() {
		return false
	}
	typ := cc.Get("type")
	return typ.Type == ojson.String && typ.String() == "ephemeral"
}

func collectForwardedClaudeSystemPromptBlocksWithCache(system ojson.Result) []forwardedClaudeSystemPromptBlock {
	var blocks []forwardedClaudeSystemPromptBlock
	appendBlock := func(text string, cc *claudeCacheControl, rawCC string) {
		if strings.TrimSpace(text) == "" || util.IsClaudeCodeAttributionSystemText(text) || isClaudeCodeIdentitySystemText(text) {
			return
		}
		blocks = append(blocks, forwardedClaudeSystemPromptBlock{Text: text, CacheControl: cc, RawCC: rawCC})
	}

	if system.Type == ojson.String {
		appendBlock(system.String(), nil, "")
		return blocks
	}
	if !system.IsArray() {
		return blocks
	}
	system.ForEach(func(_, item ojson.Result) bool {
		if item.Get("type").String() == "text" {
			var cc *claudeCacheControl
			rawCC := ""
			if itemCC := item.Get("cache_control"); isValidClaudeCacheControl(itemCC) {
				cc = &claudeCacheControl{
					Type:  itemCC.Get("type").String(),
					TTL:   itemCC.Get("ttl").String(),
					Scope: itemCC.Get("scope").String(),
				}
				rawCC = itemCC.Raw
			}
			appendBlock(item.Get("text").String(), cc, rawCC)
		}
		return true
	})
	return blocks
}

func checkSystemInstructionsWithSigningModeAt(
	payload []byte,
	strictMode bool,
	cchSigning bool,
	version, entrypoint, workload string,
	currentDate string,
	isSubagent bool,
	prevReq, promptID string,
	keepCallerSystemTopLevel bool,
	turnOrigin ...string,
) []byte {
	doc := ojson.NewDoc(payload)
	if !checkSystemInstructionsWithSigningModeAtDoc(doc, strictMode, cchSigning, version, entrypoint, workload, currentDate, isSubagent, prevReq, promptID, keepCallerSystemTopLevel, turnOrigin...) {
		return payload
	}
	return doc.Bytes()
}

func checkSystemInstructionsWithSigningModeAtDoc(
	doc *ojson.Doc,
	strictMode bool,
	cchSigning bool,
	version, entrypoint, workload string,
	currentDate string,
	isSubagent bool,
	prevReq, promptID string,
	keepCallerSystemTopLevel bool,
	turnOrigin ...string,
) bool {
	if doc == nil {
		return false
	}
	system := doc.Get("system")
	messageText := claudeBillingFingerprintMessageTextDoc(doc)

	billingText := generateBillingHeader(cchSigning, version, messageText, entrypoint, workload, isSubagent, prevReq, promptID, turnOrigin...)
	billingBlock := buildTextBlock(billingText, nil)

	isExplicit := (len(turnOrigin) > 1 && turnOrigin[1] == "explicit_cache_mode") || isExplicitPromptCacheModeDoc(doc)
	var agentCC *claudeCacheControl = &claudeCodeCacheControl
	if isExplicit {
		agentCC = nil
	}
	agentBlock := buildTextBlock(claudeCodeCLIIdentity, agentCC)

	systemBlocks := []string{billingBlock, agentBlock}
	model := strings.ToLower(strings.TrimSpace(doc.Get("model").String()))
	if isClaudeFable51Model(model) && !isClaudeProbeOrHelperRequestDoc(doc) {
		systemBlocks = append(systemBlocks, buildTextBlock(claudeCodeFableReportingOutcomes, nil))
	}
	_ = doc.SetRawBytes("system", []byte("["+strings.Join(systemBlocks, ",")+"]"))
	if strictMode {
		injectClaudeCodeCurrentDateInternalDoc(doc, currentDate, isExplicit)
		return true
	}

	forwardedSystemBlocks := collectForwardedClaudeSystemPromptBlocksWithCache(system)
	if len(forwardedSystemBlocks) == 0 {
		injectClaudeCodeCurrentDateInternalDoc(doc, currentDate, isExplicit)
		return true
	}
	if claudeHistoryHasAdvisorCallOrResultDoc(doc) {
		for _, block := range forwardedSystemBlocks {
			systemBlocks = append(systemBlocks, buildForwardedSystemBlock(block.Text, block, isExplicit))
		}
		_ = doc.SetRawBytes("system", []byte("["+strings.Join(systemBlocks, ",")+"]"))
		injectClaudeCodeCurrentDateInternalDoc(doc, currentDate, isExplicit)
		return true
	}
	if claudeUsesLegacySystemReminderDoc(doc) {
		prependClaudeSystemReminderBlocksToFirstUserMessageDoc(doc, forwardedSystemBlocks, isExplicit)
	} else if keepCallerSystemTopLevel && claudeMidConversationSystemMessagesAtEndDoc(doc) {
		for _, block := range forwardedSystemBlocks {
			systemBlocks = append(systemBlocks, buildForwardedSystemBlock(block.Text, block, isExplicit))
		}
		_ = doc.SetRawBytes("system", []byte("["+strings.Join(systemBlocks, ",")+"]"))
	} else {
		insertClaudeMidConversationSystemBlocksDoc(doc, forwardedSystemBlocks, isExplicit)
	}
	injectClaudeCodeCurrentDateInternalDoc(doc, currentDate, isExplicit)
	return true
}

// relocateClaudeSystemPromptForCountTokens keeps a cloaked count_tokens request
// in Claude Code's measured shape, which carries only model, messages and tools.
// The Claude Code system blocks are therefore not installed here, but each caller
// system block still has to be accounted for, so it is relocated into messages
// using the same positional mapping as the Messages path. That keeps the counted
// tokens aligned with the request the caller is about to send while preventing a
// third-party system prompt from reaching Anthropic in the system slot.
func relocateClaudeSystemPromptForCountTokens(payload []byte, strictMode bool, explicitCacheMode ...bool) []byte {
	isExplicit := (len(explicitCacheMode) > 0 && explicitCacheMode[0]) || isExplicitPromptCacheMode(payload)
	return relocateClaudeSystemPromptForCountTokensWithPolicy(payload, strictMode, isExplicit, false)
}

func relocateClaudeSystemPromptForCountTokensWithPolicy(payload []byte, strictMode, explicitCacheMode, keepCallerSystemTopLevel bool) []byte {
	doc := ojson.NewDoc(payload)
	if !relocateClaudeSystemPromptForCountTokensWithPolicyDoc(doc, strictMode, explicitCacheMode, keepCallerSystemTopLevel) {
		return payload
	}
	return doc.Bytes()
}

func relocateClaudeSystemPromptForCountTokensWithPolicyDoc(doc *ojson.Doc, strictMode, explicitCacheMode, keepCallerSystemTopLevel bool) bool {
	if doc == nil {
		return false
	}
	system := doc.Get("system")
	if !system.Exists() {
		return false
	}
	isExplicit := explicitCacheMode
	// Strict mode drops caller prompts on the Messages path, so it must not
	// reintroduce them here either.
	var forwardedSystemBlocks []forwardedClaudeSystemPromptBlock
	if !strictMode {
		forwardedSystemBlocks = collectForwardedClaudeSystemPromptBlocksWithCache(system)
	}
	if len(forwardedSystemBlocks) == 0 {
		return doc.Delete("system") == nil
	}
	if claudeHistoryHasAdvisorCallOrResultDoc(doc) {
		// When an advisor call or result is present, the Messages path preserves
		// caller system blocks in top-level system to prevent shifting message
		// positions. Keep them in top-level system here as well so token counting
		// remains aligned with the Messages path without splicing messages[].
		blocks := make([]string, 0, len(forwardedSystemBlocks))
		for _, block := range forwardedSystemBlocks {
			blocks = append(blocks, buildForwardedSystemBlock(block.Text, block, isExplicit))
		}
		return doc.SetRawBytes("system", []byte("["+strings.Join(blocks, ",")+"]")) == nil
	}
	if errDelete := doc.Delete("system"); errDelete != nil {
		return false
	}
	if claudeUsesLegacySystemReminderDoc(doc) {
		prependClaudeSystemReminderBlocksToFirstUserMessageDoc(doc, forwardedSystemBlocks, isExplicit)
		return true
	}
	if keepCallerSystemTopLevel && claudeMidConversationSystemMessagesAtEndDoc(doc) {
		blocks := make([]string, 0, len(forwardedSystemBlocks))
		for _, block := range forwardedSystemBlocks {
			blocks = append(blocks, buildForwardedSystemBlock(block.Text, block, isExplicit))
		}
		_ = doc.SetRawBytes("system", []byte("["+strings.Join(blocks, ",")+"]"))
		return true
	}
	insertClaudeMidConversationSystemBlocksDoc(doc, forwardedSystemBlocks, isExplicit)
	return true
}

// claudeLegacySystemReminderModels lists the official Anthropic model IDs and
// aliases that reject a mid-conversation role=system message. Entries mirror the
// "claude" provider in internal/registry/models/models.json plus Anthropic's own
// bare and "-latest" aliases. Other providers' synthetic IDs do not belong here.
var claudeLegacySystemReminderModels = map[string]struct{}{
	"claude-3-5-haiku-20241022":  {},
	"claude-3-5-haiku-latest":    {},
	"claude-3-7-sonnet-20250219": {},
	"claude-3-7-sonnet-latest":   {},
	"claude-haiku-4-5":           {},
	"claude-haiku-4-5-20251001":  {},
	"claude-opus-4":              {},
	"claude-opus-4-20250514":     {},
	"claude-opus-4-1":            {},
	"claude-opus-4-1-20250805":   {},
	"claude-opus-4-5":            {},
	"claude-opus-4-5-20251101":   {},
	"claude-opus-4-6":            {},
	"claude-opus-4-7":            {},
	"claude-sonnet-4":            {},
	"claude-sonnet-4-20250514":   {},
	"claude-sonnet-4-5":          {},
	"claude-sonnet-4-5-20250929": {},
	"claude-sonnet-4-6":          {},
}

func claudeUsesLegacySystemReminder(payload []byte) bool {
	model := strings.ToLower(strings.TrimSpace(ojson.GetBytesNoCopy(payload, "model").String()))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	_, legacy := claudeLegacySystemReminderModels[model]
	return legacy
}

func claudeUsesLegacySystemReminderDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	model := strings.ToLower(strings.TrimSpace(doc.Get("model").String()))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	_, legacy := claudeLegacySystemReminderModels[model]
	return legacy
}

// claudeCallerSystemBlockError reports a caller system block that Claude cannot
// carry in any system slot. It is request-scoped: no other credential or upstream
// model can accept the same body, so the request must not be retried.
type claudeCallerSystemBlockError struct {
	statusErr
}

func (claudeCallerSystemBlockError) IsRequestScoped() bool {
	return true
}

func newClaudeCallerSystemBlockError(index int, blockType string) error {
	if blockType == "" {
		blockType = "unknown"
	}
	return claudeCallerSystemBlockError{statusErr{
		code: http.StatusBadRequest,
		msg: fmt.Sprintf("invalid_request_error: system.%d.type: Input should be 'text'. "+
			"System instructions support text only, but this block has type %q. "+
			"Move non-text content into a user message.", index, blockType),
	}}
}

// claudeMidSystemMessageModelError reports a mid-conversation
// {"role":"system"} turn addressed to a first-party model that cannot carry
// it. It is request-scoped for the same reason as claudeCallerSystemBlockError:
// the body is incompatible with the model rather than evidence of unhealthy
// credentials, so no credential should be cooled or retried.
type claudeMidSystemMessageModelError struct {
	statusErr
}

func (claudeMidSystemMessageModelError) IsRequestScoped() bool {
	return true
}

// The turn is not always the caller's. CPA normally reconciles a cloaked turn
// when a payload rule changes the model to legacy, but it deliberately gives up
// if the rule also rewrites the tracked messages and their provenance is no
// longer exact. The wording therefore states the model's requirement instead of
// assuming the caller created the turn.
func newClaudeMidSystemMessageModelError(model string) error {
	if model == "" {
		model = "unknown"
	}
	return claudeMidSystemMessageModelError{statusErr{
		code: http.StatusBadRequest,
		msg: fmt.Sprintf("invalid_request_error: role 'system' is not supported on this model. "+
			"Model %q predates mid-conversation system turns, so system instructions must "+
			"stay in the top-level system field for it.", model),
	}}
}

// validateClaudeMidSystemMessageModel rejects a request that pairs a legacy
// model with a caller's mid-conversation {"role":"system"} turn.
//
// Anthropic answers that pairing with a guaranteed rejection, verified on both
// /v1/messages and /v1/messages/count_tokens:
//
//	400 role 'system' is not supported on this model
//
// The native client never produces it either: it gates the turn on the model,
// which is also why claudeCodeCLIBetas withholds
// mid-conversation-system-2026-04-07 for these IDs. In 314 captured native
// requests the turn appears only on claude-opus-5 and claude-sonnet-5, and on
// none of the 43 requests addressed to a model in
// claudeLegacySystemReminderModels.
//
// Three conditions keep the check inside the evidence that produced it:
//
//   - firstPartyAnthropic, because the rejection was measured against
//     api.anthropic.com. A third-party gateway may map these model IDs onto
//     something that accepts the turn, and answering locally would also stop
//     failover to another credential or base URL.
//   - confirmedClaudeCode, because a client that still matches the native
//     fingerprint owns its wire. It gates the turn itself, so its body is
//     forwarded untouched and any upstream error reaches it unchanged.
//   - the pairing itself, so unknown and future model IDs stay optimistic in
//     the same way checkSystemInstructions treats them.
//
// Operators who prefer the turn folded into the system slot can still set
// rebuild_mid_system_message, which runs before this check.
//
// The error is request-scoped: the body/model pairing is invalid independently
// of first-party credential health, so no credential should be cooled or
// retried.
func validateClaudeMidSystemMessageModel(payload []byte, confirmedClaudeCode, firstPartyAnthropic bool) error {
	if confirmedClaudeCode || !firstPartyAnthropic {
		return nil
	}
	if !claudeUsesLegacySystemReminder(payload) || !claudePayloadHasMidSystemMessage(payload) {
		return nil
	}
	return newClaudeMidSystemMessageModelError(strings.Clone(ojson.GetBytesNoCopy(payload, "model").String()))
}

func validateClaudeMidSystemMessageModelDoc(doc *ojson.Doc, confirmedClaudeCode, firstPartyAnthropic bool) error {
	if doc == nil || confirmedClaudeCode || !firstPartyAnthropic {
		return nil
	}
	if !claudeUsesLegacySystemReminderDoc(doc) || !claudeDocHasMidSystemMessage(doc) {
		return nil
	}
	return newClaudeMidSystemMessageModelError(strings.Clone(doc.Get("model").String()))
}

// validateClaudeCallerSystemBlocks rejects caller system content that cannot keep
// its operator authority. Verified against api.anthropic.com on 2026-08-03: the
// top-level system field answers "system.<i>.type: Input should be 'text'" for
// image, document and unknown block types, and a role=system message answers
// "role 'system' supports text, tool_addition, and tool_removal blocks only".
// Cloaking relocates caller blocks into one of those two slots, so a non-text
// block has no destination. Failing here keeps the caller's instructions from
// being silently dropped, and costs no upstream attempt.
func validateClaudeCallerSystemBlocks(system ojson.Result) error {
	if !system.IsArray() {
		// A string system prompt is text by definition.
		return nil
	}
	var blockErr error
	index := 0
	system.ForEach(func(_, part ojson.Result) bool {
		if strings.TrimSpace(part.Get("type").String()) != "text" {
			blockErr = newClaudeCallerSystemBlockError(index, strings.TrimSpace(part.Get("type").String()))
			return false
		}
		index++
		return true
	})
	return blockErr
}

func collectForwardedClaudeSystemPromptBlocks(system ojson.Result) []string {
	var blocks []string
	appendText := func(text string) {
		if strings.TrimSpace(text) == "" || util.IsClaudeCodeAttributionSystemText(text) || text == claudeCodeCLIIdentity {
			return
		}
		blocks = append(blocks, text)
	}

	if system.IsArray() {
		system.ForEach(func(_, part ojson.Result) bool {
			if part.Get("type").String() == "text" {
				appendText(part.Get("text").String())
			}
			return true
		})
	} else if system.Type == ojson.String {
		appendText(system.String())
	}
	return blocks
}

// buildTextBlock constructs a JSON text block with JSON.stringify-compatible
// HTML characters. encoding/json's default \u003c escaping would change the
// exact currentDate bytes and therefore the final CCH.
func buildTextBlock(text string, cacheControl *claudeCacheControl) string {
	block := `{"type":"text","text":` + marshalJSONStringWithoutHTMLEscape(text)
	if cacheControl != nil && cacheControl.Type != "" {
		block += `,"cache_control":{"type":` + marshalJSONStringWithoutHTMLEscape(cacheControl.Type)
		if cacheControl.TTL != "" {
			block += `,"ttl":` + marshalJSONStringWithoutHTMLEscape(cacheControl.TTL)
		}
		if cacheControl.Scope != "" {
			block += `,"scope":` + marshalJSONStringWithoutHTMLEscape(cacheControl.Scope)
		}
		block += "}"
	}
	return block + "}"
}

func marshalJSONStringWithoutHTMLEscape(value string) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSuffix(encoded.String(), "\n")
}

func prependClaudeSystemRemindersToFirstUserMessage(payload []byte, texts []string) []byte {
	blocks := make([]forwardedClaudeSystemPromptBlock, 0, len(texts))
	for _, text := range texts {
		blocks = append(blocks, forwardedClaudeSystemPromptBlock{Text: text})
	}
	return prependClaudeSystemReminderBlocksToFirstUserMessage(payload, blocks, false)
}

func prependClaudeSystemReminderBlocksToFirstUserMessage(payload []byte, blocks []forwardedClaudeSystemPromptBlock, isExplicit bool) []byte {
	doc := ojson.NewDoc(payload)
	if !prependClaudeSystemReminderBlocksToFirstUserMessageDoc(doc, blocks, isExplicit) {
		return payload
	}
	return doc.Bytes()
}

func prependClaudeSystemReminderBlocksToFirstUserMessageDoc(doc *ojson.Doc, blocks []forwardedClaudeSystemPromptBlock, isExplicit bool) bool {
	if doc == nil || len(blocks) == 0 {
		return false
	}
	firstUserIdx := firstClaudeUserMessageIndexDoc(doc)
	if firstUserIdx < 0 {
		return false
	}

	contentPath := fmt.Sprintf("messages.%d.content", firstUserIdx)
	content := doc.Get(contentPath)
	if content.IsArray() {
		contentBlocks := content.Array()
		existing := make(map[string]int, len(contentBlocks))
		for _, block := range contentBlocks {
			if block.Get("type").String() == "text" {
				existing[block.Get("text").String()]++
			}
		}

		reminderBlocks := make([]string, 0, len(blocks))
		for _, b := range blocks {
			reminderText := claudeCallerSystemReminder(b.Text)
			if existing[reminderText] > 0 {
				existing[reminderText]--
				continue
			}
			if isExplicit && b.RawCC != "" {
				reminderBlocks = append(reminderBlocks, `{"type":"text","text":`+marshalJSONStringWithoutHTMLEscape(reminderText)+`,"cache_control":`+b.RawCC+`}`)
			} else {
				reminderBlocks = append(reminderBlocks, buildTextBlock(reminderText, nil))
			}
		}
		if len(reminderBlocks) == 0 {
			return false
		}

		insertAt := 0
		for insertAt < len(contentBlocks) && contentBlocks[insertAt].Get("type").String() == "tool_result" {
			insertAt++
		}
		rawBlocks := make([]string, 0, len(contentBlocks)+len(reminderBlocks))
		for idx, block := range contentBlocks {
			if idx == insertAt {
				rawBlocks = append(rawBlocks, reminderBlocks...)
			}
			rawBlocks = append(rawBlocks, block.Raw)
		}
		if insertAt == len(contentBlocks) {
			rawBlocks = append(rawBlocks, reminderBlocks...)
		}
		return doc.SetRawBytes(contentPath, []byte("["+strings.Join(rawBlocks, ",")+"]")) == nil
	} else if content.Type == ojson.String {
		rawBlocks := make([]string, 0, len(blocks)+1)
		for _, b := range blocks {
			reminderText := claudeCallerSystemReminder(b.Text)
			if isExplicit && b.RawCC != "" {
				rawBlocks = append(rawBlocks, `{"type":"text","text":`+marshalJSONStringWithoutHTMLEscape(reminderText)+`,"cache_control":`+b.RawCC+`}`)
			} else {
				rawBlocks = append(rawBlocks, buildTextBlock(reminderText, nil))
			}
		}
		rawBlocks = append(rawBlocks, buildTextBlock(content.String(), nil))
		return doc.SetRawBytes(contentPath, []byte("["+strings.Join(rawBlocks, ",")+"]")) == nil
	}
	return false
}

func claudeCallerSystemReminder(text string) string {
	var reminder strings.Builder
	reminder.WriteString("<system-reminder>\n")
	reminder.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		reminder.WriteByte('\n')
	}
	reminder.WriteString("</system-reminder>")
	return reminder.String()
}

// claudeHistoryHasAdvisorCallOrResult reports whether messages contains an advisor
// tool invocation (server_tool_use) or advisor result (advisor_tool_result /
// advisor_redacted_result). Anthropic cryptographically binds the encrypted
// advisor result to the conversation layout; any mid-conversation system splice
// shifts message indices and causes upstream 400 errors.
func claudeHistoryHasAdvisorCallOrResult(payload []byte) bool {
	return claudeMessagesHaveAdvisorCallOrResult(ojson.GetBytesNoCopy(payload, "messages"))
}

func claudeHistoryHasAdvisorCallOrResultDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	return claudeMessagesHaveAdvisorCallOrResult(doc.Get("messages"))
}

func claudeMessagesHaveAdvisorCallOrResult(messages ojson.Result) bool {
	if !messages.IsArray() {
		return false
	}
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		if content.IsArray() {
			for _, block := range content.Array() {
				blockType := block.Get("type").String()
				switch blockType {
				case "advisor_tool_result", "advisor_redacted_result":
					return true
				case "server_tool_use":
					if block.Get("name").String() == "advisor" {
						return true
					}
				case "tool_result":
					inner := block.Get("content")
					if inner.IsArray() {
						for _, b := range inner.Array() {
							if b.Get("type").String() == "advisor_redacted_result" {
								return true
							}
						}
					} else if inner.IsObject() {
						if inner.Get("type").String() == "advisor_redacted_result" {
							return true
						}
					}
				}
			}
		} else if content.IsObject() {
			blockType := content.Get("type").String()
			if blockType == "advisor_tool_result" || blockType == "advisor_redacted_result" {
				return true
			}
		}
	}
	return false
}

// claudeLeadingUserRunEnd skips directive-only system turns because Anthropic
// accepts them at any position. They must not cause caller system blocks to be
// inserted before a subsequent user turn.
func claudeLeadingUserRunEnd(messageBlocks []ojson.Result, firstUserIdx int) int {
	insertAt := firstUserIdx + 1
	for insertAt < len(messageBlocks) {
		message := messageBlocks[insertAt]
		if message.Get("role").String() != "user" && !isClaudeSystemDirectiveMessage(message) {
			break
		}
		insertAt++
	}
	return insertAt
}

func isClaudeSystemDirectiveMessage(message ojson.Result) bool {
	content := message.Get("content")
	return message.Get("role").String() == "system" &&
		message.Get("output_config").Exists() &&
		content.IsArray() && len(content.Array()) == 0
}

func claudeMidConversationSystemMessagesAtEnd(payload []byte) bool {
	firstUserIdx := firstClaudeUserMessageIndex(payload)
	if firstUserIdx < 0 {
		return false
	}

	messages := ojson.GetBytesNoCopy(payload, "messages")
	if !messages.IsArray() {
		return false
	}
	messageBlocks := messages.Array()
	insertAt := claudeLeadingUserRunEnd(messageBlocks, firstUserIdx)
	return insertAt == len(messageBlocks) || insertAt > firstUserIdx+1
}

func claudeMidConversationSystemMessagesAtEndDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	firstUserIdx := firstClaudeUserMessageIndexDoc(doc)
	if firstUserIdx < 0 {
		return false
	}

	messages := doc.Get("messages")
	if !messages.IsArray() {
		return false
	}
	messageBlocks := messages.Array()
	insertAt := claudeLeadingUserRunEnd(messageBlocks, firstUserIdx)
	return insertAt == len(messageBlocks) || insertAt > firstUserIdx+1
}

func insertClaudeMidConversationSystemMessages(payload []byte, texts []string) []byte {
	blocks := make([]forwardedClaudeSystemPromptBlock, 0, len(texts))
	for _, text := range texts {
		blocks = append(blocks, forwardedClaudeSystemPromptBlock{Text: text, CacheControl: &claudeCodeCacheControl})
	}
	return insertClaudeMidConversationSystemBlocks(payload, blocks, false)
}

func insertClaudeMidConversationSystemBlocks(payload []byte, blocks []forwardedClaudeSystemPromptBlock, isExplicit bool) []byte {
	doc := ojson.NewDoc(payload)
	if !insertClaudeMidConversationSystemBlocksDoc(doc, blocks, isExplicit) {
		return payload
	}
	return doc.Bytes()
}

func insertClaudeMidConversationSystemBlocksDoc(doc *ojson.Doc, blocks []forwardedClaudeSystemPromptBlock, isExplicit bool) bool {
	if doc == nil || len(blocks) == 0 {
		return false
	}
	firstUserIdx := firstClaudeUserMessageIndexDoc(doc)
	if firstUserIdx < 0 {
		return false
	}

	messages := doc.Get("messages")
	if !messages.IsArray() {
		return false
	}
	messageBlocks := messages.Array()
	insertAt := claudeLeadingUserRunEnd(messageBlocks, firstUserIdx)
	if len(messageBlocks)-insertAt >= len(blocks) {
		matches := true
		for idx, block := range blocks {
			message := messageBlocks[insertAt+idx]
			if message.Get("role").String() != "system" || claudeMessageContentText(message.Get("content")) != block.Text {
				matches = false
				break
			}
		}
		if matches {
			return false
		}
	}

	systemMessages := make([]string, 0, len(blocks))
	for _, block := range blocks {
		content := "[" + buildForwardedSystemBlock(block.Text, block, isExplicit) + "]"
		systemMessages = append(systemMessages, `{"role":"system","content":`+content+"}")
	}
	rawMessages := make([]string, 0, len(messageBlocks)+len(systemMessages))
	for idx, message := range messageBlocks {
		if idx == insertAt {
			rawMessages = append(rawMessages, systemMessages...)
		}
		rawMessages = append(rawMessages, message.Raw)
	}
	if insertAt == len(messageBlocks) {
		rawMessages = append(rawMessages, systemMessages...)
	}
	return doc.SetRawBytes("messages", []byte("["+strings.Join(rawMessages, ",")+"]")) == nil
}

func claudeMessageContentText(content ojson.Result) string {
	if content.Type == ojson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	content.ForEach(func(_, block ojson.Result) bool {
		if block.Get("type").String() == "text" {
			parts = append(parts, block.Get("text").String())
		}
		return true
	})
	return strings.Join(parts, "\n\n")
}

// claudeCodeSystemPlacementState identifies only the role=system turns that CPA
// itself inserted while cloaking. Caller-owned turns are deliberately excluded:
// if one is paired with a legacy model, validateClaudeMidSystemMessageModel must
// still return 400 instead of silently rewriting the caller's wire.
type claudeCodeSystemPlacementState struct {
	insertAt    int
	insertedRaw []string
	texts       []string
}

type claudeCodeSystemPlacementBeforeSnapshot struct {
	legacy       bool
	texts        []string
	messagesLen  int
	firstUserIdx int
	insertAt     int
}

func captureClaudeCodeSystemPlacementBeforeDoc(doc *ojson.Doc) claudeCodeSystemPlacementBeforeSnapshot {
	if doc == nil || claudeUsesLegacySystemReminderDoc(doc) {
		return claudeCodeSystemPlacementBeforeSnapshot{legacy: true}
	}
	texts := collectForwardedClaudeSystemPromptBlocks(doc.Get("system"))
	if len(texts) == 0 {
		return claudeCodeSystemPlacementBeforeSnapshot{}
	}
	beforeMessages := doc.Get("messages").Array()
	firstUserIdx := firstClaudeUserMessageIndexDoc(doc)
	if firstUserIdx < 0 {
		return claudeCodeSystemPlacementBeforeSnapshot{}
	}
	insertAt := claudeLeadingUserRunEnd(beforeMessages, firstUserIdx)
	return claudeCodeSystemPlacementBeforeSnapshot{
		texts:        texts,
		messagesLen:  len(beforeMessages),
		firstUserIdx: firstUserIdx,
		insertAt:     insertAt,
	}
}

func captureClaudeCodeSystemPlacementAfterDoc(before claudeCodeSystemPlacementBeforeSnapshot, afterDoc *ojson.Doc, cloaked bool) claudeCodeSystemPlacementState {
	if !cloaked || before.legacy || len(before.texts) == 0 || before.firstUserIdx < 0 || afterDoc == nil {
		return claudeCodeSystemPlacementState{}
	}
	wantLen := before.messagesLen + len(before.texts)
	if wantLen <= 0 || before.insertAt+len(before.texts) > wantLen {
		return claudeCodeSystemPlacementState{}
	}
	var scanner claudeStreamDocScanner
	scanner.scan(afterDoc, false, false)
	if !scanner.messagesIsArray || scanner.messagesLen != wantLen {
		return claudeCodeSystemPlacementState{}
	}
	insertedRaw := make([]string, len(before.texts))
	for rel, text := range before.texts {
		idx := before.insertAt + rel
		message := afterDoc.Get("messages." + strconv.Itoa(idx))
		if message.Get("role").String() != "system" || claudeMessageContentText(message.Get("content")) != text {
			return claudeCodeSystemPlacementState{}
		}
		insertedRaw[rel] = strings.Clone(message.Raw)
	}
	return claudeCodeSystemPlacementState{
		insertAt:    before.insertAt,
		insertedRaw: insertedRaw,
		texts:       append([]string(nil), before.texts...),
	}
}

// captureClaudeCodeSystemPlacement records CPA's modern-model system placement
// immediately after cloaking. The message-count increase is part of the proof:
// insertClaudeMidConversationSystemMessages returns without inserting when the
// same turns already exist, and those pre-existing turns belong to the caller.
func captureClaudeCodeSystemPlacement(before, after []byte, cloaked bool) claudeCodeSystemPlacementState {
	if !cloaked || claudeUsesLegacySystemReminder(before) {
		return claudeCodeSystemPlacementState{}
	}
	texts := collectForwardedClaudeSystemPromptBlocks(ojson.GetBytesNoCopy(before, "system"))
	if len(texts) == 0 {
		return claudeCodeSystemPlacementState{}
	}

	beforeMessages := ojson.GetBytesNoCopy(before, "messages").Array()
	afterMessages := ojson.GetBytesNoCopy(after, "messages").Array()
	if len(afterMessages) != len(beforeMessages)+len(texts) {
		return claudeCodeSystemPlacementState{}
	}
	firstUserIdx := firstClaudeUserMessageIndex(before)
	if firstUserIdx < 0 {
		return claudeCodeSystemPlacementState{}
	}
	insertAt := claudeLeadingUserRunEnd(beforeMessages, firstUserIdx)
	if insertAt+len(texts) > len(afterMessages) {
		return claudeCodeSystemPlacementState{}
	}

	insertedRaw := make([]string, len(texts))
	for idx, text := range texts {
		message := afterMessages[insertAt+idx]
		if message.Get("role").String() != "system" || claudeMessageContentText(message.Get("content")) != text {
			return claudeCodeSystemPlacementState{}
		}
		insertedRaw[idx] = strings.Clone(message.Raw)
	}
	return claudeCodeSystemPlacementState{
		insertAt:    insertAt,
		insertedRaw: insertedRaw,
		texts:       append([]string(nil), texts...),
	}
}

// reconcileClaudeCodeSystemPlacementAfterPayload repairs an otherwise stale
// placement decision when payload rules change the final model from modern to
// legacy. It removes only the exact contiguous turns captured above and replays
// their text through the existing legacy <system-reminder> path. If any payload
// rule also changed those messages, reconciliation fails closed and leaves the
// final validation guard to return 400.
func reconcileClaudeCodeSystemPlacementAfterPayload(payload []byte, state claudeCodeSystemPlacementState) []byte {
	doc := ojson.NewDoc(payload)
	if !reconcileClaudeCodeSystemPlacementAfterPayloadDoc(doc, state) {
		return payload
	}
	return doc.Bytes()
}

func reconcileClaudeCodeSystemPlacementAfterPayloadDoc(doc *ojson.Doc, state claudeCodeSystemPlacementState) bool {
	if doc == nil || len(state.insertedRaw) == 0 || !claudeUsesLegacySystemReminderDoc(doc) {
		return false
	}
	messages := doc.Get("messages").Array()
	if state.insertAt < 0 || state.insertAt+len(state.insertedRaw) > len(messages) {
		return false
	}
	for idx, raw := range state.insertedRaw {
		if messages[state.insertAt+idx].Raw != raw {
			return false
		}
	}

	rawMessages := make([]string, 0, len(messages)-len(state.insertedRaw))
	for idx, message := range messages {
		if idx >= state.insertAt && idx < state.insertAt+len(state.insertedRaw) {
			continue
		}
		rawMessages = append(rawMessages, message.Raw)
	}
	if errSet := doc.SetRawBytes("messages", []byte("["+strings.Join(rawMessages, ",")+"]")); errSet != nil {
		return false
	}
	blocks := make([]forwardedClaudeSystemPromptBlock, 0, len(state.texts))
	for _, text := range state.texts {
		blocks = append(blocks, forwardedClaudeSystemPromptBlock{Text: text})
	}
	prependClaudeSystemReminderBlocksToFirstUserMessageDoc(doc, blocks, false)
	return true
}

type claudeCodeFableState struct {
	injectedFallbacks bool
	injectedDisplay   bool
	injectedReporting bool
}

type claudeCodeFableBeforeSnapshot struct {
	hasFallbacks bool
	hasDisplay   bool
	hasReporting bool
}

func captureClaudeCodeFableBeforeDoc(doc *ojson.Doc) claudeCodeFableBeforeSnapshot {
	if doc == nil {
		return claudeCodeFableBeforeSnapshot{}
	}
	return claudeCodeFableBeforeSnapshot{
		hasFallbacks: doc.Get("fallbacks").Exists(),
		hasDisplay:   doc.Get("thinking.display").Exists(),
		hasReporting: hasFableReportingBlockDoc(doc),
	}
}

func captureClaudeCodeFableAfterDoc(before claudeCodeFableBeforeSnapshot, afterDoc *ojson.Doc, cloaked bool) claudeCodeFableState {
	if !cloaked || afterDoc == nil {
		return claudeCodeFableState{}
	}
	return claudeCodeFableState{
		injectedFallbacks: !before.hasFallbacks && afterDoc.Get("fallbacks").Exists(),
		injectedDisplay:   !before.hasDisplay && afterDoc.Get("thinking.display").Exists(),
		injectedReporting: !before.hasReporting && hasFableReportingBlockDoc(afterDoc),
	}
}

func hasFableReportingBlock(body []byte) bool {
	return hasFableReportingBlockFromSystem(ojson.GetBytesNoCopy(body, "system"))
}

func hasFableReportingBlockDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	return hasFableReportingBlockFromSystem(doc.Get("system"))
}

func hasFableReportingBlockFromSystem(system ojson.Result) bool {
	if !system.IsArray() {
		str := strings.ReplaceAll(system.String(), "\u200B", "")
		return str == claudeCodeFableReportingOutcomes || strings.Contains(str, claudeCodeFableReportingOutcomes)
	}
	for _, blk := range system.Array() {
		text := strings.ReplaceAll(blk.Get("text").String(), "\u200B", "")
		if text == claudeCodeFableReportingOutcomes {
			return true
		}
	}
	return false
}

func captureClaudeCodeFableState(before, after []byte, cloaked bool) claudeCodeFableState {
	if !cloaked || len(before) == 0 || len(after) == 0 {
		return claudeCodeFableState{}
	}
	return claudeCodeFableState{
		injectedFallbacks: !ojson.GetBytesNoCopy(before, "fallbacks").Exists() && ojson.GetBytesNoCopy(after, "fallbacks").Exists(),
		injectedDisplay:   !ojson.GetBytesNoCopy(before, "thinking.display").Exists() && ojson.GetBytesNoCopy(after, "thinking.display").Exists(),
		injectedReporting: !hasFableReportingBlock(before) && hasFableReportingBlock(after),
	}
}

// reconcileClaudeCodeFableModelAfterPayload reconciles model-specific additions
// (Opus fallback, thinking.display=updates, and # Reporting outcomes system block)
// if payload rules rewrite the request model between Fable 5.1 and non-Fable models.
func reconcileClaudeCodeFableModelAfterPayload(
	body []byte,
	fableState claudeCodeFableState,
	payloadTouchedFallbacks bool,
	payloadTouchedDisplay bool,
	cloaked bool,
	isProbeOrHelper bool,
) []byte {
	if !cloaked || len(body) == 0 {
		return body
	}
	doc := ojson.NewDoc(body)
	if !reconcileClaudeCodeFableModelAfterPayloadDoc(doc, fableState, payloadTouchedFallbacks, payloadTouchedDisplay, cloaked, isProbeOrHelper) {
		return body
	}
	return doc.Bytes()
}

func reconcileClaudeCodeFableModelAfterPayloadDoc(
	doc *ojson.Doc,
	fableState claudeCodeFableState,
	payloadTouchedFallbacks bool,
	payloadTouchedDisplay bool,
	cloaked bool,
	isProbeOrHelper bool,
) bool {
	if !cloaked || doc == nil || doc.Len() == 0 {
		return false
	}
	changed := false

	// Probes and helpers must never carry Fable additions (Opus fallback, display=updates, reporting block)
	if isProbeOrHelper {
		if fableState.injectedFallbacks && !payloadTouchedFallbacks {
			if doc.Delete("fallbacks") == nil {
				changed = true
			}
		}
		if fableState.injectedDisplay && !payloadTouchedDisplay {
			if doc.Delete("thinking.display") == nil {
				changed = true
			}
		}
		if fableState.injectedReporting {
			system := doc.Get("system")
			if system.IsArray() {
				blocks := make([]string, 0, len(system.Array()))
				removed := false
				for _, blk := range system.Array() {
					if strings.ReplaceAll(blk.Get("text").String(), "\u200B", "") == claudeCodeFableReportingOutcomes {
						removed = true
						continue
					}
					blocks = append(blocks, blk.Raw)
				}
				if removed {
					_ = doc.SetRawBytes("system", []byte("["+strings.Join(blocks, ",")+"]"))
					changed = true
				}
			} else if strings.ReplaceAll(system.String(), "\u200B", "") == claudeCodeFableReportingOutcomes {
				if doc.Delete("system") == nil {
					changed = true
				}
			}
		}
		return changed
	}
	currentModel := strings.ToLower(strings.TrimSpace(doc.Get("model").String()))
	// A model rewrite may change the native fallback target. Never override a
	// caller or payload-rule fallback; only replace one inserted by the cloak.
	if fableState.injectedFallbacks && !payloadTouchedFallbacks {
		wantFallback := ""
		switch {
		case isClaudeFable51Model(currentModel):
			wantFallback = "claude-opus-5"
		case isClaudeOpus55Model(currentModel):
			wantFallback = "claude-opus-4-8"
		}
		if doc.Get("fallbacks.0.model").String() != wantFallback {
			if doc.Delete("fallbacks") == nil {
				changed = true
			}
		}
	}

	if isClaudeFable51Model(currentModel) {
		// Non-Fable rewritten to Fable 5.1 (or original Fable 5.1): attach Fable additions
		// unless matching payload rules explicitly configured or filtered them.
		if !doc.Get("fallbacks").Exists() && !payloadTouchedFallbacks {
			_ = doc.SetRawBytes("fallbacks", []byte(`[{"model":"claude-opus-5"}]`))
			changed = true
		}
		if doc.Get("thinking").Exists() {
			thinkingType := doc.Get("thinking.type").String()
			if thinkingType == "adaptive" && !doc.Get("thinking.display").Exists() && !payloadTouchedDisplay {
				_ = doc.Set("thinking.display", "updates")
				changed = true
			} else if thinkingType != "adaptive" && fableState.injectedDisplay && !payloadTouchedDisplay {
				if doc.Delete("thinking.display") == nil {
					changed = true
				}
			}
		} else if fableState.injectedDisplay && !payloadTouchedDisplay {
			if doc.Delete("thinking.display") == nil {
				changed = true
			}
		}
		if !hasFableReportingBlockDoc(doc) {
			system := doc.Get("system")
			if system.IsArray() {
				blocks := make([]string, 0, len(system.Array())+1)
				for _, blk := range system.Array() {
					blocks = append(blocks, blk.Raw)
				}
				blocks = append(blocks, buildTextBlock(claudeCodeFableReportingOutcomes, nil))
				_ = doc.SetRawBytes("system", []byte("["+strings.Join(blocks, ",")+"]"))
				changed = true
			} else if system.Type == ojson.String {
				str := system.String()
				blocks := []string{
					buildTextBlock(str, nil),
					buildTextBlock(claudeCodeFableReportingOutcomes, nil),
				}
				_ = doc.SetRawBytes("system", []byte("["+strings.Join(blocks, ",")+"]"))
				changed = true
			} else if !system.Exists() {
				blocks := []string{
					buildTextBlock(claudeCodeFableReportingOutcomes, nil),
				}
				_ = doc.SetRawBytes("system", []byte("["+strings.Join(blocks, ",")+"]"))
				changed = true
			}
		}
		if !isProbeOrHelper {
			if applyClaudeCloakThinkingDisplayDoc(doc, payloadTouchedDisplay) {
				changed = true
			}
		}
		return changed
	}

	if isClaudeOpus55Model(currentModel) && !doc.Get("fallbacks").Exists() && !payloadTouchedFallbacks {
		_ = doc.SetRawBytes("fallbacks", []byte(`[{"model":"claude-opus-4-8"}]`))
		changed = true
	}
	thinkingType := strings.ToLower(strings.TrimSpace(doc.Get("thinking.type").String()))
	thinkingActive := thinkingType == "adaptive" || thinkingType == "enabled"
	// Payload rules can disable thinking without touching display. Remove only
	// CPA's injected value; explicit caller and operator choices remain owned.
	if fableState.injectedDisplay && !payloadTouchedDisplay && (!thinkingActive || !claudeModelUsesProgressDisplay(currentModel)) {
		if doc.Delete("thinking.display") == nil {
			changed = true
		}
	}
	if !isProbeOrHelper {
		if applyClaudeCloakThinkingDisplayDoc(doc, payloadTouchedDisplay) {
			changed = true
		}
	}

	// Remove Reporting outcomes if CPA automatically injected it
	if fableState.injectedReporting {
		system := doc.Get("system")
		if system.IsArray() {
			blocks := make([]string, 0, len(system.Array()))
			removed := false
			for _, blk := range system.Array() {
				if strings.ReplaceAll(blk.Get("text").String(), "\u200B", "") == claudeCodeFableReportingOutcomes {
					removed = true
					continue
				}
				blocks = append(blocks, blk.Raw)
			}
			if removed {
				_ = doc.SetRawBytes("system", []byte("["+strings.Join(blocks, ",")+"]"))
				changed = true
			}
		} else if strings.ReplaceAll(system.String(), "\u200B", "") == claudeCodeFableReportingOutcomes {
			if doc.Delete("system") == nil {
				changed = true
			}
		}
	}
	return changed
}

// claudeCodeLocalDate reproduces Claude Code 2.1.220's wcs() helper:
// new Date(), local calendar fields, and zero-padded YYYY-MM-DD components.
func claudeCodeLocalDate(now time.Time) string {
	year, month, day := now.Date()
	return fmt.Sprintf("%04d-%02d-%02d", year, int(month), day)
}

var claudeCodeCurrentTimeFunc func(cfg *config.Config, auth *cliproxyauth.Auth) time.Time

func claudeCodeCurrentTime(cfg *config.Config, auth *cliproxyauth.Auth) time.Time {
	if claudeCodeCurrentTimeFunc != nil {
		return claudeCodeCurrentTimeFunc(cfg, auth)
	}
	return time.Now().In(claudeCodeTimezone(cfg, auth))
}

func claudeCodeTimezone(cfg *config.Config, auth *cliproxyauth.Auth) *time.Location {
	if timezone := claudeCredentialTimezone(auth); timezone != "" {
		if location, errLocation := time.LoadLocation(timezone); errLocation == nil {
			return location
		}
	}
	if cfg == nil {
		return time.Local
	}
	timezone := strings.TrimSpace(cfg.ClaudeHeaderDefaults.Timezone)
	if timezone == "" {
		return time.Local
	}
	location, errLocation := time.LoadLocation(timezone)
	if errLocation != nil {
		return time.Local
	}
	return location
}

func claudeCredentialTimezone(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if timezone := strings.TrimSpace(auth.Attributes["timezone"]); timezone != "" {
			return timezone
		}
	}
	return strings.TrimSpace(claudeauth.ReadMetadataString(&auth.Metadata, "timezone"))
}

func claudeCodeCurrentDateReminder(date string) string {
	return fmt.Sprintf(`<system-reminder>
As you answer the user's questions, you can use the following context:
# currentDate
Today's date is %s.

      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>

`, date)
}

func firstClaudeUserMessageIndex(payload []byte) int {
	return firstClaudeUserMessageIndexFromMessages(ojson.GetBytesNoCopy(payload, "messages"))
}

func firstClaudeUserMessageIndexDoc(doc *ojson.Doc) int {
	if doc == nil || doc.Len() == 0 {
		return -1
	}
	var scanner claudeStreamDocScanner
	scanner.scan(doc, false, true)
	return scanner.firstUserIdx
}

func firstClaudeUserMessageIndexFromMessages(messages ojson.Result) int {
	if !messages.Exists() || !messages.IsArray() {
		return -1
	}

	firstUserIdx := -1
	messages.ForEach(func(idx, msg ojson.Result) bool {
		if msg.Get("role").String() == "user" {
			firstUserIdx = int(idx.Int())
			return false
		}
		return true
	})
	return firstUserIdx
}

func isClaudeCodeContextReminder(text string) bool {
	return strings.HasPrefix(text, "<system-reminder>") && strings.Contains(text, "</system-reminder>")
}

const claudeCodeCurrentDateReminderPrefix = "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is "

func isClaudeCodeCurrentDateReminder(text string) bool {
	return strings.HasPrefix(text, claudeCodeCurrentDateReminderPrefix)
}

// claudeCodeCurrentDateReminderDate extracts the YYYY-MM-DD date from a
// currentDate reminder, or "" when the reminder date is not a valid date.
func claudeCodeCurrentDateReminderDate(text string) string {
	rest, found := strings.CutPrefix(text, claudeCodeCurrentDateReminderPrefix)
	if !found || len(rest) < len("2006-01-02.") || rest[len("2006-01-02")] != '.' {
		return ""
	}
	date := rest[:len("2006-01-02")]
	if _, errParse := time.Parse("2006-01-02", date); errParse != nil {
		return ""
	}
	return date
}

func claudeSessionDatePinRetention(cfg *config.Config) time.Duration {
	if cfg == nil {
		return 0
	}
	return cfg.Claude.SessionDatePinRetention()
}

// claudeCallerCurrentDateReminderDate returns the date of the first valid
// caller-supplied currentDate reminder in the first user message.
func claudeCallerCurrentDateReminderDate(payload []byte) string {
	firstUserIdx := firstClaudeUserMessageIndex(payload)
	if firstUserIdx < 0 {
		return ""
	}
	return claudeCallerCurrentDateReminderDateFromContent(ojson.GetBytesNoCopy(payload, fmt.Sprintf("messages.%d.content", firstUserIdx)))
}

func claudeCallerCurrentDateReminderDateDoc(doc *ojson.Doc) string {
	if doc == nil {
		return ""
	}
	firstUserIdx := firstClaudeUserMessageIndexDoc(doc)
	if firstUserIdx < 0 {
		return ""
	}
	return claudeCallerCurrentDateReminderDateFromContent(doc.Get(fmt.Sprintf("messages.%d.content", firstUserIdx)))
}

func claudeCallerCurrentDateReminderDateFromContent(content ojson.Result) string {
	if content.Type == ojson.String {
		return claudeCodeCurrentDateReminderDate(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	for _, block := range content.Array() {
		if block.Get("type").String() != "text" {
			continue
		}
		if date := claudeCodeCurrentDateReminderDate(block.Get("text").String()); date != "" {
			return date
		}
	}
	return ""
}

// stripClaudeCodeCurrentDateReminder removes a leading caller currentDate
// reminder from string content so the injection path replaces it with the
// canonical reminder instead of duplicating it.
func stripClaudeCodeCurrentDateReminder(text string) string {
	if claudeCodeCurrentDateReminderDate(text) == "" {
		return text
	}
	end := strings.Index(text, "</system-reminder>")
	if end < 0 {
		return text
	}
	return strings.TrimLeft(text[end+len("</system-reminder>"):], "\n")
}

// stripFirstUserClaudeCodeCurrentDateReminder strips a leading caller
// currentDate reminder from a string-form first user message before the
// cloaking pipeline converts the content into blocks, preserving user text.
func stripFirstUserClaudeCodeCurrentDateReminder(payload []byte) []byte {
	doc := ojson.NewDoc(payload)
	if !stripFirstUserClaudeCodeCurrentDateReminderDoc(doc) {
		return payload
	}
	return doc.Bytes()
}

func stripFirstUserClaudeCodeCurrentDateReminderDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	firstUserIdx := firstClaudeUserMessageIndexDoc(doc)
	if firstUserIdx < 0 {
		return false
	}
	contentPath := fmt.Sprintf("messages.%d.content", firstUserIdx)
	content := doc.Get(contentPath)
	if content.Type != ojson.String {
		return false
	}
	stripped := stripClaudeCodeCurrentDateReminder(content.String())
	if stripped == content.String() {
		return false
	}
	return doc.Set(contentPath, stripped) == nil
}

// resolveClaudeSessionPinnedDate resolves the session date pin. With a
// configured retention, an existing pin always wins; a caller-supplied
// currentDate reminder only seeds the first anchor, so a later caller date
// cannot rewrite the established conversation prefix.
func resolveClaudeSessionPinnedDate(
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	payload []byte,
	continuityKey string,
	sessionID string,
) string {
	retention := claudeSessionDatePinRetention(cfg)
	candidate := claudeCodeLocalDate(claudeCodeCurrentTime(cfg, auth))
	if retention <= 0 {
		return helps.PinClaudeSessionDate(continuityKey, sessionID, candidate, retention)
	}
	if existing := helps.LookupClaudeSessionDatePin(sessionID, retention); existing != "" {
		// Re-pin the anchored date so the sliding retention refreshes
		// without ever changing the established prefix.
		return helps.PinClaudeSessionDate(continuityKey, sessionID, existing, retention)
	}
	if callerDate := claudeCallerCurrentDateReminderDate(payload); callerDate != "" {
		candidate = callerDate
	}
	return helps.PinClaudeSessionDate(continuityKey, sessionID, candidate, retention)
}

func resolveClaudeSessionPinnedDateDoc(
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	doc *ojson.Doc,
	continuityKey string,
	sessionID string,
) string {
	retention := claudeSessionDatePinRetention(cfg)
	candidate := claudeCodeLocalDate(claudeCodeCurrentTime(cfg, auth))
	if retention <= 0 {
		return helps.PinClaudeSessionDate(continuityKey, sessionID, candidate, retention)
	}
	if existing := helps.LookupClaudeSessionDatePin(sessionID, retention); existing != "" {
		return helps.PinClaudeSessionDate(continuityKey, sessionID, existing, retention)
	}
	if callerDate := claudeCallerCurrentDateReminderDateDoc(doc); callerDate != "" {
		candidate = callerDate
	}
	return helps.PinClaudeSessionDate(continuityKey, sessionID, candidate, retention)
}

// injectClaudeCodeCurrentDate stamps the currentDate reminder into the first
// user message. date is the already-resolved calendar date (the session-pinned
// one whenever continuity is available) so the reminder text cannot change
// between requests of one session.
func injectClaudeCodeCurrentDate(payload []byte, date string) []byte {
	return injectClaudeCodeCurrentDateInternal(payload, date, false)
}

func injectClaudeCodeCurrentDateInternal(payload []byte, date string, isExplicit bool) []byte {
	doc := ojson.NewDoc(payload)
	if !injectClaudeCodeCurrentDateInternalDoc(doc, date, isExplicit) {
		return payload
	}
	return doc.Bytes()
}

func injectClaudeCodeCurrentDateInternalDoc(doc *ojson.Doc, date string, isExplicit bool) bool {
	if doc == nil {
		return false
	}
	firstUserIdx := firstClaudeUserMessageIndexDoc(doc)
	if firstUserIdx < 0 {
		return false
	}

	contentPath := fmt.Sprintf("messages.%d.content", firstUserIdx)
	content := doc.Get(contentPath)
	dateText := claudeCodeCurrentDateReminder(date)
	dateBlock := buildTextBlock(dateText, nil)

	if content.Type == ojson.String {
		var userCC *claudeCacheControl = &claudeCodeCacheControl
		if isExplicit || strings.TrimSpace(content.String()) == "" {
			userCC = nil
		}
		userBlock := buildTextBlock(content.String(), userCC)
		newArray := "[" + dateBlock + "," + userBlock + "]"
		return doc.SetRawBytes(contentPath, []byte(newArray)) == nil
	}
	if !content.IsArray() {
		return false
	}

	blocks := content.Array()
	rawBlocks := make([]string, 0, len(blocks)+1)
	actualTextCached := false
	for _, block := range blocks {
		if block.Get("type").String() == "text" {
			text := block.Get("text").String()
			if isClaudeCodeCurrentDateReminder(text) {
				continue
			}
			if !actualTextCached && !isClaudeCodeContextReminder(text) && strings.TrimSpace(text) != "" {
				if !isExplicit {
					rawBlocks = append(rawBlocks, withEphemeralCacheControl(block.Raw))
					actualTextCached = true
					continue
				}
			}
		}
		rawBlocks = append(rawBlocks, block.Raw)
	}

	// Anthropic requires the user message following an assistant tool_use turn
	// to lead with its tool_result blocks, so the reminder goes after them.
	// Every other content shape keeps the native first-block placement.
	insertAt := 0
	for insertAt < len(rawBlocks) && ojson.Parse(rawBlocks[insertAt]).Get("type").String() == "tool_result" {
		insertAt++
	}
	rawBlocks = append(rawBlocks, "")
	copy(rawBlocks[insertAt+1:], rawBlocks[insertAt:])
	rawBlocks[insertAt] = dateBlock
	return doc.SetRawBytes(contentPath, []byte("["+strings.Join(rawBlocks, ",")+"]")) == nil
}

// claudeCodeContextManagement is the context_management object Claude Code
// 2.1.220 sends on every Messages request, captured 2026-08-01 from an isolated
// profile talking to api.anthropic.com. keep:"all" retains every thinking block,
// so replicating the client's exact value cannot produce upstream behaviour the
// real client does not already get.
const claudeCodeContextManagement = `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`

// claudeThinkingAcceptsClearThinking reports whether the payload's thinking
// value allows the clear_thinking_20251015 strategy. Anthropic rejects the
// request outright otherwise:
//
//	`clear_thinking_20251015` strategy requires `thinking` to be enabled or adaptive
//
// An absent thinking field is therefore just as ineligible as an explicit
// {"type":"disabled"}, which is why this checks for the accepted values rather
// than excluding the disabled one.
func claudeThinkingAcceptsClearThinking(payload []byte) bool {
	switch ojson.GetBytesNoCopy(payload, "thinking.type").String() {
	case "enabled", "adaptive":
		return true
	default:
		return false
	}
}

func claudeThinkingAcceptsClearThinkingDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	switch doc.Get("thinking.type").String() {
	case "enabled", "adaptive":
		return true
	default:
		return false
	}
}

// injectClaudeCodeContextManagement supplies context_management when the caller
// omitted it. CPA already claims context-management-2025-06-27 in Anthropic-Beta,
// so a missing body field is an observable inconsistency with the real client. A
// caller that sent its own object keeps it untouched.
func injectClaudeCodeContextManagement(payload []byte) ([]byte, bool) {
	doc := ojson.NewDoc(payload)
	if !injectClaudeCodeContextManagementDoc(doc) {
		return payload, false
	}
	return doc.Bytes(), true
}

func injectClaudeCodeContextManagementDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Get("context_management").Exists() || !claudeThinkingAcceptsClearThinkingDoc(doc) {
		return false
	}
	return doc.SetRawBytes("context_management", []byte(claudeCodeContextManagement)) == nil
}

type claudeCodeContextManagementState struct {
	eligible              bool
	callerOwned           bool
	automaticallyInjected bool
}

// reconcileClaudeCodeContextManagement resolves built-in ownership after forced
// tool-choice processing. User payload rules run later and never need reconciliation.
func reconcileClaudeCodeContextManagement(payload []byte, state claudeCodeContextManagementState) []byte {
	doc := ojson.NewDoc(payload)
	if !reconcileClaudeCodeContextManagementDoc(doc, state) {
		return payload
	}
	return doc.Bytes()
}

func reconcileClaudeCodeContextManagementDoc(doc *ojson.Doc, state claudeCodeContextManagementState) bool {
	if doc == nil {
		return false
	}
	contextManagement := doc.Get("context_management")

	// Any thinking value the strategy does not accept must drop an object CPA
	// injected itself. disableThinkingIfToolChoiceForced deletes the whole
	// thinking field after injection, so this also covers a request that was
	// still eligible when injectClaudeCodeContextManagement ran.
	if !claudeThinkingAcceptsClearThinkingDoc(doc) {
		if state.callerOwned || !state.automaticallyInjected {
			return false
		}
		if contextManagement.Raw != claudeCodeContextManagement {
			return false
		}
		return doc.Delete("context_management") == nil
	}

	if !state.eligible || state.callerOwned || contextManagement.Exists() {
		return false
	}
	return doc.SetRawBytes("context_management", []byte(claudeCodeContextManagement)) == nil
}

// withEphemeralCacheControl stamps the native Claude Code default cache marker
// {"type":"ephemeral"} onto a content block. A 1h ttl is not part of the default
// shape; upgradeClaudeCacheControlTTL adds it for the credentials native uses it
// on, after all placement decisions are final.
func withEphemeralCacheControl(rawBlock string) string {
	updated, err := ojson.SetRawBytes([]byte(rawBlock), "cache_control", []byte(`{"type":"ephemeral"}`))
	if err != nil {
		return rawBlock
	}
	return string(updated)
}

type claudeWirePolicy struct {
	OAuth                bool // real OAuth token runtime identity
	ProfileClaudeCodeCLI bool // request fingerprint looks like Claude Code CLI
	ConfirmedClaudeCode  bool
	Cloak                bool
	Uncloak              bool
	ExplicitNever        bool
}

type claudeCloakSettings struct {
	strictMode     bool
	sensitiveWords []string
	cacheUserID    bool
}

func isClaudeCodeIdentitySystemText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return trimmed == claudeCodeCLIIdentity ||
		trimmed == "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK." ||
		trimmed == "You are a Claude agent, built on Anthropic's Claude Agent SDK."
}

func isEffectiveConfirmedClaudeCode(detection helps.ClaudeCodeRequestDetection, policy claudeWirePolicy) bool {
	if policy.Uncloak {
		return false
	}
	if detection.Confirmed {
		return true
	}
	return !policy.Cloak && policy.ExplicitNever && detection.ConfirmedShape
}

func stripClaudeCodeSystemBlocks(payload []byte) []byte {
	doc := ojson.NewDoc(payload)
	if !stripClaudeCodeSystemBlocksDoc(doc) {
		return payload
	}
	return doc.Bytes()
}

func stripClaudeCodeSystemBlocksDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	changed := stripClaudeCodeAttributionSystemDoc(doc)
	system := doc.Get("system")
	if !system.Exists() {
		return changed
	}
	if system.Type == ojson.String {
		if !isClaudeCodeIdentitySystemText(system.String()) {
			return changed
		}
		if errDelete := doc.Delete("system"); errDelete == nil {
			return true
		}
		return changed
	}
	if !system.IsArray() {
		return changed
	}
	kept := make([]string, 0, len(system.Array()))
	removed := false
	system.ForEach(func(_, block ojson.Result) bool {
		if block.Get("type").String() == "text" && isClaudeCodeIdentitySystemText(block.Get("text").String()) {
			removed = true
			return true
		}
		if block.Raw != "" {
			kept = append(kept, block.Raw)
		}
		return true
	})
	if !removed {
		return changed
	}
	if len(kept) == 0 {
		if errDelete := doc.Delete("system"); errDelete == nil {
			return true
		}
		return changed
	}
	if errSet := doc.SetRawBytes("system", []byte("["+strings.Join(kept, ",")+"]")); errSet == nil {
		return true
	}
	return changed
}

func uncloakClaudeCodePayload(payload []byte, confirmedClaudeCode bool) []byte {
	doc := ojson.NewDoc(payload)
	if !uncloakClaudeCodePayloadDoc(doc, confirmedClaudeCode) {
		return payload
	}
	return doc.Bytes()
}

func uncloakClaudeCodePayloadDoc(doc *ojson.Doc, confirmedClaudeCode bool) bool {
	if doc == nil {
		return false
	}
	changed := stripClaudeCodeSystemBlocksDoc(doc)
	if metadata := doc.Get("metadata"); metadata.Exists() && metadata.IsObject() {
		if userID := metadata.Get("user_id"); userID.Exists() && (confirmedClaudeCode || helps.IsValidUserID(userID.String())) {
			if errDelete := doc.Delete("metadata.user_id"); errDelete == nil {
				changed = true
				if remaining := doc.Get("metadata"); remaining.Exists() && remaining.IsObject() && len(remaining.Map()) == 0 {
					_ = doc.Delete("metadata")
				}
			}
		}
	}
	if diag := doc.Get("diagnostics"); diag.Exists() && (confirmedClaudeCode || diag.Get("previous_message_id").Exists()) {
		if errDelete := doc.Delete("diagnostics"); errDelete == nil {
			changed = true
		}
	}
	return changed
}

func resolveClaudeWirePolicy(cfg *config.Config, auth *cliproxyauth.Auth, apiKey string, confirmedClaudeCode bool) (claudeWirePolicy, claudeCloakSettings) {
	if auth != nil && auth.AuthKind() == cliproxyauth.AuthKindAPIKey {
		cfg = cfg.ForAPIKey()
	}
	cloakCfg := resolveClaudeKeyCloakConfig(cfg, auth)
	attrMode, attrStrict, attrWords, attrCache := getCloakConfigFromAuth(auth)

	cloakMode := "auto"
	if cfg != nil && cfg.DisableClaudeCloakMode {
		cloakMode = "never"
	}
	settings := claudeCloakSettings{
		strictMode:     attrStrict,
		sensitiveWords: attrWords,
		cacheUserID:    attrCache,
	}
	explicitMode := strings.ToLower(strings.TrimSpace(attrMode))
	if attrMode != "" {
		cloakMode = attrMode
	}
	if cloakCfg != nil {
		if mode := strings.TrimSpace(cloakCfg.Mode); mode != "" {
			cloakMode = mode
			explicitMode = strings.ToLower(mode)
		}
		if cloakCfg.StrictMode {
			settings.strictMode = true
		}
		if len(cloakCfg.SensitiveWords) > 0 {
			settings.sensitiveWords = cloakCfg.SensitiveWords
		}
		if cloakCfg.CacheUserID != nil {
			settings.cacheUserID = *cloakCfg.CacheUserID
		}
	}

	fp := resolveClaudeFingerprintPolicy(cfg, auth, apiKey)
	cloakConfigured := cloakCfg != nil || attrMode != "" || attrStrict || len(attrWords) > 0 || attrCache
	trimmedMode := strings.ToLower(strings.TrimSpace(cloakMode))
	uncloak := !fp.ProfileClaudeCodeCLI && (explicitMode == "never" || explicitMode == "uncloak")
	explicitNever := trimmedMode == "never" && !uncloak
	policy := claudeWirePolicy{
		OAuth:                fp.AuthIsOAuthToken,
		ProfileClaudeCodeCLI: fp.ProfileClaudeCodeCLI,
		ConfirmedClaudeCode:  confirmedClaudeCode,
		Cloak:                (fp.ProfileClaudeCodeCLI || cloakConfigured) && !confirmedClaudeCode && !uncloak,
		Uncloak:              uncloak,
		ExplicitNever:        explicitNever,
	}
	if confirmedClaudeCode {
		// Native Claude Code is always a passthrough client unless the
		// credential explicitly opts out of Claude Code identity via never/uncloak.
		policy.Cloak = false
		return policy, settings
	}
	switch trimmedMode {
	case "always":
		policy.Cloak = true
	case "never", "uncloak":
		policy.Cloak = false
	default:
		// Auto applies the CLI cloak only to real Claude OAuth credentials,
		// explicit fingerprint-profile opt-ins, or credentials with explicit cloak
		// settings. Other API keys and delegated providers keep the caller shape.
	}
	return policy, settings
}

// applyCloaking applies the shared Messages/count_tokens wire policy. The
// returned boolean reports whether cloaking ran.
func applyCloaking(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	payload []byte,
	apiKey string,
	confirmedClaudeCode bool,
	cchSigning bool,
) ([]byte, bool, error) {
	return applyCloakingInternal(ctx, cfg, auth, payload, apiKey, confirmedClaudeCode, cchSigning, true)
}

func applyCloakingInternal(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	payload []byte,
	apiKey string,
	confirmedClaudeCode bool,
	cchSigning bool,
	obfuscateSensitiveWords bool,
	explicitCacheMode ...bool,
) ([]byte, bool, error) {
	doc := ojson.NewDoc(payload)
	cloaked, changed, errCloak := applyCloakingInternalDoc(ctx, cfg, auth, doc, apiKey, confirmedClaudeCode, cchSigning, obfuscateSensitiveWords, explicitCacheMode...)
	if errCloak != nil {
		return nil, false, errCloak
	}
	if !changed {
		return payload, cloaked, nil
	}
	return doc.Bytes(), cloaked, nil
}

func applyCloakingInternalDoc(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	doc *ojson.Doc,
	apiKey string,
	confirmedClaudeCode bool,
	cchSigning bool,
	obfuscateSensitiveWords bool,
	explicitCacheMode ...bool,
) (bool, bool, error) {
	if doc == nil {
		return false, false, nil
	}
	policy, settings := resolveClaudeWirePolicy(cfg, auth, apiKey, confirmedClaudeCode)
	if !policy.Cloak {
		changed := false
		if policy.Uncloak {
			changed = uncloakClaudeCodePayloadDoc(doc, confirmedClaudeCode)
		}
		return false, changed, nil
	}
	isExplicit := (len(explicitCacheMode) > 0 && explicitCacheMode[0]) || isExplicitPromptCacheModeContext(ctx)
	// Strict mode drops caller system prompts entirely, so nothing needs a
	// destination and an unusable block cannot lose information.
	if !settings.strictMode {
		if errSystem := validateClaudeCallerSystemBlocks(doc.Get("system")); errSystem != nil {
			return false, false, errSystem
		}
	}

	billingVersion := helps.DefaultClaudeVersion(cfg)
	workload := getWorkloadFromContext(ctx)

	isProbeOrHelper := isClaudeProbeOrHelperRequestDoc(doc)
	isSubagent := false
	prevReq := ""
	promptID := ""
	pinnedDate := ""
	var incomingHeaders http.Header
	if !isProbeOrHelper {
		incomingHeaders = resolveIncomingClaudeHeaders(ctx, helps.IncomingHeadersFromContext(ctx))
		isSubagent = isClaudeSubagentRequestDoc(incomingHeaders, doc)
		existingPrevReq, existingPromptID := extractClaudeBillingTagsDoc(doc)

		var cCtx helps.ClaudeContinuityContext
		var ok bool
		prevReq, promptID, cCtx, ok = resolveClaudeContinuityTagsDoc(ctx, cfg, auth, incomingHeaders, doc, confirmedClaudeCode, existingPrevReq, existingPromptID)
		if ok {
			pinnedDate = cCtx.PinnedDate
			if continuityCtx := helps.ClaudeContinuityContextFromContext(ctx); continuityCtx != nil {
				*continuityCtx = cCtx
			}
		}
	}
	// Without continuity state (probe/helper traffic, or no session identity)
	// fall back to the per-request date, which is the pre-pinning behaviour.
	if pinnedDate == "" {
		pinnedDate = claudeCodeLocalDate(claudeCodeCurrentTime(cfg, auth))
	}
	if !isProbeOrHelper && claudeSessionDatePinRetention(cfg) > 0 {
		// Strip a string-form caller reminder before the pipeline turns the
		// content into blocks, so user text is never dropped with it.
		stripFirstUserClaudeCodeCurrentDateReminderDoc(doc)
	}

	turnOrigin := ""
	if !isProbeOrHelper && !isSubagent {
		turnOrigin = "human"
	}
	turnOriginArgs := []string{turnOrigin}
	if isExplicit {
		turnOriginArgs = append(turnOriginArgs, "explicit_cache_mode")
	}
	checkSystemInstructionsWithSigningModeAtDoc(
		doc,
		settings.strictMode,
		cchSigning,
		billingVersion,
		"cli",
		workload,
		pinnedDate,
		isSubagent,
		prevReq,
		promptID,
		!policy.OAuth, // OAuth top-level caller prompts trigger Anthropic's third-party classifier (#6432).
		turnOriginArgs...,
	)

	// In native Claude Code 2.1.258, claude-fable-5-1 requests carry:
	// "fallbacks": [{"model": "claude-opus-5"}]
	model := strings.ToLower(strings.TrimSpace(doc.Get("model").String()))
	if isClaudeOpus55Model(model) && !isProbeOrHelper && !doc.Get("fallbacks").Exists() {
		_ = doc.SetRawBytes("fallbacks", []byte(`[{"model":"claude-opus-4-8"}]`))
	}
	if isClaudeFable51Model(model) && !isProbeOrHelper {
		if !doc.Get("fallbacks").Exists() {
			_ = doc.SetRawBytes("fallbacks", []byte(`[{"model":"claude-opus-5"}]`))
		}
	}
	if !isProbeOrHelper {
		applyClaudeCloakThinkingDisplayDoc(doc, false)
	}

	// Probes never use 1h cache in native Claude Code; ensure any caller-supplied
	// 1h ttl is stripped to match extended-cache-ttl beta suppression. Subagents
	// preserve caller-requested 1h cache TTL (e.g. subagentPromptCacheTtl: 1h).
	if !isExplicit && (isProbeOrHelper || (isSubagent && !claudeSubagentRequests1hDoc(incomingHeaders, doc))) {
		stripClaudeCacheControlTTLDoc(doc)
	}

	// Claude-Code-CLI fingerprint identity (real OAuth or fingerprint-profile=claude-code-cli)
	// is applied later through the shared ApplyClaudeCredentialMetadata path.
	// Other non-OAuth cloaking keeps the legacy per-request fake user_id.
	if !policy.ProfileClaudeCodeCLI {
		if _, errFakeUserID := injectFakeUserIDDoc(ctx, doc, apiKey, settings.cacheUserID); errFakeUserID != nil {
			return false, false, errFakeUserID
		}
	}

	// Apply sensitive word obfuscation
	if obfuscateSensitiveWords && len(settings.sensitiveWords) > 0 {
		matcher := helps.BuildSensitiveWordMatcher(settings.sensitiveWords)
		helps.ObfuscateSensitiveWordsDoc(doc, matcher)
	}

	return true, true, nil
}

type claudeCacheControl struct {
	Type  string `json:"type"`
	TTL   string `json:"ttl,omitempty"`
	Scope string `json:"scope,omitempty"`
}

// claudeCodeCacheControl is the default Claude Code breakpoint shape.
//
// Recovered from the cache-control constructor in the installed 2.1.220,
// 2.1.221 and 2.1.227 binaries, which is byte-identical in all three:
//
//	function ctor({scope, ttl} = {}) {
//	  return {type: "ephemeral", ...ttl && {ttl}, ...scope === "global" && {scope}}
//	}
//
// ttl is spread in only when the caller passes one, so the default native wire
// shape carries no ttl at all. upgradeClaudeCacheControlTTL applies the 1h pool
// separately, for the credentials native selects it on. The struct field order
// preserves the native {type, ttl} key order when sjson marshals a value.
var claudeCodeCacheControl = claudeCacheControl{
	Type: "ephemeral",
}

// claudeCacheControlTTL1h is the only non-default ttl native ever selects.
const claudeCacheControlTTL1h = "1h"

// ensureCacheControl injects default cache_control breakpoints for translated
// entrypoints (Responses/Chat/Gemini) after cloaking. Placement follows the
// native request builder recovered from the installed binaries:
//  1. LAST system block when no system marker exists
//  2. LAST cacheable message when that message has no marker
//
// Tools are normally not stamped: the native Messages builder never passes a
// cacheControl to its tool-schema converter, and a system breakpoint already
// covers the tools prefix. The one exception is a payload with tools but no
// system at all, which native never produces (it always sends a system prompt).
// Without the fallback such a request has its only breakpoint on the volatile
// final message, so a stateless caller with large tool definitions rewrites the
// whole prefix on every request and never reads it back.
//
// Each section injects independently so cloaking's first-user marker cannot
// suppress system/latest-user breakpoints. Callers still run enforceCacheControlLimit.
// See: https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
func ensureCacheControl(payload []byte) []byte {
	doc := ojson.NewDoc(payload)
	if !ensureCacheControlDoc(doc) {
		return payload
	}
	return doc.Bytes()
}

func ensureCacheControlDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	changed := false
	if !claudeDocHasCacheableSystem(doc) {
		if injectToolsCacheControlDoc(doc) {
			changed = true
		}
	}
	if injectSystemCacheControlDoc(doc) {
		changed = true
	}
	if injectMessagesCacheControlDoc(doc) {
		changed = true
	}
	return changed
}

// claudePayloadHasCacheableSystem reports whether the payload has a system prompt
// that injectSystemCacheControl can actually host a breakpoint on. An absent key, an
// empty array, an array of only empty/whitespace text blocks, and an empty string
// all leave the tools prefix uncovered.
func claudePayloadHasCacheableSystem(payload []byte) bool {
	return claudeSystemHasCacheableBlocks(ojson.GetBytesNoCopy(payload, "system"))
}

func claudeDocHasCacheableSystem(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	return claudeSystemHasCacheableBlocks(doc.Get("system"))
}

func claudeSystemHasCacheableBlocks(system ojson.Result) bool {
	switch {
	case !system.Exists():
		return false
	case system.IsArray():
		return lastCacheableClaudeSystemIndex(system) >= 0
	case system.Type == ojson.String:
		return strings.TrimSpace(system.String()) != ""
	default:
		return false
	}
}

func claudeSystemBlockCanHostCacheControl(item ojson.Result) bool {
	if !item.IsObject() {
		return false
	}
	if typ := item.Get("type").String(); typ != "" && typ != "text" {
		return false
	}
	return strings.TrimSpace(item.Get("text").String()) != ""
}

func lastCacheableClaudeSystemIndex(system ojson.Result) int {
	if !system.IsArray() {
		return -1
	}
	lastEligible := -1
	system.ForEach(func(index, item ojson.Result) bool {
		if claudeSystemBlockCanHostCacheControl(item) {
			lastEligible = int(index.Int())
		}
		return true
	})
	return lastEligible
}

// upgradeClaudeCacheControlTTL mirrors the native ttl upgrade helper, which only
// touches blocks that already carry a cache_control without a ttl:
//
//	function upgrade(block, ttl) {
//	  if (!("cache_control" in block) || !block.cache_control || block.cache_control.ttl) return block
//	  return {...block, cache_control: {...block.cache_control, ttl}}
//	}
//
// It never creates a breakpoint, so placement stays owned by ensureCacheControl.
// Native gates the 1h selection on OAuth scopes, a non-overage account and an
// allowlisted internal query source, and pushes extended-cache-ttl-2025-04-11
// only when that selection produced a 1h body ttl. CPA has no query-source
// equivalent, so the credential check is the reproducible half: OAuth is exactly
// when claudeCodeCLIBetas emits extended-cache-ttl, which keeps body ttl and the
// beta strictly paired the way native does. API-key credentials keep the plain
// {"type":"ephemeral"} native default, which also avoids sending ttl to
// Anthropic-compatible gateways that never advertised support for it.
func upgradeClaudeCacheControlTTL(payload []byte, ttl string) []byte {
	if ttl == "" || len(payload) == 0 || !ojson.ValidBytes(payload) {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !upgradeClaudeCacheControlTTLDoc(doc, ttl) {
		return payload
	}
	return doc.Bytes()
}

func upgradeClaudeCacheControlTTLDoc(doc *ojson.Doc, ttl string) bool {
	if ttl == "" || doc == nil || doc.Len() == 0 {
		return false
	}
	changed := false
	forEachClaudeCacheControlPathDoc(doc, func(path string, cacheControl ojson.Result) bool {
		if !cacheControl.IsObject() || cacheControl.Get("ttl").Exists() {
			return true
		}
		blockType := cacheControl.Get("type")
		if blockType.Type != ojson.String {
			return true
		}
		// Rebuild the object so the native {type, ttl, scope} key order survives
		// instead of appending ttl after a caller-supplied scope.
		upgraded := `{"type":` + marshalJSONStringWithoutHTMLEscape(blockType.String()) +
			`,"ttl":` + marshalJSONStringWithoutHTMLEscape(ttl)
		if scope := cacheControl.Get("scope"); scope.Exists() {
			upgraded += `,"scope":` + scope.Raw
		}
		upgraded += "}"
		if errSet := doc.SetRawBytes(path+".cache_control", []byte(upgraded)); errSet == nil {
			changed = true
		}
		return true
	})
	return changed
}

// stripClaudeCacheControlTTL removes any ttl field from cache_control blocks in payload,
// downgrading {"type":"ephemeral","ttl":"..."} to {"type":"ephemeral"}.
// This ensures that when extended-cache-ttl-2025-04-11 is stripped (e.g. on probes,
// subagents, or non-OAuth credentials), the body does not retain a ttl field that would
// trigger Anthropic 400 errors or fingerprint mismatch.
func stripClaudeCacheControlTTL(payload []byte) []byte {
	if len(payload) == 0 || !ojson.ValidBytes(payload) {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !stripClaudeCacheControlTTLDoc(doc) {
		return payload
	}
	return doc.Bytes()
}

func stripClaudeCacheControlTTLDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	changed := false
	forEachClaudeCacheControlPathDoc(doc, func(path string, cacheControl ojson.Result) bool {
		if !cacheControl.IsObject() || !cacheControl.Get("ttl").Exists() {
			return true
		}
		if errDel := doc.Delete(path + ".cache_control.ttl"); errDel == nil {
			changed = true
		}
		return true
	})
	return changed
}

var errStopClaudeScan = errors.New("stop claude scan")

const (
	claudeSecNone uint8 = iota
	claudeSecTools
	claudeSecSystem
	claudeSecMessages
)

const (
	claudeK3None uint8 = iota
	claudeK3CacheControl
	claudeK3Role
	claudeK3Content
)

const (
	claudeK5None uint8 = iota
	claudeK5Type
	claudeK5Text
	claudeK5CacheControl
)

const (
	claudeScanTokNone uint8 = iota
	claudeScanTokString
	claudeScanTokPrimitive
)

const (
	claudeStrIgnore uint8 = iota
	claudeStrCapShort
	claudeStrCheckNonSpace
)

type claudeScannedCC struct {
	idx1 int
	idx2 int
	raw  []byte
}

type claudeStreamDocScanner struct {
	collectCC       bool
	stopOnFirstUser bool

	depth      int
	isObj      [64]bool
	inKey      [64]bool
	topSection uint8
	elemIdx    int
	lvl3Key    uint8
	blockIdx   int
	lvl5Key    uint8

	tok            uint8
	strEsc         uint8
	uVal           uint16
	strIsKey       bool
	strTarget      uint8
	strBuf         [256]byte
	strExtra       []byte
	strLen         int
	strHasEsc      bool
	strHasNonSpace bool
	utfBuf         [4]byte
	utfLen         uint8

	captureActive bool
	captureDepth  int
	capStart      int
	ccBuf         [256]byte
	ccExtra       []byte
	ccLen         int

	ccArena     [1024]byte
	ccArenaUsed int
	toolCC      []claudeScannedCC
	toolCCBuf   [8]claudeScannedCC
	sysCC       []claudeScannedCC
	sysCCBuf    [8]claudeScannedCC
	msgCC       []claudeScannedCC
	msgCCBuf    [16]claudeScannedCC

	messagesIsArray                  bool
	messagesLen                      int
	firstUserIdx                     int
	lastMessageIndex                 int
	lastMsgRole                      string
	lastMsgContentIsNonEmptyString   bool
	lastEligibleIndex                int
	lastEligibleContentIsArray       bool
	lastEligibleContentIsNonEmptyStr bool
	lastEligibleHasCacheControl      bool
	lastEligibleTargetBlockIndex     int

	curMsgRole              string
	curMsgContentIsString   bool
	curMsgContentHasNonSpc  bool
	curMsgContentIsArray    bool
	curMsgHasCacheControl   bool
	curMsgLastCacheableUser int
	curMsgLastCacheableAsst int

	curBlockIsObj         bool
	curBlockType          string
	curBlockTextHasNonSpc bool

	err error
}

func (s *claudeStreamDocScanner) scan(doc *ojson.Doc, collectCC, stopOnFirstUser bool) {
	s.collectCC = collectCC
	s.stopOnFirstUser = stopOnFirstUser
	s.firstUserIdx = -1
	s.lastMessageIndex = -1
	s.lastEligibleIndex = -1
	s.lastEligibleTargetBlockIndex = -1
	s.toolCC = s.toolCCBuf[:0]
	s.sysCC = s.sysCCBuf[:0]
	s.msgCC = s.msgCCBuf[:0]
	_, _ = doc.WriteTo(s)
}

func (s *claudeStreamDocScanner) appendCaptureBytes(b []byte) {
	if len(b) == 0 {
		return
	}
	if len(s.ccExtra) > 0 {
		s.ccExtra = append(s.ccExtra, b...)
		s.ccLen = len(s.ccExtra)
		return
	}
	if s.ccLen+len(b) <= len(s.ccBuf) {
		copy(s.ccBuf[s.ccLen:], b)
		s.ccLen += len(b)
		return
	}
	s.ccExtra = append(s.ccExtra[:0], s.ccBuf[:s.ccLen]...)
	s.ccExtra = append(s.ccExtra, b...)
	s.ccLen = len(s.ccExtra)
}

func (s *claudeStreamDocScanner) startCapture(depth, idx int) {
	s.captureActive = true
	s.captureDepth = depth
	s.capStart = idx
	s.ccLen = 0
	if s.ccExtra != nil {
		s.ccExtra = s.ccExtra[:0]
	}
}

func (s *claudeStreamDocScanner) finishCapture(p []byte, endIdx int) {
	s.appendCaptureBytes(p[s.capStart:endIdx])
	s.captureActive = false
	if s.ccLen == 0 {
		return
	}
	var raw []byte
	if len(s.ccExtra) > 0 {
		raw = s.ccExtra
	} else {
		raw = s.ccBuf[:s.ccLen]
	}
	if s.captureDepth == 5 {
		if isValidClaudeCacheControl(util.ParseGJSONBytesNoCopy(raw)) {
			s.curMsgHasCacheControl = true
		}
	}
	if !s.collectCC {
		return
	}
	var stored []byte
	if s.ccArenaUsed+len(raw) <= len(s.ccArena) {
		stored = s.ccArena[s.ccArenaUsed : s.ccArenaUsed+len(raw)]
		copy(stored, raw)
		s.ccArenaUsed += len(raw)
	} else {
		stored = bytes.Clone(raw)
	}
	switch s.topSection {
	case claudeSecTools:
		s.toolCC = append(s.toolCC, claudeScannedCC{idx1: s.elemIdx, raw: stored})
	case claudeSecSystem:
		s.sysCC = append(s.sysCC, claudeScannedCC{idx1: s.elemIdx, raw: stored})
	case claudeSecMessages:
		s.msgCC = append(s.msgCC, claudeScannedCC{idx1: s.elemIdx, idx2: s.blockIdx, raw: stored})
	}
}

func (s *claudeStreamDocScanner) resetShortToken() {
	s.strTarget = claudeStrCapShort
	s.strLen = 0
	s.strHasEsc = false
	if s.strExtra != nil {
		s.strExtra = s.strExtra[:0]
	}
}

func (s *claudeStreamDocScanner) appendShortByte(c byte) {
	if len(s.strExtra) > 0 {
		s.strExtra = append(s.strExtra, c)
		s.strLen = len(s.strExtra)
		return
	}
	if s.strLen < len(s.strBuf) {
		s.strBuf[s.strLen] = c
		s.strLen++
		return
	}
	s.strExtra = append(s.strExtra[:0], s.strBuf[:s.strLen]...)
	s.strExtra = append(s.strExtra, c)
	s.strLen = len(s.strExtra)
}

func (s *claudeStreamDocScanner) decodeShortToken() string {
	var rawStr []byte
	if len(s.strExtra) > 0 {
		rawStr = s.strExtra
	} else {
		rawStr = s.strBuf[:s.strLen]
	}
	if !s.strHasEsc {
		switch string(rawStr) {
		case "":
			return ""
		case "tools":
			return "tools"
		case "system":
			return "system"
		case "messages":
			return "messages"
		case "cache_control":
			return "cache_control"
		case "role":
			return "role"
		case "content":
			return "content"
		case "type":
			return "type"
		case "text":
			return "text"
		case "user":
			return "user"
		case "assistant":
			return "assistant"
		case "thinking":
			return "thinking"
		case "redacted_thinking":
			return "redacted_thinking"
		default:
			return "other"
		}
	}
	var stackRaw [258]byte
	var quoted []byte
	if 2+len(rawStr) <= len(stackRaw) {
		stackRaw[0] = '"'
		copy(stackRaw[1:], rawStr)
		stackRaw[1+len(rawStr)] = '"'
		quoted = stackRaw[:2+len(rawStr)]
	} else {
		quoted = make([]byte, 2+len(rawStr))
		quoted[0] = '"'
		copy(quoted[1:], rawStr)
		quoted[1+len(rawStr)] = '"'
	}
	decoded := util.ParseGJSONBytesNoCopy(quoted).String()
	switch decoded {
	case "":
		return ""
	case "tools":
		return "tools"
	case "system":
		return "system"
	case "messages":
		return "messages"
	case "cache_control":
		return "cache_control"
	case "role":
		return "role"
	case "content":
		return "content"
	case "type":
		return "type"
	case "text":
		return "text"
	case "user":
		return "user"
	case "assistant":
		return "assistant"
	case "thinking":
		return "thinking"
	case "redacted_thinking":
		return "redacted_thinking"
	default:
		return "other"
	}
}

func (s *claudeStreamDocScanner) finishKey() {
	if s.strTarget != claudeStrCapShort {
		return
	}
	key := s.decodeShortToken()
	switch s.depth {
	case 1:
		switch key {
		case "tools":
			s.topSection = claudeSecTools
		case "system":
			s.topSection = claudeSecSystem
		case "messages":
			s.topSection = claudeSecMessages
		default:
			s.topSection = claudeSecNone
		}
	case 3:
		if s.isObj[2] || !s.isObj[3] {
			s.lvl3Key = claudeK3None
			return
		}
		if s.topSection == claudeSecTools || s.topSection == claudeSecSystem {
			if key == "cache_control" {
				s.lvl3Key = claudeK3CacheControl
			} else {
				s.lvl3Key = claudeK3None
			}
		} else if s.topSection == claudeSecMessages {
			switch key {
			case "role":
				s.lvl3Key = claudeK3Role
			case "content":
				s.lvl3Key = claudeK3Content
			default:
				s.lvl3Key = claudeK3None
			}
		}
	case 5:
		if s.topSection != claudeSecMessages || s.lvl3Key != claudeK3Content || s.isObj[4] || !s.isObj[5] {
			s.lvl5Key = claudeK5None
			return
		}
		switch key {
		case "type":
			s.lvl5Key = claudeK5Type
		case "text":
			s.lvl5Key = claudeK5Text
		case "cache_control":
			s.lvl5Key = claudeK5CacheControl
		default:
			s.lvl5Key = claudeK5None
		}
	}
}

func (s *claudeStreamDocScanner) startValue(c byte, idx int) {
	switch s.depth {
	case 2:
		if s.topSection != claudeSecNone && !s.isObj[2] {
			if s.topSection == claudeSecMessages {
				s.messagesLen = s.elemIdx + 1
				s.curMsgRole = ""
				s.curMsgContentIsString = false
				s.curMsgContentHasNonSpc = false
				s.curMsgContentIsArray = false
				s.curMsgHasCacheControl = false
				s.curMsgLastCacheableUser = -1
				s.curMsgLastCacheableAsst = -1
			}
		}
	case 3:
		if !s.isObj[2] && s.isObj[3] {
			if s.lvl3Key == claudeK3CacheControl && s.collectCC {
				s.startCapture(3, idx)
			} else if s.lvl3Key == claudeK3Content && s.topSection == claudeSecMessages {
				if c == '"' {
					s.curMsgContentIsString = true
				} else if c == '[' {
					s.curMsgContentIsArray = true
					s.blockIdx = 0
				}
			}
		}
	case 4:
		if s.topSection == claudeSecMessages && s.lvl3Key == claudeK3Content && !s.isObj[4] {
			s.curBlockIsObj = c == '{'
			s.curBlockType = ""
			s.curBlockTextHasNonSpc = false
		}
	case 5:
		if s.topSection == claudeSecMessages && s.lvl3Key == claudeK3Content && !s.isObj[4] && s.isObj[5] {
			if s.lvl5Key == claudeK5CacheControl {
				s.startCapture(5, idx)
			}
		}
	}
}

func (s *claudeStreamDocScanner) finishValue(p []byte, endIdx int) {
	if s.captureActive && s.depth == s.captureDepth {
		s.finishCapture(p, endIdx)
	}
	switch s.depth {
	case 1:
		s.topSection = claudeSecNone
	case 2:
		if s.topSection == claudeSecMessages && !s.isObj[2] {
			s.lastMessageIndex = s.elemIdx
			s.lastMsgRole = s.curMsgRole
			s.lastMsgContentIsNonEmptyString = s.curMsgContentIsString && s.curMsgContentHasNonSpc
			if s.curMsgRole == "user" && s.firstUserIdx < 0 {
				s.firstUserIdx = s.elemIdx
				if s.stopOnFirstUser {
					s.err = errStopClaudeScan
				}
			}
			if s.curMsgRole == "user" || s.curMsgRole == "assistant" {
				targetBlock := s.curMsgLastCacheableUser
				if s.curMsgRole == "assistant" {
					targetBlock = s.curMsgLastCacheableAsst
				}
				eligible := false
				if s.curMsgContentIsString {
					eligible = s.curMsgContentHasNonSpc
				} else if s.curMsgContentIsArray {
					eligible = targetBlock >= 0
				}
				if eligible {
					s.lastEligibleIndex = s.elemIdx
					s.lastEligibleContentIsArray = s.curMsgContentIsArray
					s.lastEligibleContentIsNonEmptyStr = s.curMsgContentIsString && s.curMsgContentHasNonSpc
					s.lastEligibleHasCacheControl = s.curMsgHasCacheControl
					s.lastEligibleTargetBlockIndex = targetBlock
				}
			}
		}
	case 3:
		if s.isObj[3] {
			s.lvl3Key = claudeK3None
		}
	case 4:
		if s.topSection == claudeSecMessages && s.lvl3Key == claudeK3Content && !s.isObj[4] {
			if s.curBlockIsObj {
				switch s.curBlockType {
				case "text":
					if s.curBlockTextHasNonSpc {
						s.curMsgLastCacheableUser = s.blockIdx
						s.curMsgLastCacheableAsst = s.blockIdx
					}
				case "thinking", "redacted_thinking":
					s.curMsgLastCacheableAsst = -1
				case "":
				default:
					s.curMsgLastCacheableUser = s.blockIdx
					s.curMsgLastCacheableAsst = s.blockIdx
				}
			}
		}
	case 5:
		if s.isObj[5] {
			s.lvl5Key = claudeK5None
		}
	}
}

func claudeHexValue(c byte) uint16 {
	switch {
	case c >= '0' && c <= '9':
		return uint16(c - '0')
	case c >= 'a' && c <= 'f':
		return uint16(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return uint16(c-'A') + 10
	default:
		return 0
	}
}

func (s *claudeStreamDocScanner) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if s.captureActive {
		s.capStart = 0
	}
	i := 0
	for i < len(p) {
		switch s.tok {
		case claudeScanTokString:
			if s.strEsc == 0 && (s.strTarget == claudeStrIgnore || (s.strTarget == claudeStrCheckNonSpace && s.strHasNonSpace)) {
				for i < len(p) && p[i] != '"' && p[i] != '\\' {
					i++
				}
				if i >= len(p) {
					break
				}
			}
			c := p[i]
			switch s.strEsc {
			case 0:
				if c == '\\' {
					if s.strTarget == claudeStrCapShort {
						s.strHasEsc = true
						s.appendShortByte(c)
					} else if s.strTarget == claudeStrCheckNonSpace && s.utfLen > 0 {
						s.strHasNonSpace = true
						s.utfLen = 0
					}
					s.strEsc = 1
					i++
					continue
				}
				if c == '"' {
					if s.strTarget == claudeStrCheckNonSpace && s.utfLen > 0 {
						s.strHasNonSpace = true
						s.utfLen = 0
					}
					s.tok = claudeScanTokNone
					if s.strIsKey {
						s.strIsKey = false
						s.finishKey()
					} else {
						if s.depth == 3 && s.topSection == claudeSecMessages && !s.isObj[2] && s.isObj[3] {
							if s.lvl3Key == claudeK3Role {
								s.curMsgRole = s.decodeShortToken()
								if s.curMsgRole == "user" && s.firstUserIdx < 0 {
									s.firstUserIdx = s.elemIdx
									if s.stopOnFirstUser {
										s.err = errStopClaudeScan
										return len(p), s.err
									}
								}
							} else if s.lvl3Key == claudeK3Content {
								s.curMsgContentHasNonSpc = s.strHasNonSpace
							}
						} else if s.depth == 5 && s.topSection == claudeSecMessages && s.lvl3Key == claudeK3Content && !s.isObj[4] && s.isObj[5] {
							if s.lvl5Key == claudeK5Type {
								s.curBlockType = s.decodeShortToken()
							} else if s.lvl5Key == claudeK5Text {
								s.curBlockTextHasNonSpc = s.strHasNonSpace
							}
						}
						s.finishValue(p, i+1)
						if s.err != nil {
							return len(p), s.err
						}
					}
					i++
					continue
				}
				if s.strTarget == claudeStrCapShort {
					s.appendShortByte(c)
				} else if s.strTarget == claudeStrCheckNonSpace && !s.strHasNonSpace {
					if c < 0x80 {
						if s.utfLen > 0 || (c > ' ') || (c != ' ' && c != '\t' && c != '\n' && c != '\v' && c != '\f' && c != '\r') {
							s.strHasNonSpace = true
							s.utfLen = 0
						}
					} else {
						s.utfBuf[s.utfLen] = c
						s.utfLen++
						if utf8.FullRune(s.utfBuf[:s.utfLen]) {
							r, _ := utf8.DecodeRune(s.utfBuf[:s.utfLen])
							s.utfLen = 0
							if !unicode.IsSpace(r) {
								s.strHasNonSpace = true
							}
						} else if int(s.utfLen) == len(s.utfBuf) {
							s.utfLen = 0
							s.strHasNonSpace = true
						}
					}
				}
				i++
			case 1:
				if s.strTarget == claudeStrCapShort {
					s.appendShortByte(c)
				} else if s.strTarget == claudeStrCheckNonSpace && !s.strHasNonSpace {
					switch c {
					case '"', '\\', '/', 'b':
						s.strHasNonSpace = true
					}
				}
				if c == 'u' {
					s.strEsc = 2
					s.uVal = 0
				} else {
					s.strEsc = 0
				}
				i++
			default:
				if s.strTarget == claudeStrCapShort {
					s.appendShortByte(c)
				} else if s.strTarget == claudeStrCheckNonSpace && !s.strHasNonSpace {
					s.uVal = (s.uVal << 4) | claudeHexValue(c)
					if s.strEsc == 5 && !unicode.IsSpace(rune(s.uVal)) {
						s.strHasNonSpace = true
					}
				}
				if s.strEsc == 5 {
					s.strEsc = 0
				} else {
					s.strEsc++
				}
				i++
			}
			continue

		case claudeScanTokPrimitive:
			for i < len(p) && p[i] > ' ' && p[i] != ',' && p[i] != '}' && p[i] != ']' && p[i] != ':' {
				i++
			}
			if i >= len(p) {
				break
			}
			s.tok = claudeScanTokNone
			s.finishValue(p, i)
			if s.err != nil {
				return len(p), s.err
			}
		}

		for i < len(p) && (p[i] == ' ' || p[i] == '\t' || p[i] == '\r' || p[i] == '\n') {
			i++
		}
		if i >= len(p) {
			break
		}
		c := p[i]
		if c == ':' {
			if s.depth > 0 && s.depth < len(s.inKey) {
				s.inKey[s.depth] = false
			}
			i++
			continue
		}
		if c == ',' {
			if s.depth > 0 && s.depth < len(s.isObj) {
				if s.isObj[s.depth] {
					s.inKey[s.depth] = true
				} else if s.depth == 2 && s.topSection != claudeSecNone {
					s.elemIdx++
				} else if s.depth == 4 && s.topSection == claudeSecMessages && s.lvl3Key == claudeK3Content {
					s.blockIdx++
				}
			}
			i++
			continue
		}
		if c == '}' || c == ']' {
			if s.depth > 0 {
				s.depth--
				if s.depth >= 1 {
					s.finishValue(p, i+1)
					if s.err != nil {
						return len(p), s.err
					}
				}
			}
			i++
			continue
		}
		if s.depth > 0 && s.depth < len(s.isObj) && s.isObj[s.depth] && s.inKey[s.depth] {
			if c == '"' {
				s.tok = claudeScanTokString
				s.strEsc = 0
				s.strIsKey = true
				if s.depth == 1 || s.depth == 3 || s.depth == 5 {
					s.resetShortToken()
				} else {
					s.strTarget = claudeStrIgnore
				}
			}
			i++
			continue
		}

		s.startValue(c, i)
		switch c {
		case '{', '[':
			s.depth++
			if s.depth < len(s.isObj) {
				s.isObj[s.depth] = c == '{'
				s.inKey[s.depth] = c == '{'
			}
			if s.depth == 2 && c == '[' && s.topSection != claudeSecNone {
				s.elemIdx = 0
				if s.topSection == claudeSecMessages {
					s.messagesIsArray = true
				}
			}
			i++
		case '"':
			s.tok = claudeScanTokString
			s.strEsc = 0
			s.strIsKey = false
			s.strTarget = claudeStrIgnore
			if s.depth == 3 && s.topSection == claudeSecMessages && !s.isObj[2] && s.isObj[3] {
				if s.lvl3Key == claudeK3Role {
					s.resetShortToken()
				} else if s.lvl3Key == claudeK3Content {
					s.strTarget = claudeStrCheckNonSpace
					s.strHasNonSpace = false
					s.utfLen = 0
				}
			} else if s.depth == 5 && s.topSection == claudeSecMessages && s.lvl3Key == claudeK3Content && !s.isObj[4] && s.isObj[5] {
				if s.lvl5Key == claudeK5Type {
					s.resetShortToken()
				} else if s.lvl5Key == claudeK5Text {
					s.strTarget = claudeStrCheckNonSpace
					s.strHasNonSpace = false
					s.utfLen = 0
				}
			}
			i++
		default:
			s.tok = claudeScanTokPrimitive
			i++
		}
	}
	if s.captureActive {
		s.appendCaptureBytes(p[s.capStart:len(p)])
	}
	return len(p), nil
}

// forEachClaudeCacheControlPathDoc walks every candidate cache_control in
// Anthropic evaluation order (tools -> system -> messages) in a single O(N)
// pass without materializing split containers.
func forEachClaudeCacheControlPathDoc(doc *ojson.Doc, visit func(path string, cacheControl ojson.Result) bool) {
	if doc == nil || doc.Len() == 0 {
		return
	}
	var scanner claudeStreamDocScanner
	scanner.scan(doc, true, false)
	for _, entry := range scanner.toolCC {
		ccRes := util.ParseGJSONBytesNoCopy(entry.raw)
		if !ccRes.Exists() {
			continue
		}
		if !visit("tools."+strconv.Itoa(entry.idx1), ccRes) {
			return
		}
	}
	for _, entry := range scanner.sysCC {
		ccRes := util.ParseGJSONBytesNoCopy(entry.raw)
		if !ccRes.Exists() {
			continue
		}
		if !visit("system."+strconv.Itoa(entry.idx1), ccRes) {
			return
		}
	}
	for _, entry := range scanner.msgCC {
		ccRes := util.ParseGJSONBytesNoCopy(entry.raw)
		if !ccRes.Exists() {
			continue
		}
		path := "messages." + strconv.Itoa(entry.idx1) + ".content." + strconv.Itoa(entry.idx2)
		if !visit(path, ccRes) {
			return
		}
	}
}

// forEachClaudeCacheControlBlock walks every block that can carry cache_control
// in Anthropic's evaluation order: tools, then system, then messages.
func forEachClaudeCacheControlBlock(payload []byte, visit func(path string, block ojson.Result)) {
	forEachClaudeCacheControlBlockResult(
		ojson.GetBytesNoCopy(payload, "tools"),
		ojson.GetBytesNoCopy(payload, "system"),
		ojson.GetBytesNoCopy(payload, "messages"),
		visit,
	)
}

func forEachClaudeCacheControlBlockResult(tools, system, messages ojson.Result, visit func(path string, block ojson.Result)) {
	if tools.IsArray() {
		tools.ForEach(func(idx, item ojson.Result) bool {
			visit(fmt.Sprintf("tools.%d", int(idx.Int())), item)
			return true
		})
	}
	if system.IsArray() {
		system.ForEach(func(idx, item ojson.Result) bool {
			visit(fmt.Sprintf("system.%d", int(idx.Int())), item)
			return true
		})
	}
	if messages.IsArray() {
		messages.ForEach(func(msgIdx, message ojson.Result) bool {
			content := message.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(itemIdx, item ojson.Result) bool {
				visit(fmt.Sprintf("messages.%d.content.%d", int(msgIdx.Int()), int(itemIdx.Int())), item)
				return true
			})
			return true
		})
	}
}

// isExplicitPromptCacheMode reports whether any of the candidate payloads
// specifies prompt_cache_options.mode as "explicit" (case-insensitive).
func isExplicitPromptCacheMode(payloads ...[]byte) bool {
	for _, payload := range payloads {
		if len(payload) == 0 {
			continue
		}
		mode := ojson.GetBytesNoCopy(payload, "prompt_cache_options.mode").String()
		if strings.EqualFold(strings.TrimSpace(mode), "explicit") {
			return true
		}
	}
	return false
}

func isExplicitPromptCacheModeDoc(doc *ojson.Doc, candidates ...[]byte) bool {
	if doc != nil && doc.Len() > 0 {
		mode := doc.Get("prompt_cache_options.mode").String()
		if strings.EqualFold(strings.TrimSpace(mode), "explicit") {
			return true
		}
	}
	return isExplicitPromptCacheMode(candidates...)
}

type explicitPromptCacheModeContextKey struct{}

func withExplicitPromptCacheMode(ctx context.Context, explicit bool) context.Context {
	return context.WithValue(ctx, explicitPromptCacheModeContextKey{}, explicit)
}

func isExplicitPromptCacheModeContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(explicitPromptCacheModeContextKey{}).(bool)
	return v
}

func stripPromptCacheOptions(payload []byte) []byte {
	doc := ojson.NewDoc(payload)
	if !stripPromptCacheOptionsDoc(doc) {
		return payload
	}
	return doc.Bytes()
}

func stripPromptCacheOptionsDoc(doc *ojson.Doc) bool {
	if doc == nil || !doc.Get("prompt_cache_options").Exists() {
		return false
	}
	return doc.Delete("prompt_cache_options") == nil
}

func shouldEnsureCacheControl(payload []byte, cloaked, confirmedClaudeCode bool, candidates ...[]byte) bool {
	if isExplicitPromptCacheMode(append([][]byte{payload}, candidates...)...) {
		return false
	}
	return !confirmedClaudeCode && (cloaked || countCacheControls(payload) == 0)
}

func shouldEnsureCacheControlDoc(doc *ojson.Doc, cloaked, confirmedClaudeCode bool, candidates ...[]byte) bool {
	if isExplicitPromptCacheModeDoc(doc, candidates...) {
		return false
	}
	return !confirmedClaudeCode && (cloaked || countCacheControlsDoc(doc) == 0)
}

func countCacheControls(payload []byte) int {
	return countCacheControlsResult(
		ojson.GetBytesNoCopy(payload, "system"),
		ojson.GetBytesNoCopy(payload, "tools"),
		ojson.GetBytesNoCopy(payload, "messages"),
	)
}

func countCacheControlsDoc(doc *ojson.Doc) int {
	if doc == nil {
		return 0
	}
	count := 0
	forEachClaudeCacheControlPathDoc(doc, func(_ string, cc ojson.Result) bool {
		if isValidClaudeCacheControl(cc) {
			count++
		}
		return true
	})
	return count
}

func countCacheControlsResult(system, tools, messages ojson.Result) int {
	count := 0

	if system.IsArray() {
		system.ForEach(func(_, item ojson.Result) bool {
			if isValidClaudeCacheControl(item.Get("cache_control")) {
				count++
			}
			return true
		})
	}

	if tools.IsArray() {
		tools.ForEach(func(_, item ojson.Result) bool {
			if isValidClaudeCacheControl(item.Get("cache_control")) {
				count++
			}
			return true
		})
	}

	if messages.IsArray() {
		messages.ForEach(func(_, msg ojson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, item ojson.Result) bool {
					if isValidClaudeCacheControl(item.Get("cache_control")) {
						count++
					}
					return true
				})
			}
			return true
		})
	}

	return count
}

// normalizeCacheControlTTL ensures cache_control TTL values don't violate the
// prompt-caching-scope-2026-01-05 ordering constraint: a 1h-TTL block must not
// appear after a 5m-TTL block anywhere in the evaluation order.
//
// Anthropic evaluates blocks in order: tools → system (index 0..N) → messages.
// Within each section, blocks are evaluated in array order. A 5m (default) block
// followed by a 1h block at ANY later position is an error — including within
// the same section (e.g. system[1]=5m then system[3]=1h).
//
// Strategy: walk all cache_control blocks in evaluation order. Once a 5m block
// is seen, strip ttl from ALL subsequent 1h blocks (downgrading them to 5m).
func normalizeCacheControlTTL(payload []byte) []byte {
	if len(payload) == 0 || !ojson.ValidBytes(payload) {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !normalizeCacheControlTTLDoc(doc) {
		return payload
	}
	return doc.Bytes()
}

func normalizeCacheControlTTLDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}

	seen5m := false
	modified := false
	forEachClaudeCacheControlPathDoc(doc, func(path string, cc ojson.Result) bool {
		if !isValidClaudeCacheControl(cc) {
			return true
		}
		ttl := cc.Get("ttl")
		if ttl.Type != ojson.String || ttl.String() != "1h" {
			seen5m = true
			return true
		}
		if !seen5m {
			return true
		}
		if errDel := doc.Delete(path + ".cache_control.ttl"); errDel == nil {
			modified = true
		}
		return true
	})
	return modified
}

// enforceCacheControlLimit removes excess cache_control blocks from a payload
// so the total does not exceed the Anthropic API limit (currently 4).
//
// Anthropic evaluates cache breakpoints in order: tools → system → messages.
// The most valuable breakpoints are:
//  1. Last tool         — caches ALL tool definitions
//  2. Last system block — caches ALL system content
//  3. Recent messages   — cache conversation context
//
// Removal priority (strip lowest-value first):
//
//	Phase 1: system blocks earliest-first, preserving the last one.
//	Phase 2: tool blocks earliest-first, preserving the last one.
//	Phase 3: message content blocks earliest-first.
//	Phase 4: remaining system blocks (last system).
//	Phase 5: remaining tool blocks (last tool).
func enforceCacheControlLimit(payload []byte, maxBlocks int) []byte {
	if len(payload) == 0 || !ojson.ValidBytes(payload) {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !enforceCacheControlLimitDoc(doc, maxBlocks) {
		return payload
	}
	return doc.Bytes()
}

func enforceCacheControlLimitDoc(doc *ojson.Doc, maxBlocks int) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}

	thread := doc.Get("thread")
	if thread.Exists() && thread.Type != ojson.Null && maxBlocks > 0 {
		// Anthropic reserves one cache breakpoint for thread continuation.
		maxBlocks--
	}

	var toolPaths, systemPaths, messagePaths []string
	forEachClaudeCacheControlPathDoc(doc, func(path string, cc ojson.Result) bool {
		if !isValidClaudeCacheControl(cc) {
			return true
		}
		ccPath := path + ".cache_control"
		switch {
		case strings.HasPrefix(path, "tools."):
			toolPaths = append(toolPaths, ccPath)
		case strings.HasPrefix(path, "system."):
			systemPaths = append(systemPaths, ccPath)
		default:
			messagePaths = append(messagePaths, ccPath)
		}
		return true
	})

	total := len(toolPaths) + len(systemPaths) + len(messagePaths)
	if total <= maxBlocks {
		return false
	}

	excess := total - maxBlocks
	changed := false
	deleteList := func(paths []string) {
		for _, path := range paths {
			if excess <= 0 {
				return
			}
			if errDel := doc.Delete(path); errDel == nil {
				changed = true
				excess--
			}
		}
	}

	if len(systemPaths) > 1 {
		deleteList(systemPaths[:len(systemPaths)-1])
	}
	if excess <= 0 {
		return changed
	}
	if len(toolPaths) > 1 {
		deleteList(toolPaths[:len(toolPaths)-1])
	}
	if excess <= 0 {
		return changed
	}
	deleteList(messagePaths)
	if excess <= 0 {
		return changed
	}
	if len(systemPaths) > 0 {
		deleteList(systemPaths[len(systemPaths)-1:])
	}
	if excess <= 0 {
		return changed
	}
	if len(toolPaths) > 0 {
		deleteList(toolPaths[len(toolPaths)-1:])
	}
	return changed
}

// injectMessagesCacheControl adds cache_control to the message the native rolling
// breakpoint selector would pick. Recovered from the marker selector in the
// installed 2.1.220/2.1.221/2.1.227 binaries:
//
//	eligible(msg): a non-assistant turn is always eligible; an assistant turn with
//	               string content is eligible; an assistant turn with array content
//	               is eligible only when its last block is not thinking-like.
//	last        := walk back from the end, skipping internal system turns and
//	               ineligible turns.
//	target      := (final turn is a system turn with non-empty STRING content and
//	               last >= 0) ? final turn : last
//
// The final-system special case is deliberately narrow: native requires string
// content there and writes a brand new single text block for it rather than
// stamping the last element of an existing array. Markers on other messages must
// not suppress this rolling write.
func injectMessagesCacheControl(payload []byte) []byte {
	doc := ojson.NewDoc(payload)
	if !injectMessagesCacheControlDoc(doc) {
		return payload
	}
	return doc.Bytes()
}

func injectMessagesCacheControlDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	var scanner claudeStreamDocScanner
	scanner.scan(doc, false, false)
	if !scanner.messagesIsArray || scanner.lastMessageIndex < 0 {
		return false
	}

	if scanner.lastEligibleIndex >= 0 && scanner.lastMsgRole == "system" && scanner.lastMsgContentIsNonEmptyString {
		finalContent := doc.Get(fmt.Sprintf("messages.%d.content", scanner.lastMessageIndex))
		if finalContent.Type == ojson.String && strings.TrimSpace(finalContent.String()) != "" {
			return injectClaudeFinalSystemCacheControlDoc(doc, scanner.lastMessageIndex, finalContent.String())
		}
	}
	if scanner.lastEligibleIndex < 0 || scanner.lastEligibleHasCacheControl {
		return false
	}

	if scanner.lastEligibleContentIsArray {
		if scanner.lastEligibleTargetBlockIndex >= 0 {
			cacheControlPath := fmt.Sprintf("messages.%d.content.%d.cache_control", scanner.lastEligibleIndex, scanner.lastEligibleTargetBlockIndex)
			if errSet := doc.Set(cacheControlPath, claudeCodeCacheControl); errSet != nil {
				log.Warnf("failed to inject cache_control into messages: %v", errSet)
				return false
			}
			return true
		}
		return false
	}

	if scanner.lastEligibleContentIsNonEmptyStr {
		contentPath := fmt.Sprintf("messages.%d.content", scanner.lastEligibleIndex)
		contentRes := doc.Get(contentPath)
		if contentRes.Type == ojson.String && strings.TrimSpace(contentRes.String()) != "" {
			newContent := "[" + buildTextBlock(contentRes.String(), &claudeCodeCacheControl) + "]"
			if errSet := doc.SetRawBytes(contentPath, []byte(newContent)); errSet != nil {
				log.Warnf("failed to inject cache_control into message string content: %v", errSet)
				return false
			}
			return true
		}
	}

	return false
}

func isEmptyClaudeTextBlock(block ojson.Result) bool {
	return block.IsObject() &&
		block.Get("type").String() == "text" &&
		strings.TrimSpace(block.Get("text").String()) == ""
}

func claudeContentBlockCanHostCacheControl(block ojson.Result) bool {
	if !block.IsObject() {
		return false
	}
	switch block.Get("type").String() {
	case "", "thinking", "redacted_thinking":
		return false
	case "text":
		return strings.TrimSpace(block.Get("text").String()) != ""
	default:
		return true
	}
}

func lastCacheableClaudeMessageContentBlockIndex(message ojson.Result) int {
	content := message.Get("content")
	if !content.IsArray() {
		return -1
	}
	blocks := content.Array()
	if len(blocks) == 0 {
		return -1
	}
	isAssistant := message.Get("role").String() == "assistant"
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		if isEmptyClaudeTextBlock(block) {
			continue
		}
		if isAssistant {
			switch block.Get("type").String() {
			case "thinking", "redacted_thinking":
				return -1
			}
		}
		if claudeContentBlockCanHostCacheControl(block) {
			return i
		}
	}
	return -1
}

// claudeMessageEligibleForRollingCache reports whether the native selector would
// consider this user/assistant turn as a rolling breakpoint host. Native rejects
// an assistant turn whose last content block is thinking-like, because a thinking
// block cannot host the marker, and Anthropic rejects cache_control on empty text blocks.
func claudeMessageEligibleForRollingCache(message ojson.Result) bool {
	content := message.Get("content")
	if content.Type == ojson.String {
		return strings.TrimSpace(content.String()) != ""
	}
	return lastCacheableClaudeMessageContentBlockIndex(message) >= 0
}

// injectClaudeFinalSystemCacheControl reproduces the native final-system special
// case, which replaces the string content with a single marked text block.
func injectClaudeFinalSystemCacheControl(payload []byte, messageIndex int, text string) []byte {
	doc := ojson.NewDoc(payload)
	if !injectClaudeFinalSystemCacheControlDoc(doc, messageIndex, text) {
		return payload
	}
	return doc.Bytes()
}

func injectClaudeFinalSystemCacheControlDoc(doc *ojson.Doc, messageIndex int, text string) bool {
	if doc == nil {
		return false
	}
	contentPath := fmt.Sprintf("messages.%d.content", messageIndex)
	newContent := "[" + buildTextBlock(text, &claudeCodeCacheControl) + "]"
	if errSet := doc.SetRawBytes(contentPath, []byte(newContent)); errSet != nil {
		log.Warnf("failed to inject cache_control into trailing system message: %v", errSet)
		return false
	}
	return true
}

func messageContentHasCacheControl(content ojson.Result) bool {
	if content.IsArray() {
		found := false
		content.ForEach(func(_, item ojson.Result) bool {
			if isValidClaudeCacheControl(item.Get("cache_control")) {
				found = true
				return false
			}
			return true
		})
		return found
	}
	return false
}

// injectToolsCacheControl adds cache_control to the last non-deferred tool in the tools array.
// Deferred tools cannot use prompt caching, so trailing deferred tools are skipped.
// This only adds cache_control if NO tool in the array already has it.
func injectToolsCacheControl(payload []byte) []byte {
	doc := ojson.NewDoc(payload)
	if !injectToolsCacheControlDoc(doc) {
		return payload
	}
	return doc.Bytes()
}

func injectToolsCacheControlDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	tools := doc.Get("tools")
	if !tools.IsArray() {
		return false
	}

	// Check if ANY tool already has cache_control and find the last eligible tool.
	hasCacheControlInTools := false
	lastEligibleToolIndex := -1
	idx := 0
	tools.ForEach(func(_, tool ojson.Result) bool {
		if isValidClaudeCacheControl(tool.Get("cache_control")) {
			hasCacheControlInTools = true
			return false
		}
		if !tool.Get("defer_loading").Bool() {
			lastEligibleToolIndex = idx
		}
		idx++
		return true
	})
	if hasCacheControlInTools || lastEligibleToolIndex < 0 {
		return false
	}

	lastToolPath := fmt.Sprintf("tools.%d.cache_control", lastEligibleToolIndex)
	if errSet := doc.Set(lastToolPath, claudeCodeCacheControl); errSet != nil {
		log.Warnf("failed to inject cache_control into tools array: %v", errSet)
		return false
	}

	return true
}

// injectSystemCacheControl adds cache_control to the last element in the system prompt.
// Converts string system prompts to array format if needed.
// This only adds cache_control if NO system element already has it.
func injectSystemCacheControl(payload []byte) []byte {
	doc := ojson.NewDoc(payload)
	if !injectSystemCacheControlDoc(doc) {
		return payload
	}
	return doc.Bytes()
}

func injectSystemCacheControlDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	system := doc.Get("system")
	if system.IsArray() {
		// Check if ANY system element already has cache_control and find the last cacheable element.
		hasCacheControlInSystem := false
		lastEligibleSystemIndex := -1
		idx := 0
		system.ForEach(func(_, item ojson.Result) bool {
			if isValidClaudeCacheControl(item.Get("cache_control")) {
				hasCacheControlInSystem = true
				return false
			}
			if claudeSystemBlockCanHostCacheControl(item) {
				lastEligibleSystemIndex = idx
			}
			idx++
			return true
		})
		if hasCacheControlInSystem || lastEligibleSystemIndex < 0 {
			return false
		}

		// Add cache_control to the last cacheable system element
		lastSystemPath := fmt.Sprintf("system.%d.cache_control", lastEligibleSystemIndex)
		if errSet := doc.Set(lastSystemPath, claudeCodeCacheControl); errSet != nil {
			log.Warnf("failed to inject cache_control into system array: %v", errSet)
			return false
		}
		return true
	}
	if !system.Exists() {
		return false
	}
	if system.Type == ojson.String {
		// Empty/blank strings are not cacheable hosts. claudePayloadHasCacheableSystem
		// already treats them as missing so tools can cover the prefix; converting them
		// here would create a second, useless breakpoint on whitespace.
		if strings.TrimSpace(system.String()) == "" {
			return false
		}
		// Convert string system prompt to an ordered native text block.
		newSystem := "[" + buildTextBlock(system.String(), &claudeCodeCacheControl) + "]"
		if errSet := doc.SetRawBytes("system", []byte(newSystem)); errSet != nil {
			log.Warnf("failed to inject cache_control into system string: %v", errSet)
			return false
		}
		return true
	}

	return false
}

func ensureModelMaxTokens(body []byte, modelID string) []byte {
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !ensureModelMaxTokensDoc(doc, modelID) {
		return body
	}
	return doc.Bytes()
}

func ensureModelMaxTokensDoc(doc *ojson.Doc, modelID string) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}

	if maxTokens := doc.Get("max_tokens"); maxTokens.Exists() {
		return false
	}

	for _, provider := range registry.GetGlobalRegistry().GetModelProviders(strings.TrimSpace(modelID)) {
		if strings.EqualFold(provider, "claude") {
			maxTokens := defaultModelMaxTokens
			if info := registry.GetGlobalRegistry().GetModelInfo(strings.TrimSpace(modelID), "claude"); info != nil && info.MaxCompletionTokens > 0 {
				maxTokens = info.MaxCompletionTokens
			}
			return doc.Set("max_tokens", maxTokens) == nil
		}
	}

	return false
}
