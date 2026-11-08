package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

const (
	claudeDiagnosticsTTL            = time.Hour
	claudeDiagnosticsCleanupPeriod  = 15 * time.Minute
	claudeDiagnosticsMaxEntries     = 4096
	claudeDiagnosticsEvictBatchSize = 256
	claudeSessionDatePinKVPrefix    = "cpa:claude:session-date-pin:"
)

type claudeSessionDatePinKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error)
}

var currentClaudeSessionDatePinKVClient = func() (claudeSessionDatePinKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

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
	// PinnedDate is the calendar date this session was first seen on. The
	// cloaked currentDate reminder reuses it so the reminder text stays
	// byte-stable within a session even when the local date flips mid-session.
	PinnedDate  string
	Initialized bool
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
	pinnedDate        string
	minimumSequence   uint64
	committedSequence uint64
	lastAccess        uint64
	expiresAt         time.Time
}

// claudeSessionDatePin retains a session date independently of continuity
// state when the operator configures a retention window.
type claudeSessionDatePin struct {
	date      string
	expiresAt time.Time
}

var claudeDiagnosticsState = struct {
	sync.Mutex
	entries      map[string]claudeDiagnosticsEntry
	datePins     map[string]claudeSessionDatePin
	lastCleanup  time.Time
	nextSequence uint64
	nextAccess   uint64
}{entries: make(map[string]claudeDiagnosticsEntry), datePins: make(map[string]claudeSessionDatePin)}

// ClaudeSessionDatePinKey returns the credential-independent store key
// for a session date pin, or "" for an empty session ID.
func ClaudeSessionDatePinKey(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(digest[:])
}

func claudeSessionDatePinKVKey(pinKey string) string {
	if pinKey == "" {
		return ""
	}
	return claudeSessionDatePinKVPrefix + pinKey
}

func normalizeClaudePinnedDate(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) != len("2006-01-02") {
		return ""
	}
	if _, errParse := time.Parse("2006-01-02", trimmed); errParse != nil {
		return ""
	}
	return trimmed
}

func lookupClaudeSessionDatePinLocked(pinKey string, now time.Time) string {
	if pinKey == "" {
		return ""
	}
	pin, okPin := claudeDiagnosticsState.datePins[pinKey]
	if !okPin {
		return ""
	}
	if !pin.expiresAt.IsZero() && now.After(pin.expiresAt) {
		return ""
	}
	return normalizeClaudePinnedDate(pin.date)
}

func storePermanentClaudeSessionDatePinLocked(pinKey, date string, now time.Time) {
	if pinKey == "" || date == "" {
		return
	}
	cleanupClaudeDiagnosticsLocked(now)
	if _, okPin := claudeDiagnosticsState.datePins[pinKey]; !okPin {
		evictClaudeSessionDatePinsLocked()
	}
	claudeDiagnosticsState.datePins[pinKey] = claudeSessionDatePin{
		date:      date,
		expiresAt: time.Time{},
	}
}

func storePermanentClaudeSessionDatePin(pinKey, date string) {
	if pinKey == "" || date == "" {
		return
	}
	now := time.Now()
	claudeDiagnosticsState.Lock()
	storePermanentClaudeSessionDatePinLocked(pinKey, date, now)
	claudeDiagnosticsState.Unlock()
}

// IsClaudeSessionDatePinKVActive reports whether a persistent KV store (local
// bbolt or Home KV) is configured as the backing store for Claude session date pins.
func IsClaudeSessionDatePinKVActive() bool {
	_, kvMode, _ := currentClaudeSessionDatePinKVClient()
	return kvMode
}

