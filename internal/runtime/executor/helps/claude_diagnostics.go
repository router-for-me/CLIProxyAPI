package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	claudeDiagnosticsTTL            = time.Hour
	claudeDiagnosticsCleanupPeriod  = 15 * time.Minute
	claudeDiagnosticsMaxEntries     = 4096
	claudeDiagnosticsEvictBatchSize = 256
)

var (
	claudeRequestIDPattern = regexp.MustCompile(`^req_[A-Za-z0-9_-]{1,36}$`)
	claudePromptIDPattern  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type claudeContextKey string

const (
	claudeSessionIDContextKey       claudeContextKey = "cpa_claude_session_id"
	claudeContinuityContextKey      claudeContextKey = "cpa_claude_continuity_ctx"
	claudeIncomingHeadersContextKey claudeContextKey = "cpa_claude_incoming_headers"
)

// WithIncomingHeaders attaches incoming HTTP headers to ctx.
func WithIncomingHeaders(ctx context.Context, headers http.Header) context.Context {
	if headers == nil {
		return ctx
	}
	return context.WithValue(ctx, claudeIncomingHeadersContextKey, headers)
}

// IncomingHeadersFromContext retrieves incoming HTTP headers from ctx, if present.
func IncomingHeadersFromContext(ctx context.Context) http.Header {
	if ctx == nil {
		return nil
	}
	if h, ok := ctx.Value(claudeIncomingHeadersContextKey).(http.Header); ok {
		return h
	}
	return nil
}

// ClaudeContinuityContext holds request-scoped continuity state across cloaking and execution.
type ClaudeContinuityContext struct {
	Key               string
	Sequence          uint64
	PreviousMessageID string
	PreviousRequestID string
	PromptID          string
	Initialized       bool
}

// WithClaudeSessionID attaches a known Claude session ID to ctx.
func WithClaudeSessionID(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, claudeSessionIDContextKey, sessionID)
}

// ClaudeSessionIDFromContext retrieves the Claude session ID from ctx, if present.
func ClaudeSessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if val, ok := ctx.Value(claudeSessionIDContextKey).(string); ok {
		return val
	}
	return ""
}

// WithClaudeContinuityContext attaches a mutable ClaudeContinuityContext to ctx.
func WithClaudeContinuityContext(ctx context.Context, cc *ClaudeContinuityContext) context.Context {
	if cc == nil {
		return ctx
	}
	return context.WithValue(ctx, claudeContinuityContextKey, cc)
}

// ClaudeContinuityContextFromContext retrieves the ClaudeContinuityContext from ctx, if present.
func ClaudeContinuityContextFromContext(ctx context.Context) *ClaudeContinuityContext {
	if ctx == nil {
		return nil
	}
	if cc, ok := ctx.Value(claudeContinuityContextKey).(*ClaudeContinuityContext); ok {
		return cc
	}
	return nil
}

type claudeDiagnosticsEntry struct {
	previousMessageID string
	previousRequestID string
	promptID          string
	minimumSequence   uint64
	committedSequence uint64
	lastAccess        uint64
	expiresAt         time.Time
}

var claudeDiagnosticsState = struct {
	sync.Mutex
	entries      map[string]claudeDiagnosticsEntry
	lastCleanup  time.Time
	nextSequence uint64
	nextAccess   uint64
}{entries: make(map[string]claudeDiagnosticsEntry)}

// IsValidClaudePromptID verifies whether an explicit prompt ID adheres to strict RFC 4122 UUIDv4 semantics.
func IsValidClaudePromptID(id string) bool {
	id = strings.TrimSpace(id)
	if !claudePromptIDPattern.MatchString(id) {
		return false
	}
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.Version() == 4 && parsed.Variant() == uuid.RFC4122
}

