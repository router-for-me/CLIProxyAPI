package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

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

	metadata := gjson.GetBytes(payload, "metadata")
	if !metadata.Exists() {
		userID, errUserID := generateID()
		if errUserID != nil {
			return nil, errUserID
		}
		payload, _ = sjson.SetBytes(payload, "metadata.user_id", userID)
		return payload, nil
	}

	existingUserID := gjson.GetBytes(payload, "metadata.user_id").String()
	if existingUserID == "" || !helps.IsValidUserID(existingUserID) {
		userID, errUserID := generateID()
		if errUserID != nil {
			return nil, errUserID
		}
		payload, _ = sjson.SetBytes(payload, "metadata.user_id", userID)
	}
	return payload, nil
}

// fingerprintSalt is the salt used by Claude Code to compute the 3-char build fingerprint.
const fingerprintSalt = "59cf53e54c78"

// computeFingerprint computes the 3-char build fingerprint that Claude Code embeds in cc_version.
// Algorithm: SHA256(salt + messageText[4] + messageText[7] + messageText[20] + version)[:3]
func computeFingerprint(messageText, version string) string {
	indices := [3]int{4, 7, 20}
	runes := []rune(messageText)
	var sb strings.Builder
	for _, idx := range indices {
		if idx < len(runes) {
			sb.WriteRune(runes[idx])
		} else {
			sb.WriteRune('0')
		}
	}
	input := fingerprintSalt + sb.String() + version
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:3]
}

// generateBillingHeader creates the x-anthropic-billing-header text block that
// Claude Code prepends to its system prompt. cch is present only on signed paths.
func generateBillingHeader(cchSigning bool, version, messageText, entrypoint, workload string) string {
	if entrypoint == "" {
		entrypoint = "cli"
	}
	buildHash := computeFingerprint(messageText, version)
	workloadPart := ""
	if workload != "" {
		workloadPart = fmt.Sprintf(" cc_workload=%s;", workload)
	}

	if cchSigning {
		return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; cch=00000;%s", version, buildHash, entrypoint, workloadPart)
	}
	return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s;%s", version, buildHash, entrypoint, workloadPart)
}

func claudeBillingFingerprintMessageText(payload []byte) string {
	messageText := ""
	gjson.GetBytes(payload, "messages").ForEach(func(_, message gjson.Result) bool {
		if message.Get("role").String() != "user" {
			return true
		}
		content := message.Get("content")
		candidate := ""
		if content.Type == gjson.String {
			candidate = content.String()
		} else if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "text" {
					candidate = part.Get("text").String()
				}
				return true
			})
		}
		if candidate != "" {
			messageText = candidate
		}
		return true
	})
	return messageText
}

func claudeCCHFallbackBillingHeader(ctx context.Context, cfg *config.Config, payload []byte, entrypoint string) string {
	return generateBillingHeader(
		true,
		helps.DefaultClaudeVersion(cfg),
		claudeBillingFingerprintMessageText(payload),
		entrypoint,
		getWorkloadFromContext(ctx),
	)
}

const claudeCodeCLIIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

func checkSystemInstructionsWithMode(payload []byte, strictMode bool) []byte {
	return checkSystemInstructionsWithSigningMode(payload, strictMode, false, "2.1.220", "cli", "")
}

// checkSystemInstructionsWithSigningMode keeps the top-level system in Claude
// Code's minimal CLI shape. Each caller system block is preserved as a separate
// mid-conversation system message after the first user turn, where supported
// Claude models give it operator-level authority without changing the cached
// top-level prefix.
func checkSystemInstructionsWithSigningMode(payload []byte, strictMode bool, cchSigning bool, version, entrypoint, workload string) []byte {
	return checkSystemInstructionsWithSigningModeAt(payload, strictMode, cchSigning, version, entrypoint, workload, time.Now())
}