// LookupClaudeSessionDatePin returns the retained pinned date for a session
// without creating or refreshing it. When a KV store is active, pins are stored
// permanently and retention is ignored; otherwise non-positive retention returns "".
func LookupClaudeSessionDatePin(sessionID string, retention time.Duration) string {
	pinKey := ClaudeSessionDatePinKey(sessionID)
	if pinKey == "" {
		return ""
	}
	client, kvMode, errClient := currentClaudeSessionDatePinKVClient()
	if kvMode {
		if errClient == nil && client != nil {
			raw, found, errGet := client.KVGet(context.Background(), claudeSessionDatePinKVKey(pinKey))
			if errGet == nil {
				if !found {
					return ""
				}
				pinned := normalizeClaudePinnedDate(string(raw))
				if pinned != "" {
					storePermanentClaudeSessionDatePin(pinKey, pinned)
				}
				return pinned
			}
		}
		now := time.Now()
		claudeDiagnosticsState.Lock()
		defer claudeDiagnosticsState.Unlock()
		return lookupClaudeSessionDatePinLocked(pinKey, now)
	}
	if retention <= 0 {
		return ""
	}
	now := time.Now()
	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	return lookupClaudeSessionDatePinLocked(pinKey, now)
}

// IsValidClaudePromptID verifies whether an explicit prompt ID adheres to strict RFC 4122 UUIDv4 semantics.
func IsValidClaudePromptID(id string) bool {
	id = strings.TrimSpace(id)
	if !claudePromptIDPattern.MatchString(id) {
		return false
	}
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.Version() == 4 && parsed.Variant() == uuid.RFC4122
}

// ClaudeDeterministicPromptID generates a deterministic RFC 4122 UUIDv4 from a seed string.
func ClaudeDeterministicPromptID(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	digest[6] = (digest[6] & 0x0f) | 0x40 // Version 4
	digest[8] = (digest[8] & 0x3f) | 0x80 // Variant RFC 4122
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		digest[0:4],
		digest[4:6],
		digest[6:8],
		digest[8:10],
		digest[10:16],
	)
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

// PinClaudeSessionDate returns the calendar date pinned for a session,
// recording date on the first call and returning the pinned value on later
// calls. When a KV store is active, the pin is persisted permanently under a
// session-only KV key and retention is ignored. Otherwise, a non-positive
// retention keeps the legacy continuity-scoped pin (whose lifetime follows
// claudeDiagnosticsTTL) and a positive retention stores the pin in memory
// under a session-only key for retention. An unknown continuity key returns
// date unchanged, matching per-request dates.
func PinClaudeSessionDate(key, sessionID, date string, retention time.Duration) string {
	key = strings.TrimSpace(key)
	sessionID = strings.TrimSpace(sessionID)
	date = strings.TrimSpace(date)
	if key == "" || date == "" {
		return date
	}
	client, kvMode, errClient := currentClaudeSessionDatePinKVClient()
	claudeDiagnosticsState.Lock()
	entry, okEntry := claudeDiagnosticsState.entries[key]
	if !okEntry {
		claudeDiagnosticsState.Unlock()
		return date
	}
	if sessionID != "" && kvMode {
		pinKey := ClaudeSessionDatePinKey(sessionID)
		now := time.Now()
		targetDate := normalizeClaudePinnedDate(date)
		if l1Date := lookupClaudeSessionDatePinLocked(pinKey, now); l1Date != "" {
			targetDate = l1Date
		}
		if errClient == nil && client != nil {
			claudeDiagnosticsState.Unlock()
			ctx := context.Background()
			kvKey := claudeSessionDatePinKVKey(pinKey)
			raw, found, errGet := client.KVGet(ctx, kvKey)
			if errGet == nil && found {
				if pinned := normalizeClaudePinnedDate(string(raw)); pinned != "" {
					storePermanentClaudeSessionDatePin(pinKey, pinned)
					return pinned
				}
				if targetDate == "" {
					return date
				}
				swapped, errCAS := client.KVCompareAndSwap(ctx, kvKey, raw, true, []byte(targetDate), 0)
				if errCAS == nil && swapped {
					storePermanentClaudeSessionDatePin(pinKey, targetDate)
					return targetDate
				}
				if errCAS == nil && !swapped {
					if rawRace, foundRace, errRace := client.KVGet(ctx, kvKey); errRace == nil && foundRace {
						if pinned := normalizeClaudePinnedDate(string(rawRace)); pinned != "" {
							storePermanentClaudeSessionDatePin(pinKey, pinned)
							return pinned
						}
					}
				}
			} else if errGet == nil && !found {
				if targetDate == "" {
					return date
				}
				written, errSet := client.KVSetNX(ctx, kvKey, []byte(targetDate), 0)
				if errSet == nil && written {
					storePermanentClaudeSessionDatePin(pinKey, targetDate)
					return targetDate
				}
				if errSet == nil && !written {
					if rawRace, foundRace, errRace := client.KVGet(ctx, kvKey); errRace == nil && foundRace {
						if pinned := normalizeClaudePinnedDate(string(rawRace)); pinned != "" {
							storePermanentClaudeSessionDatePin(pinKey, pinned)
							return pinned
						}
					}
				}
			}
			claudeDiagnosticsState.Lock()
			entry, okEntry = claudeDiagnosticsState.entries[key]
			if !okEntry {
				claudeDiagnosticsState.Unlock()
				return date
			}
			now = time.Now()
			if l1Date := lookupClaudeSessionDatePinLocked(pinKey, now); l1Date != "" {
				targetDate = l1Date
			}
		}
		if targetDate != "" {
			storePermanentClaudeSessionDatePinLocked(pinKey, targetDate, now)
			if entry.pinnedDate == "" {
				entry.pinnedDate = targetDate
				claudeDiagnosticsState.entries[key] = entry
			}
			claudeDiagnosticsState.Unlock()
			return targetDate
		}
		claudeDiagnosticsState.Unlock()
		return date
	}
	defer claudeDiagnosticsState.Unlock()

	now := time.Now()
	if retention <= 0 || sessionID == "" {
		if entry.pinnedDate == "" {
			entry.pinnedDate = date
			claudeDiagnosticsState.entries[key] = entry
			return date
		}
		return entry.pinnedDate
	}
	cleanupClaudeDiagnosticsLocked(now)
	pinKey := ClaudeSessionDatePinKey(sessionID)
	pin, okPin := claudeDiagnosticsState.datePins[pinKey]
	if !okPin {
		evictClaudeSessionDatePinsLocked()
	}
	if !okPin || (!pin.expiresAt.IsZero() && now.After(pin.expiresAt)) {
		pin.date = date
	}
	pin.expiresAt = now.Add(retention)
	claudeDiagnosticsState.datePins[pinKey] = pin
	return pin.date
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
	for key, pin := range claudeDiagnosticsState.datePins {
		if !pin.expiresAt.IsZero() && now.After(pin.expiresAt) {
			delete(claudeDiagnosticsState.datePins, key)
		}
	}
	claudeDiagnosticsState.lastCleanup = now
}