// BeginClaudeContinuity starts one request generation for a stable credential
// identity and Claude conversation. It tracks the previous upstream message ID,
// previous upstream request ID (cc_prev_req), and active prompt ID (cc_prompt_id).
// If explicitPromptID is provided and valid, it is adopted. Otherwise if isNewPromptTurn
// is true (or if no prompt ID exists), a fresh UUIDv4 is generated.
func BeginClaudeContinuity(credentialIdentity, sessionID string, isNewPromptTurn bool, explicitPromptID string) (key string, sequence uint64, previousMessageID, previousRequestID, promptID string) {
	credentialIdentity = strings.TrimSpace(credentialIdentity)
	sessionID = strings.TrimSpace(sessionID)
	if credentialIdentity == "" || sessionID == "" {
		return "", 0, "", "", ""
	}
	digest := sha256.Sum256([]byte(credentialIdentity + "\x00" + sessionID))
	key = hex.EncodeToString(digest[:])
	now := time.Now()

	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	cleanupClaudeDiagnosticsLocked(now)

	entry, found := claudeDiagnosticsState.entries[key]
	newGeneration := !found || (!entry.expiresAt.IsZero() && now.After(entry.expiresAt))
	if newGeneration && !found {
		evictClaudeDiagnosticsLocked()
	}

	claudeDiagnosticsState.nextSequence++
	sequence = claudeDiagnosticsState.nextSequence
	if newGeneration {
		entry = claudeDiagnosticsEntry{minimumSequence: sequence}
	}

	activePromptID := entry.promptID
	explicitPromptID = strings.TrimSpace(explicitPromptID)
	if explicitPromptID != "" && IsValidClaudePromptID(explicitPromptID) {
		activePromptID = strings.ToLower(explicitPromptID)
	} else if isNewPromptTurn || activePromptID == "" {
		activePromptID = uuid.NewString()
	}

	claudeDiagnosticsState.nextAccess++
	entry.lastAccess = claudeDiagnosticsState.nextAccess
	entry.expiresAt = now.Add(claudeDiagnosticsTTL)
	claudeDiagnosticsState.entries[key] = entry
	return key, sequence, entry.previousMessageID, entry.previousRequestID, activePromptID
}

// BeginClaudeDiagnostics starts one request generation for a stable credential
// identity and Claude conversation. It returns the last successfully completed
// upstream message ID, if any. Only a SHA-256 digest of the credential identity
// and session is retained as the cache key, so access-token rotation does not
// interrupt continuity.
func BeginClaudeDiagnostics(credentialIdentity, sessionID string) (key string, sequence uint64, previousMessageID string) {
	key, sequence, prevMsg, _, _ := BeginClaudeContinuity(credentialIdentity, sessionID, false, "")
	return key, sequence, prevMsg
}

// CommitClaudeContinuity advances continuity only after a response completes.
// It commits the upstream message ID (msg_01...), request ID (req_01...), and prompt ID (cc_prompt_id).
// A non-empty messageID is required so incomplete/truncated streams do not advance sequence.
func CommitClaudeContinuity(key string, sequence uint64, messageID, requestID string, promptIDs ...string) {
	key = strings.TrimSpace(key)
	messageID = strings.TrimSpace(messageID)
	requestID = strings.TrimSpace(requestID)
	if key == "" || sequence == 0 || messageID == "" {
		return
	}
	now := time.Now()

	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	entry, ok := claudeDiagnosticsState.entries[key]
	if !ok || (!entry.expiresAt.IsZero() && now.After(entry.expiresAt)) || sequence < entry.minimumSequence || sequence < entry.committedSequence {
		return
	}
	claudeDiagnosticsState.nextAccess++
	entry.previousMessageID = messageID
	if requestID != "" && claudeRequestIDPattern.MatchString(requestID) {
		entry.previousRequestID = requestID
	} else {
		entry.previousRequestID = ""
	}
	if len(promptIDs) > 0 {
		if pID := strings.TrimSpace(promptIDs[0]); pID != "" && IsValidClaudePromptID(pID) {
			entry.promptID = strings.ToLower(pID)
		}
	}
	entry.committedSequence = sequence
	entry.lastAccess = claudeDiagnosticsState.nextAccess
	entry.expiresAt = now.Add(claudeDiagnosticsTTL)
	claudeDiagnosticsState.entries[key] = entry
}

// CommitClaudeDiagnostics advances continuity only after a response completes.
// A response from an older concurrently-started request cannot overwrite a
// newer committed generation, including after TTL expiry or capacity eviction.
func CommitClaudeDiagnostics(key string, sequence uint64, messageID string) {
	CommitClaudeContinuity(key, sequence, messageID, "")
}

func cleanupClaudeDiagnosticsLocked(now time.Time) {
	if !claudeDiagnosticsState.lastCleanup.IsZero() && now.Sub(claudeDiagnosticsState.lastCleanup) < claudeDiagnosticsCleanupPeriod {
		return
	}
	for key, entry := range claudeDiagnosticsState.entries {
		if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			delete(claudeDiagnosticsState.entries, key)
		}
	}
	claudeDiagnosticsState.lastCleanup = now
}