func checkSystemInstructionsWithSigningModeAt(payload []byte, strictMode bool, cchSigning bool, version, entrypoint, workload string, now time.Time) []byte {
	system := gjson.GetBytes(payload, "system")
	messageText := claudeBillingFingerprintMessageText(payload)

	billingText := generateBillingHeader(cchSigning, version, messageText, entrypoint, workload)
	billingBlock := buildTextBlock(billingText, nil)
	agentBlock := buildTextBlock(claudeCodeCLIIdentity, &claudeCodeCacheControl)
	payload, _ = sjson.SetRawBytes(payload, "system", []byte("["+billingBlock+","+agentBlock+"]"))
	if strictMode {
		return injectClaudeCodeCurrentDate(payload, now)
	}

	forwardedSystemBlocks := collectForwardedClaudeSystemPromptBlocks(system)
	if len(forwardedSystemBlocks) == 0 {
		return injectClaudeCodeCurrentDate(payload, now)
	}
	if claudeUsesLegacySystemReminder(payload) {
		payload = prependClaudeSystemRemindersToFirstUserMessage(payload, forwardedSystemBlocks)
	} else {
		// Unknown and future model IDs optimistically use the authoritative
		// mid-conversation system role. Only empirically unsupported legacy IDs
		// stay on the user-reminder compatibility path.
		payload = insertClaudeMidConversationSystemMessages(payload, forwardedSystemBlocks)
	}
	return injectClaudeCodeCurrentDate(payload, now)
}

// relocateClaudeSystemPromptForCountTokens keeps a cloaked count_tokens request
// in Claude Code's measured shape, which carries only model, messages and tools.
// The Claude Code system blocks are therefore not installed here, but each caller
// system block still has to be accounted for, so it is relocated into messages
// using the same positional mapping as the Messages path. That keeps the counted
// tokens aligned with the request the caller is about to send while preventing a
// third-party system prompt from reaching Anthropic in the system slot.
func relocateClaudeSystemPromptForCountTokens(payload []byte, strictMode bool) []byte {
	system := gjson.GetBytes(payload, "system")
	if !system.Exists() {
		return payload
	}
	// Strict mode drops caller prompts on the Messages path, so it must not
	// reintroduce them here either.
	var forwardedSystemBlocks []string
	if !strictMode {
		forwardedSystemBlocks = collectForwardedClaudeSystemPromptBlocks(system)
	}
	updated, errDelete := sjson.DeleteBytes(payload, "system")
	if errDelete != nil {
		return payload
	}
	payload = updated
	if len(forwardedSystemBlocks) == 0 {
		return payload
	}
	if claudeUsesLegacySystemReminder(payload) {
		return prependClaudeSystemRemindersToFirstUserMessage(payload, forwardedSystemBlocks)
	}
	return insertClaudeMidConversationSystemMessages(payload, forwardedSystemBlocks)
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
	model := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "model").String()))
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
	return newClaudeMidSystemMessageModelError(gjson.GetBytes(payload, "model").String())
}

// validateClaudeCallerSystemBlocks rejects caller system content that cannot keep
// its operator authority. Verified against api.anthropic.com on 2026-08-03: the
// top-level system field answers "system.<i>.type: Input should be 'text'" for
// image, document and unknown block types, and a role=system message answers
// "role 'system' supports text, tool_addition, and tool_removal blocks only".
// Cloaking relocates caller blocks into one of those two slots, so a non-text
// block has no destination. Failing here keeps the caller's instructions from
// being silently dropped, and costs no upstream attempt.
func validateClaudeCallerSystemBlocks(system gjson.Result) error {
	if !system.IsArray() {
		// A string system prompt is text by definition.
		return nil
	}
	var blockErr error
	index := 0
	system.ForEach(func(_, part gjson.Result) bool {
		if strings.TrimSpace(part.Get("type").String()) != "text" {
			blockErr = newClaudeCallerSystemBlockError(index, strings.TrimSpace(part.Get("type").String()))
			return false
		}
		index++
		return true
	})
	return blockErr
}