// evictClaudeSessionDatePinsLocked drops the pins closest to expiry once
// the store is full. Sliding expiry makes those the least recently used.
func evictClaudeSessionDatePinsLocked() {
	if len(claudeDiagnosticsState.datePins) < claudeDiagnosticsMaxEntries {
		return
	}
	type candidate struct {
		key       string
		expiresAt time.Time
	}
	candidates := make([]candidate, 0, len(claudeDiagnosticsState.datePins))
	for key, pin := range claudeDiagnosticsState.datePins {
		candidates = append(candidates, candidate{key: key, expiresAt: pin.expiresAt})
	}
	sort.Slice(candidates, func(i, j int) bool {
		iZero := candidates[i].expiresAt.IsZero()
		jZero := candidates[j].expiresAt.IsZero()
		if iZero != jZero {
			return !iZero
		}
		return candidates[i].expiresAt.Before(candidates[j].expiresAt)
	})
	count := min(claudeDiagnosticsEvictBatchSize, len(candidates))
	for _, candidate := range candidates[:count] {
		delete(claudeDiagnosticsState.datePins, candidate.key)
	}
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

// ClaudeSessionDatePinExpiryForTest returns the expiry of the retained date
// pin for a session, or the zero time when no pin exists.
func ClaudeSessionDatePinExpiryForTest(sessionID string) time.Time {
	pinKey := ClaudeSessionDatePinKey(sessionID)
	if pinKey == "" {
		return time.Time{}
	}
	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	pin, okPin := claudeDiagnosticsState.datePins[pinKey]
	if !okPin {
		return time.Time{}
	}
	return pin.expiresAt
}

// ExpireClaudeDiagnosticsForTest expires continuity entries and retained date
// pins without waiting for wall-clock TTLs. Pin expiry is relative to now.
func ExpireClaudeDiagnosticsForTest(expireContinuity bool, pinExpiryOffset *time.Duration) {
	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	if expireContinuity {
		past := time.Now().Add(-time.Second)
		for key, entry := range claudeDiagnosticsState.entries {
			entry.expiresAt = past
			claudeDiagnosticsState.entries[key] = entry
		}
	}
	if pinExpiryOffset != nil {
		expiresAt := time.Now().Add(*pinExpiryOffset)
		for key, pin := range claudeDiagnosticsState.datePins {
			pin.expiresAt = expiresAt
			claudeDiagnosticsState.datePins[key] = pin
		}
	}
	claudeDiagnosticsState.lastCleanup = time.Time{}
}

func resetClaudeDiagnosticsForTest() {
	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	claudeDiagnosticsState.entries = make(map[string]claudeDiagnosticsEntry)
	claudeDiagnosticsState.datePins = make(map[string]claudeSessionDatePin)
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
	messages := ojson.GetBytesNoCopy(body, "messages")
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
	maxTokens := ojson.GetBytesNoCopy(body, "max_tokens")
	if !maxTokens.Exists() || maxTokens.Int() != 1 {
		return false
	}
	tools := ojson.GetBytesNoCopy(body, "tools")
	if tools.Exists() && len(tools.Array()) > 0 {
		return false
	}
	messages := ojson.GetBytesNoCopy(body, "messages")
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

func isClaudeTitleHelperInstruction(body []byte) bool {
	matchesTitlePrompt := func(t string) bool {
		return strings.Contains(t, "Return a short title") ||
			strings.Contains(t, "naming a coding session") ||
			strings.Contains(t, "Write the title in the predominant language") ||
			strings.Contains(t, "<session>")
	}
	system := ojson.GetBytesNoCopy(body, "system")
	if system.IsArray() {
		for _, part := range system.Array() {
			if matchesTitlePrompt(part.Get("text").String()) {
				return true
			}
		}
	} else if matchesTitlePrompt(system.String()) {
		return true
	}
	messages := ojson.GetBytesNoCopy(body, "messages")
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
	// Without an output_config key the request is a helper only if one of the
	// system title instructions below appears, so skip the walks when none can.
	if !jsonMayContainASCII(body, "output_config", "naming a coding session", "Return a short title",
		"Write the title in the predominant language") {
		return false
	}
	props := ojson.GetBytesNoCopy(body, "output_config.format.schema.properties")
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
	system := ojson.GetBytesNoCopy(body, "system")
	if system.IsArray() {
		for _, part := range system.Array() {
			if matchesSystemTitleInstruction(part.Get("text").String()) {
				return true
			}
		}
	} else if matchesSystemTitleInstruction(system.String()) {
		return true
	}

	messages := ojson.GetBytesNoCopy(body, "messages")
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
	if ojson.GetBytesNoCopy(body, "metadata.user_id.parent_session_id").Exists() {
		return true
	}
	if userID := ojson.GetBytesNoCopy(body, "metadata.user_id").String(); userID != "" {
		if strings.Contains(userID, `"parent_session_id"`) {
			return true
		}
	}
	// Only inspect system[0].text or billing header for cc_is_subagent, never raw user message content
	system := ojson.GetBytesNoCopy(body, "system")
	if system.IsArray() && len(system.Array()) > 0 {
		if strings.Contains(system.Array()[0].Get("text").String(), "cc_is_subagent=true") {
			return true
		}
	} else if system.Type == ojson.String && strings.Contains(system.String(), "cc_is_subagent=true") {
		return true
	}
	return false
}

// ClaudePayloadHas1hTTL reports whether the request payload contains any cache_control
// block with ttl set to "1h".
func ClaudePayloadHas1hTTL(payload []byte) bool {
	// A ttl of "1h" is the JSON string "1h"; its quotes are structural and never
	// escaped, so a payload without that token cannot match.
	if len(payload) == 0 || !jsonMayContainASCII(payload, `"1h"`) || !ojson.ValidBytes(payload) {
		return false
	}
	has1h := false
	checkBlock := func(item ojson.Result) bool {
		cc := item.Get("cache_control")
		if cc.IsObject() && cc.Get("ttl").String() == "1h" {
			has1h = true
			return false
		}
		return true
	}
	if tools := ojson.GetBytesNoCopy(payload, "tools"); tools.IsArray() {
		tools.ForEach(func(_, item ojson.Result) bool {
			return checkBlock(item)
		})
		if has1h {
			return true
		}
	}
	if system := ojson.GetBytesNoCopy(payload, "system"); system.IsArray() {
		system.ForEach(func(_, item ojson.Result) bool {
			return checkBlock(item)
		})
		if has1h {
			return true
		}
	}
	if messages := ojson.GetBytesNoCopy(payload, "messages"); messages.IsArray() {
		messages.ForEach(func(_, msg ojson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, item ojson.Result) bool {
					return checkBlock(item)
				})
			}
			return !has1h
		})
	}
	return has1h
}