func evictClaudeDiagnosticsLocked() {
	if len(claudeDiagnosticsState.entries) < claudeDiagnosticsMaxEntries {
		return
	}
	type candidate struct {
		key        string
		lastAccess uint64
	}
	candidates := make([]candidate, 0, len(claudeDiagnosticsState.entries))
	for key, entry := range claudeDiagnosticsState.entries {
		candidates = append(candidates, candidate{key: key, lastAccess: entry.lastAccess})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].lastAccess < candidates[j].lastAccess
	})
	count := min(claudeDiagnosticsEvictBatchSize, len(candidates))
	for _, candidate := range candidates[:count] {
		delete(claudeDiagnosticsState.entries, candidate.key)
	}
}

// ResetClaudeDiagnosticsForTest resets in-memory continuity state for test isolation.
func ResetClaudeDiagnosticsForTest() {
	resetClaudeDiagnosticsForTest()
}

func resetClaudeDiagnosticsForTest() {
	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	claudeDiagnosticsState.entries = make(map[string]claudeDiagnosticsEntry)
	claudeDiagnosticsState.lastCleanup = time.Time{}
	claudeDiagnosticsState.nextSequence = 0
	claudeDiagnosticsState.nextAccess = 0
}

// IsClaudeNewPromptTurn inspects the request messages to determine whether
// this request starts a new user prompt turn (requiring a new cc_prompt_id)
// versus a tool continuation step (which reuses the current cc_prompt_id).
func IsClaudeNewPromptTurn(body []byte) bool {
	if IsClaudeProbeOrHelperRequest(body) {
		return false
	}
	messages := gjson.GetBytes(body, "messages")
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
		// If any element is a tool_result, this is a tool continuation turn.
		for _, part := range content.Array() {
			if part.Get("type").String() == "tool_result" {
				return false
			}
		}
	}
	return true
}

var (
	claudePrevReqBillingPattern  = regexp.MustCompile(`\s*cc_prev_req=[^;]+;`)
	claudePromptIDBillingPattern = regexp.MustCompile(`\s*cc_prompt_id=[^;]+;`)
)