func collectForwardedClaudeSystemPromptBlocks(system gjson.Result) []string {
	var blocks []string
	appendText := func(text string) {
		if strings.TrimSpace(text) == "" || util.IsClaudeCodeAttributionSystemText(text) || text == claudeCodeCLIIdentity {
			return
		}
		blocks = append(blocks, text)
	}

	if system.IsArray() {
		system.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "text" {
				appendText(part.Get("text").String())
			}
			return true
		})
	} else if system.Type == gjson.String {
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
	firstUserIdx := firstClaudeUserMessageIndex(payload)
	if firstUserIdx < 0 || len(texts) == 0 {
		return payload
	}

	reminderTexts := make([]string, 0, len(texts))
	for _, text := range texts {
		reminderTexts = append(reminderTexts, claudeCallerSystemReminder(text))
	}

	contentPath := fmt.Sprintf("messages.%d.content", firstUserIdx)
	content := gjson.GetBytes(payload, contentPath)
	if content.IsArray() {
		blocks := content.Array()
		existing := make(map[string]int, len(blocks))
		for _, block := range blocks {
			if block.Get("type").String() == "text" {
				existing[block.Get("text").String()]++
			}
		}

		reminderBlocks := make([]string, 0, len(reminderTexts))
		for _, reminderText := range reminderTexts {
			if existing[reminderText] > 0 {
				existing[reminderText]--
				continue
			}
			reminderBlocks = append(reminderBlocks, buildTextBlock(reminderText, nil))
		}
		if len(reminderBlocks) == 0 {
			return payload
		}

		insertAt := 0
		for insertAt < len(blocks) && blocks[insertAt].Get("type").String() == "tool_result" {
			insertAt++
		}
		rawBlocks := make([]string, 0, len(blocks)+len(reminderBlocks))
		for idx, block := range blocks {
			if idx == insertAt {
				rawBlocks = append(rawBlocks, reminderBlocks...)
			}
			rawBlocks = append(rawBlocks, block.Raw)
		}
		if insertAt == len(blocks) {
			rawBlocks = append(rawBlocks, reminderBlocks...)
		}
		payload, _ = sjson.SetRawBytes(payload, contentPath, []byte("["+strings.Join(rawBlocks, ",")+"]"))
	} else if content.Type == gjson.String {
		rawBlocks := make([]string, 0, len(reminderTexts)+1)
		for _, reminderText := range reminderTexts {
			rawBlocks = append(rawBlocks, buildTextBlock(reminderText, nil))
		}
		rawBlocks = append(rawBlocks, buildTextBlock(content.String(), nil))
		payload, _ = sjson.SetRawBytes(payload, contentPath, []byte("["+strings.Join(rawBlocks, ",")+"]"))
	}
	return payload
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

func insertClaudeMidConversationSystemMessages(payload []byte, texts []string) []byte {
	firstUserIdx := firstClaudeUserMessageIndex(payload)
	if firstUserIdx < 0 || len(texts) == 0 {
		return payload
	}

	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}
	messageBlocks := messages.Array()
	insertAt := firstUserIdx + 1
	for insertAt < len(messageBlocks) && messageBlocks[insertAt].Get("role").String() == "user" {
		insertAt++
	}
	if len(messageBlocks)-insertAt >= len(texts) {
		matches := true
		for idx, text := range texts {
			message := messageBlocks[insertAt+idx]
			if message.Get("role").String() != "system" || claudeMessageContentText(message.Get("content")) != text {
				matches = false
				break
			}
		}
		if matches {
			return payload
		}
	}

	systemMessages := make([]string, 0, len(texts))
	for _, text := range texts {
		content := "[" + buildTextBlock(text, &claudeCodeCacheControl) + "]"
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
	payload, _ = sjson.SetRawBytes(payload, "messages", []byte("["+strings.Join(rawMessages, ",")+"]"))
	return payload
}

func claudeMessageContentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	content.ForEach(func(_, block gjson.Result) bool {
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

// captureClaudeCodeSystemPlacement records CPA's modern-model system placement
// immediately after cloaking. The message-count increase is part of the proof:
// insertClaudeMidConversationSystemMessages returns without inserting when the
// same turns already exist, and those pre-existing turns belong to the caller.
func captureClaudeCodeSystemPlacement(before, after []byte, cloaked bool) claudeCodeSystemPlacementState {
	if !cloaked || claudeUsesLegacySystemReminder(before) {
		return claudeCodeSystemPlacementState{}
	}
	texts := collectForwardedClaudeSystemPromptBlocks(gjson.GetBytes(before, "system"))
	if len(texts) == 0 {
		return claudeCodeSystemPlacementState{}
	}

	beforeMessages := gjson.GetBytes(before, "messages").Array()
	afterMessages := gjson.GetBytes(after, "messages").Array()
	if len(afterMessages) != len(beforeMessages)+len(texts) {
		return claudeCodeSystemPlacementState{}
	}
	firstUserIdx := firstClaudeUserMessageIndex(before)
	if firstUserIdx < 0 {
		return claudeCodeSystemPlacementState{}
	}
	insertAt := firstUserIdx + 1
	for insertAt < len(beforeMessages) && beforeMessages[insertAt].Get("role").String() == "user" {
		insertAt++
	}
	if insertAt+len(texts) > len(afterMessages) {
		return claudeCodeSystemPlacementState{}
	}

	insertedRaw := make([]string, len(texts))
	for idx, text := range texts {
		message := afterMessages[insertAt+idx]
		if message.Get("role").String() != "system" || claudeMessageContentText(message.Get("content")) != text {
			return claudeCodeSystemPlacementState{}
		}
		insertedRaw[idx] = message.Raw
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
	if len(state.insertedRaw) == 0 || !claudeUsesLegacySystemReminder(payload) {
		return payload
	}
	messages := gjson.GetBytes(payload, "messages").Array()
	if state.insertAt < 0 || state.insertAt+len(state.insertedRaw) > len(messages) {
		return payload
	}
	for idx, raw := range state.insertedRaw {
		if messages[state.insertAt+idx].Raw != raw {
			return payload
		}
	}

	rawMessages := make([]string, 0, len(messages)-len(state.insertedRaw))
	for idx, message := range messages {
		if idx >= state.insertAt && idx < state.insertAt+len(state.insertedRaw) {
			continue
		}
		rawMessages = append(rawMessages, message.Raw)
	}
	updated, errSet := sjson.SetRawBytes(payload, "messages", []byte("["+strings.Join(rawMessages, ",")+"]"))
	if errSet != nil {
		return payload
	}
	return prependClaudeSystemRemindersToFirstUserMessage(updated, state.texts)
}

// claudeCodeLocalDate reproduces Claude Code 2.1.220's wcs() helper:
// new Date(), local calendar fields, and zero-padded YYYY-MM-DD components.
func claudeCodeLocalDate(now time.Time) string {
	year, month, day := now.Date()
	return fmt.Sprintf("%04d-%02d-%02d", year, int(month), day)
}

func claudeCodeCurrentTime(cfg *config.Config, auth *cliproxyauth.Auth) time.Time {
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

func claudeCodeCurrentDateReminder(now time.Time) string {
	return fmt.Sprintf(`<system-reminder>
As you answer the user's questions, you can use the following context:
# currentDate
Today's date is %s.

      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>

`, claudeCodeLocalDate(now))
}

func firstClaudeUserMessageIndex(payload []byte) int {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return -1
	}

	firstUserIdx := -1
	messages.ForEach(func(idx, msg gjson.Result) bool {
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

func isClaudeCodeCurrentDateReminder(text string) bool {
	return strings.HasPrefix(text, "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is ")
}

func injectClaudeCodeCurrentDate(payload []byte, now time.Time) []byte {
	firstUserIdx := firstClaudeUserMessageIndex(payload)
	if firstUserIdx < 0 {
		return payload
	}

	contentPath := fmt.Sprintf("messages.%d.content", firstUserIdx)
	content := gjson.GetBytes(payload, contentPath)
	dateText := claudeCodeCurrentDateReminder(now)
	dateBlock := buildTextBlock(dateText, nil)

	if content.Type == gjson.String {
		userBlock := buildTextBlock(content.String(), &claudeCodeCacheControl)
		newArray := "[" + dateBlock + "," + userBlock + "]"
		payload, _ = sjson.SetRawBytes(payload, contentPath, []byte(newArray))
		return payload
	}
	if !content.IsArray() {
		return payload
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
			if !actualTextCached && !isClaudeCodeContextReminder(text) {
				rawBlocks = append(rawBlocks, withEphemeralCacheControl(block.Raw))
				actualTextCached = true
				continue
			}
		}
		rawBlocks = append(rawBlocks, block.Raw)
	}

	rawBlocks = append(rawBlocks, "")
	copy(rawBlocks[1:], rawBlocks)
	rawBlocks[0] = dateBlock
	payload, _ = sjson.SetRawBytes(payload, contentPath, []byte("["+strings.Join(rawBlocks, ",")+"]"))
	return payload
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
	switch gjson.GetBytes(payload, "thinking.type").String() {
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
	if gjson.GetBytes(payload, "context_management").Exists() {
		return payload, false
	}
	if !claudeThinkingAcceptsClearThinking(payload) {
		return payload, false
	}
	updated, err := sjson.SetRawBytes(payload, "context_management", []byte(claudeCodeContextManagement))
	if err != nil {
		return payload, false
	}
	return updated, true
}

type claudeCodeContextManagementState struct {
	eligible              bool
	callerOwned           bool
	automaticallyInjected bool
	payloadRuleTouched    bool
}

// reconcileClaudeCodeContextManagement resolves automatic ownership after all
// payload rules and forced tool-choice processing have completed.
func reconcileClaudeCodeContextManagement(payload []byte, state claudeCodeContextManagementState) []byte {
	contextManagement := gjson.GetBytes(payload, "context_management")

	// Any thinking value the strategy does not accept must drop an object CPA
	// injected itself. disableThinkingIfToolChoiceForced deletes the whole
	// thinking field after injection, so this also covers a request that was
	// still eligible when injectClaudeCodeContextManagement ran.
	if !claudeThinkingAcceptsClearThinking(payload) {
		if state.callerOwned || !state.automaticallyInjected || state.payloadRuleTouched {
			return payload
		}
		if contextManagement.Raw != claudeCodeContextManagement {
			return payload
		}
		updated, err := sjson.DeleteBytes(payload, "context_management")
		if err != nil {
			return payload
		}
		return updated
	}

	if !state.eligible || state.callerOwned || state.payloadRuleTouched || contextManagement.Exists() {
		return payload
	}
	updated, err := sjson.SetRawBytes(payload, "context_management", []byte(claudeCodeContextManagement))
	if err != nil {
		return payload
	}
	return updated
}

// withEphemeralCacheControl stamps the native Claude Code default cache marker
// {"type":"ephemeral"} onto a content block. A 1h ttl is not part of the default
// shape; upgradeClaudeCacheControlTTL adds it for the credentials native uses it
// on, after all placement decisions are final.
func withEphemeralCacheControl(rawBlock string) string {
	updated, err := sjson.SetRawBytes([]byte(rawBlock), "cache_control", []byte(`{"type":"ephemeral"}`))
	if err != nil {
		return rawBlock
	}
	return string(updated)
}

type claudeWirePolicy struct {
	OAuth               bool
	ConfirmedClaudeCode bool
	Cloak               bool
}

type claudeCloakSettings struct {
	strictMode     bool
	sensitiveWords []string
	cacheUserID    bool
}

func resolveClaudeWirePolicy(cfg *config.Config, auth *cliproxyauth.Auth, apiKey string, confirmedClaudeCode bool) (claudeWirePolicy, claudeCloakSettings) {
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
	if attrMode != "" {
		cloakMode = attrMode
	}
	if cloakCfg != nil {
		if mode := strings.TrimSpace(cloakCfg.Mode); mode != "" {
			cloakMode = mode
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

	policy := claudeWirePolicy{
		OAuth:               isClaudeOAuthToken(apiKey),
		ConfirmedClaudeCode: confirmedClaudeCode,
		Cloak:               !confirmedClaudeCode,
	}
	if confirmedClaudeCode {
		// Native Claude Code is always a passthrough client. An operator-level
		// "always" mode may cloak unknown callers, but must not overwrite a
		// strongly confirmed CLI, sdk-cli, or claude-vscode fingerprint.
		policy.Cloak = false
		return policy, settings
	}
	switch strings.ToLower(strings.TrimSpace(cloakMode)) {
	case "always":
		policy.Cloak = true
	case "never":
		policy.Cloak = false
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
	policy, settings := resolveClaudeWirePolicy(cfg, auth, apiKey, confirmedClaudeCode)
	if !policy.Cloak {
		return payload, false, nil
	}
	// Strict mode drops caller system prompts entirely, so nothing needs a
	// destination and an unusable block cannot lose information.
	if !settings.strictMode {
		if errSystem := validateClaudeCallerSystemBlocks(gjson.GetBytes(payload, "system")); errSystem != nil {
			return nil, false, errSystem
		}
	}

	billingVersion := helps.DefaultClaudeVersion(cfg)
	workload := getWorkloadFromContext(ctx)
	payload = checkSystemInstructionsWithSigningModeAt(payload, settings.strictMode, cchSigning, billingVersion, "cli", workload, claudeCodeCurrentTime(cfg, auth))

	// OAuth metadata is rewritten after credential selection and all remaining
	// body mutations. Non-OAuth cloaking keeps the legacy generated identity.
	if !policy.OAuth {
		var errFakeUserID error
		payload, errFakeUserID = injectFakeUserID(ctx, payload, apiKey, settings.cacheUserID)
		if errFakeUserID != nil {
			return nil, false, errFakeUserID
		}
	}

	// Apply sensitive word obfuscation
	if len(settings.sensitiveWords) > 0 {
		matcher := helps.BuildSensitiveWordMatcher(settings.sensitiveWords)
		payload = helps.ObfuscateSensitiveWords(payload, matcher)
	}

	return payload, true, nil
}

type claudeCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
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
	if !claudePayloadHasCacheableSystem(payload) {
		payload = injectToolsCacheControl(payload)
	}
	payload = injectSystemCacheControl(payload)
	payload = injectMessagesCacheControl(payload)
	return payload
}

// claudePayloadHasCacheableSystem reports whether the payload has a system prompt
// that injectSystemCacheControl can actually host a breakpoint on. An absent key, an
// empty array and an empty string all leave the tools prefix uncovered.
func claudePayloadHasCacheableSystem(payload []byte) bool {
	system := gjson.GetBytes(payload, "system")
	switch {
	case !system.Exists():
		return false
	case system.IsArray():
		return system.Get("#").Int() > 0
	case system.Type == gjson.String:
		return strings.TrimSpace(system.String()) != ""
	default:
		return false
	}
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
	if ttl == "" || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	upgrade := func(path string, block gjson.Result) {
		cacheControl := block.Get("cache_control")
		if !cacheControl.IsObject() || cacheControl.Get("ttl").Exists() {
			return
		}
		blockType := cacheControl.Get("type")
		if blockType.Type != gjson.String {
			return
		}
		// Rebuild the object so the native {type, ttl, scope} key order survives
		// instead of appending ttl after a caller-supplied scope.
		upgraded := `{"type":` + marshalJSONStringWithoutHTMLEscape(blockType.String()) +
			`,"ttl":` + marshalJSONStringWithoutHTMLEscape(ttl)
		if scope := cacheControl.Get("scope"); scope.Exists() {
			upgraded += `,"scope":` + scope.Raw
		}
		upgraded += "}"
		updated, errSet := sjson.SetRawBytes(payload, path+".cache_control", []byte(upgraded))
		if errSet != nil {
			return
		}
		payload = updated
	}

	forEachClaudeCacheControlBlock(payload, upgrade)
	return payload
}

// forEachClaudeCacheControlBlock walks every block that can carry cache_control
// in Anthropic's evaluation order: tools, then system, then messages.
func forEachClaudeCacheControlBlock(payload []byte, visit func(path string, block gjson.Result)) {
	if tools := gjson.GetBytes(payload, "tools"); tools.IsArray() {
		tools.ForEach(func(idx, item gjson.Result) bool {
			visit(fmt.Sprintf("tools.%d", int(idx.Int())), item)
			return true
		})
	}
	if system := gjson.GetBytes(payload, "system"); system.IsArray() {
		system.ForEach(func(idx, item gjson.Result) bool {
			visit(fmt.Sprintf("system.%d", int(idx.Int())), item)
			return true
		})
	}
	if messages := gjson.GetBytes(payload, "messages"); messages.IsArray() {
		messages.ForEach(func(msgIdx, message gjson.Result) bool {
			content := message.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(itemIdx, item gjson.Result) bool {
				visit(fmt.Sprintf("messages.%d.content.%d", int(msgIdx.Int()), int(itemIdx.Int())), item)
				return true
			})
			return true
		})
	}
}

func shouldEnsureCacheControl(payload []byte, cloaked, confirmedClaudeCode bool) bool {
	return !confirmedClaudeCode && (cloaked || countCacheControls(payload) == 0)
}

func countCacheControls(payload []byte) int {
	count := 0

	// Check system
	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				count++
			}
			return true
		})
	}

	// Check tools
	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				count++
			}
			return true
		})
	}

	// Check messages
	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, item gjson.Result) bool {
					if item.Get("cache_control").Exists() {
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
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	original := payload
	seen5m := false
	modified := false

	processBlock := func(path string, obj gjson.Result) {
		cc := obj.Get("cache_control")
		if !cc.Exists() {
			return
		}
		if !cc.IsObject() {
			seen5m = true
			return
		}
		ttl := cc.Get("ttl")
		if ttl.Type != gjson.String || ttl.String() != "1h" {
			seen5m = true
			return
		}
		if !seen5m {
			return
		}
		ttlPath := path + ".cache_control.ttl"
		updated, errDel := sjson.DeleteBytes(payload, ttlPath)
		if errDel != nil {
			return
		}
		payload = updated
		modified = true
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(idx, item gjson.Result) bool {
			processBlock(fmt.Sprintf("tools.%d", int(idx.Int())), item)
			return true
		})
	}

	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(idx, item gjson.Result) bool {
			processBlock(fmt.Sprintf("system.%d", int(idx.Int())), item)
			return true
		})
	}

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(msgIdx, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(itemIdx, item gjson.Result) bool {
				processBlock(fmt.Sprintf("messages.%d.content.%d", int(msgIdx.Int()), int(itemIdx.Int())), item)
				return true
			})
			return true
		})
	}

	if !modified {
		return original
	}
	return payload
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
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	total := countCacheControls(payload)
	if total <= maxBlocks {
		return payload
	}

	excess := total - maxBlocks

	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		lastIdx := -1
		system.ForEach(func(idx, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				lastIdx = int(idx.Int())
			}
			return true
		})
		if lastIdx >= 0 {
			system.ForEach(func(idx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				i := int(idx.Int())
				if i == lastIdx {
					return true
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("system.%d.cache_control", i)
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
		}
	}
	if excess <= 0 {
		return payload
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		lastIdx := -1
		tools.ForEach(func(idx, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				lastIdx = int(idx.Int())
			}
			return true
		})
		if lastIdx >= 0 {
			tools.ForEach(func(idx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				i := int(idx.Int())
				if i == lastIdx {
					return true
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("tools.%d.cache_control", i)
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
		}
	}
	if excess <= 0 {
		return payload
	}

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(msgIdx, msg gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(itemIdx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("messages.%d.content.%d.cache_control", int(msgIdx.Int()), int(itemIdx.Int()))
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
			return true
		})
	}
	if excess <= 0 {
		return payload
	}

	system = gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(idx, item gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			if !item.Get("cache_control").Exists() {
				return true
			}
			path := fmt.Sprintf("system.%d.cache_control", int(idx.Int()))
			updated, errDel := sjson.DeleteBytes(payload, path)
			if errDel != nil {
				return true
			}
			payload = updated
			excess--
			return true
		})
	}
	if excess <= 0 {
		return payload
	}

	tools = gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(idx, item gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			if !item.Get("cache_control").Exists() {
				return true
			}
			path := fmt.Sprintf("tools.%d.cache_control", int(idx.Int()))
			updated, errDel := sjson.DeleteBytes(payload, path)
			if errDel != nil {
				return true
			}
			payload = updated
			excess--
			return true
		})
	}

	return payload
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
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}

	lastMessageIndex := int(messages.Get("#").Int()) - 1
	lastEligibleIndex := -1
	messages.ForEach(func(index gjson.Result, message gjson.Result) bool {
		if role := message.Get("role").String(); role != "user" && role != "assistant" {
			return true
		}
		if claudeMessageEligibleForRollingCache(message) {
			lastEligibleIndex = int(index.Int())
		}
		return true
	})

	if lastEligibleIndex >= 0 {
		finalMessage := messages.Get(fmt.Sprintf("%d", lastMessageIndex))
		finalContent := finalMessage.Get("content")
		if finalMessage.Get("role").String() == "system" &&
			finalContent.Type == gjson.String &&
			strings.TrimSpace(finalContent.String()) != "" {
			return injectClaudeFinalSystemCacheControl(payload, lastMessageIndex, finalContent.String())
		}
	}
	if lastEligibleIndex < 0 {
		return payload
	}

	contentPath := fmt.Sprintf("messages.%d.content", lastEligibleIndex)
	content := gjson.GetBytes(payload, contentPath)
	if messageContentHasCacheControl(content) {
		return payload
	}

	if content.IsArray() {
		contentCount := int(content.Get("#").Int())
		if contentCount > 0 {
			cacheControlPath := fmt.Sprintf("messages.%d.content.%d.cache_control", lastEligibleIndex, contentCount-1)
			result, err := sjson.SetBytes(payload, cacheControlPath, claudeCodeCacheControl)
			if err != nil {
				log.Warnf("failed to inject cache_control into messages: %v", err)
				return payload
			}
			payload = result
		}
	} else if content.Type == gjson.String {
		newContent := "[" + buildTextBlock(content.String(), &claudeCodeCacheControl) + "]"
		result, err := sjson.SetRawBytes(payload, contentPath, []byte(newContent))
		if err != nil {
			log.Warnf("failed to inject cache_control into message string content: %v", err)
			return payload
		}
		payload = result
	}

	return payload
}