// ClaudeSubagentRequests1h reports whether a subagent request explicitly requests
// 1h cache TTL either via a cache_control block with ttl="1h" in the payload or via
// extended-cache-ttl-2025-04-11 in incoming Anthropic-Beta headers.
func ClaudeSubagentRequests1h(headers http.Header, body []byte) bool {
	if ClaudePayloadHas1hTTL(body) {
		return true
	}
	betas := strings.Join(HeaderValuesCaseInsensitive(headers, "Anthropic-Beta"), ",")
	return strings.Contains(betas, "extended-cache-ttl-2025-04-11")
}

// StripClaudeBillingTags removes cc_prev_req and cc_prompt_id from the billing header in body.
func StripClaudeBillingTags(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	doc := ojson.NewDoc(body)
	if !StripClaudeBillingTagsDoc(doc) {
		return body
	}
	return doc.Bytes()
}

// StripClaudeBillingTagsDoc removes cc_prev_req and cc_prompt_id from the billing header in doc in place.
func StripClaudeBillingTagsDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	system := doc.Get("system")
	if !system.IsArray() || len(system.Array()) == 0 {
		return false
	}
	billingText := system.Array()[0].Get("text").String()
	if !strings.HasPrefix(billingText, "x-anthropic-billing-header:") {
		return false
	}
	cleaned := claudePrevReqBillingPattern.ReplaceAllString(billingText, "")
	cleaned = claudePromptIDBillingPattern.ReplaceAllString(cleaned, "")
	if cleaned != billingText {
		return doc.Set("system.0.text", cleaned) == nil
	}
	return false
}