func isClaudeProbeRequest(body []byte) bool {
	maxTokens := gjson.GetBytes(body, "max_tokens")
	if !maxTokens.Exists() || maxTokens.Int() != 1 {
		return false
	}
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && len(tools.Array()) > 0 {
		return false
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() || len(messages.Array()) == 0 {
		return true // headless preflight probe without messages
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
	if content.Type == gjson.String {
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

func isClaudeTitleHelperInstruction(body []byte) bool {
	matchesTitlePrompt := func(t string) bool {
		return strings.Contains(t, "Return a short title") ||
			strings.Contains(t, "naming a coding session") ||
			strings.Contains(t, "Write the title in the predominant language") ||
			strings.Contains(t, "<session>")
	}
	system := gjson.GetBytes(body, "system")
	if system.IsArray() {
		for _, part := range system.Array() {
			if matchesTitlePrompt(part.Get("text").String()) {
				return true
			}
		}
	} else if matchesTitlePrompt(system.String()) {
		return true
	}
	messages := gjson.GetBytes(body, "messages")
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

func isClaudeTitleHelperRequest(body []byte) bool {
	props := gjson.GetBytes(body, "output_config.format.schema.properties")
	if props.Exists() {
		if props.Get("title").Exists() && len(props.Map()) == 1 {
			return isClaudeTitleHelperInstruction(body)
		}
		return false
	}

	// When structured output schema is absent, it can only be an internal helper
	// if a system-role block (either in system or in messages) contains the title instruction.
	// Ordinary user messages ("role": "user") without a title schema are never internal helpers.
	matchesSystemTitleInstruction := func(t string) bool {
		return strings.Contains(t, "naming a coding session") ||
			strings.Contains(t, "Return a short title") ||
			strings.Contains(t, "Write the title in the predominant language")
	}
	system := gjson.GetBytes(body, "system")
	if system.IsArray() {
		for _, part := range system.Array() {
			if matchesSystemTitleInstruction(part.Get("text").String()) {
				return true
			}
		}
	} else if matchesSystemTitleInstruction(system.String()) {
		return true
	}

	messages := gjson.GetBytes(body, "messages")
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

// IsClaudeProbeOrHelperRequest reports whether the request is a minimal
// probe/preflight (max_tokens: 1) or an automated title generation helper,
// which in native Claude Code do not emit cc_prompt_id or cc_prev_req.
func IsClaudeProbeOrHelperRequest(body []byte) bool {
	return isClaudeProbeRequest(body) || isClaudeTitleHelperRequest(body)
}

// IsClaudeSubagentRequest reports whether the incoming request originates from
// or represents a Claude Code subagent.
func IsClaudeSubagentRequest(headers http.Header, body []byte) bool {
	if val := HeaderValueCaseInsensitive(headers, "X-Claude-Code-Agent-Id"); val != "" {
		return true
	}
	if val := HeaderValueCaseInsensitive(headers, "X-Claude-Code-Parent-Agent-Id"); val != "" {
		return true
	}
	if gjson.GetBytes(body, "metadata.user_id.parent_session_id").Exists() {
		return true
	}
	if userID := gjson.GetBytes(body, "metadata.user_id").String(); userID != "" {
		if strings.Contains(userID, `"parent_session_id"`) {
			return true
		}
	}
	// Only inspect system[0].text or billing header for cc_is_subagent, never raw user message content
	system := gjson.GetBytes(body, "system")
	if system.IsArray() && len(system.Array()) > 0 {
		if strings.Contains(system.Array()[0].Get("text").String(), "cc_is_subagent=true") {
			return true
		}
	} else if system.Type == gjson.String && strings.Contains(system.String(), "cc_is_subagent=true") {
		return true
	}
	return false
}

// StripClaudeBillingTags removes cc_prev_req and cc_prompt_id from the billing header in body.
func StripClaudeBillingTags(body []byte) []byte {
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() || len(system.Array()) == 0 {
		return body
	}
	billingText := system.Array()[0].Get("text").String()
	if !strings.HasPrefix(billingText, "x-anthropic-billing-header:") {
		return body
	}
	cleaned := claudePrevReqBillingPattern.ReplaceAllString(billingText, "")
	cleaned = claudePromptIDBillingPattern.ReplaceAllString(cleaned, "")
	if cleaned != billingText {
		updated, err := sjson.SetBytes(body, "system.0.text", cleaned)
		if err == nil {
			return updated
		}
	}
	return body
}

// InjectClaudeBillingTags appends cc_prev_req and cc_prompt_id to the billing header in body if present.
func InjectClaudeBillingTags(body []byte, prevReq, promptID string) []byte {
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() || len(system.Array()) == 0 {
		return body
	}
	billingText := system.Array()[0].Get("text").String()
	if !strings.HasPrefix(billingText, "x-anthropic-billing-header:") {
		return body
	}
	cleaned := claudePrevReqBillingPattern.ReplaceAllString(billingText, "")
	cleaned = claudePromptIDBillingPattern.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSpace(cleaned)
	if !strings.HasSuffix(cleaned, ";") {
		cleaned += ";"
	}
	if prevReq != "" {
		cleaned += " cc_prev_req=" + prevReq + ";"
	}
	if promptID != "" {
		cleaned += " cc_prompt_id=" + promptID + ";"
	}
	updated, err := sjson.SetBytes(body, "system.0.text", cleaned)
	if err == nil {
		return updated
	}
	return body
}

// ExtractClaudeBillingTags extracts existing cc_prev_req and cc_prompt_id values
// from a billing header in system text, if present.
func ExtractClaudeBillingTags(body []byte) (prevReq, promptID string) {
	system := gjson.GetBytes(body, "system")
	var billingText string
	if system.IsArray() && len(system.Array()) > 0 {
		billingText = system.Array()[0].Get("text").String()
	} else if system.Type == gjson.String {
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
		if claudeRequestIDPattern.MatchString(val) {
			prevReq = val
		}
	}
	if idx := strings.Index(billingText, "cc_prompt_id="); idx >= 0 {
		val := billingText[idx+len("cc_prompt_id="):]
		if end := strings.IndexByte(val, ';'); end >= 0 {
			val = val[:end]
		}
		if IsValidClaudePromptID(val) {
			promptID = strings.ToLower(val)
		}
	}
	return prevReq, promptID
}