// claudeMessageEligibleForRollingCache reports whether the native selector would
// consider this user/assistant turn as a rolling breakpoint host. Native rejects
// an assistant turn whose last content block is thinking-like, because a thinking
// block cannot host the marker.
func claudeMessageEligibleForRollingCache(message gjson.Result) bool {
	content := message.Get("content")
	if content.Type == gjson.String {
		return true
	}
	if !content.IsArray() || content.Get("#").Int() == 0 {
		return false
	}
	if message.Get("role").String() != "assistant" {
		return true
	}
	lastBlock := content.Get(fmt.Sprintf("%d", content.Get("#").Int()-1))
	switch lastBlock.Get("type").String() {
	case "thinking", "redacted_thinking":
		return false
	default:
		return true
	}
}

// injectClaudeFinalSystemCacheControl reproduces the native final-system special
// case, which replaces the string content with a single marked text block.
func injectClaudeFinalSystemCacheControl(payload []byte, messageIndex int, text string) []byte {
	contentPath := fmt.Sprintf("messages.%d.content", messageIndex)
	newContent := "[" + buildTextBlock(text, &claudeCodeCacheControl) + "]"
	result, err := sjson.SetRawBytes(payload, contentPath, []byte(newContent))
	if err != nil {
		log.Warnf("failed to inject cache_control into trailing system message: %v", err)
		return payload
	}
	return result
}