// InjectClaudeBillingTags appends cc_prev_req and cc_prompt_id to the billing header in body if present.
func InjectClaudeBillingTags(body []byte, prevReq, promptID string) []byte {
	if len(body) == 0 {
		return body
	}
	doc := ojson.NewDoc(body)
	if !InjectClaudeBillingTagsDoc(doc, prevReq, promptID) {
		return body
	}
	return doc.Bytes()
}

// InjectClaudeBillingTagsDoc appends cc_prev_req and cc_prompt_id to the billing header in doc in place.
func InjectClaudeBillingTagsDoc(doc *ojson.Doc, prevReq, promptID string) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	system := doc.Get("system")
	if !system.IsArray() || len(system.Array()) == 0 {
		return false
	}
	billingText := system.Array()[0].Get("text").String()
	if !strings.HasPrefix(billingText, "x-anthropic-billing-header:") {
		return false
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
	return doc.Set("system.0.text", cleaned) == nil
}

// ExtractClaudeBillingTags extracts existing cc_prev_req and cc_prompt_id values
// from a billing header in system text, if present.
func ExtractClaudeBillingTags(body []byte) (prevReq, promptID string) {
	system := ojson.GetBytesNoCopy(body, "system")
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
		if claudeRequestIDPattern.MatchString(val) {
			prevReq = strings.Clone(val)
		}
	}
	if idx := strings.Index(billingText, "cc_prompt_id="); idx >= 0 {
		val := billingText[idx+len("cc_prompt_id="):]
		if end := strings.IndexByte(val, ';'); end >= 0 {
			val = val[:end]
		}
		if IsValidClaudePromptID(val) {
			promptID = strings.Clone(strings.ToLower(val))
		}
	}
	return prevReq, promptID
}