func messageContentHasCacheControl(content gjson.Result) bool {
	if content.IsArray() {
		found := false
		content.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
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
	tools := gjson.GetBytes(payload, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return payload
	}

	// Check if ANY tool already has cache_control and find the last eligible tool.
	hasCacheControlInTools := false
	lastEligibleToolIndex := -1
	tools.ForEach(func(index, tool gjson.Result) bool {
		if tool.Get("cache_control").Exists() {
			hasCacheControlInTools = true
			return false
		}
		if !tool.Get("defer_loading").Bool() {
			lastEligibleToolIndex = int(index.Int())
		}
		return true
	})
	if hasCacheControlInTools || lastEligibleToolIndex < 0 {
		return payload
	}

	lastToolPath := fmt.Sprintf("tools.%d.cache_control", lastEligibleToolIndex)
	result, err := sjson.SetBytes(payload, lastToolPath, claudeCodeCacheControl)
	if err != nil {
		log.Warnf("failed to inject cache_control into tools array: %v", err)
		return payload
	}

	return result
}

// injectSystemCacheControl adds cache_control to the last element in the system prompt.
// Converts string system prompts to array format if needed.
// This only adds cache_control if NO system element already has it.
func injectSystemCacheControl(payload []byte) []byte {
	system := gjson.GetBytes(payload, "system")
	if !system.Exists() {
		return payload
	}

	if system.IsArray() {
		count := int(system.Get("#").Int())
		if count == 0 {
			return payload
		}

		// Check if ANY system element already has cache_control
		hasCacheControlInSystem := false
		system.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				hasCacheControlInSystem = true
				return false
			}
			return true
		})
		if hasCacheControlInSystem {
			return payload
		}

		// Add cache_control to the last system element
		lastSystemPath := fmt.Sprintf("system.%d.cache_control", count-1)
		result, err := sjson.SetBytes(payload, lastSystemPath, claudeCodeCacheControl)
		if err != nil {
			log.Warnf("failed to inject cache_control into system array: %v", err)
			return payload
		}
		payload = result
	} else if system.Type == gjson.String {
		// Empty/blank strings are not cacheable hosts. claudePayloadHasCacheableSystem
		// already treats them as missing so tools can cover the prefix; converting them
		// here would create a second, useless breakpoint on whitespace.
		if strings.TrimSpace(system.String()) == "" {
			return payload
		}
		// Convert string system prompt to an ordered native text block.
		newSystem := "[" + buildTextBlock(system.String(), &claudeCodeCacheControl) + "]"
		result, err := sjson.SetRawBytes(payload, "system", []byte(newSystem))
		if err != nil {
			log.Warnf("failed to inject cache_control into system string: %v", err)
			return payload
		}
		payload = result
	}

	return payload
}

func ensureModelMaxTokens(body []byte, modelID string) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}

	if maxTokens := gjson.GetBytes(body, "max_tokens"); maxTokens.Exists() {
		return body
	}

	for _, provider := range registry.GetGlobalRegistry().GetModelProviders(strings.TrimSpace(modelID)) {
		if strings.EqualFold(provider, "claude") {
			maxTokens := defaultModelMaxTokens
			if info := registry.GetGlobalRegistry().GetModelInfo(strings.TrimSpace(modelID), "claude"); info != nil && info.MaxCompletionTokens > 0 {
				maxTokens = info.MaxCompletionTokens
			}
			body, _ = sjson.SetBytes(body, "max_tokens", maxTokens)
			return body
		}
	}

	return body
}
