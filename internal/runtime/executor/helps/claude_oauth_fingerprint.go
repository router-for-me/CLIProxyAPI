package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	claudeOAuthViolationNone              = ""
	claudeOAuthViolationIdentityMismatch  = "identity_mismatch"
	claudeOAuthViolationSessionLimit      = "session_limit"
	claudeOAuthFingerprintCleanupInterval = 15 * time.Minute
)

var (
	claudeOAuthLegacyUserIDPattern = regexp.MustCompile(`^user_([a-fA-F0-9]{64})_account_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})_session_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

	claudeOAuthRegistryMu          sync.Mutex
	claudeOAuthRegistries          = make(map[string]*claudeOAuthAccountRegistry)
	claudeOAuthRegistryCleanupOnce sync.Once
)

type claudeOAuthContextKey struct{}

type claudeOAuthAccountIdentity struct {
	Format      string // "legacy" | "json"
	UserHash    string
	AccountUUID string
	DeviceID    string
}

type claudeOAuthSessionEntry struct {
	sessionID string
	slot      int
	expiresAt time.Time
}

type claudeOAuthAccountRegistry struct {
	account  claudeOAuthAccountIdentity
	sessions map[string]*claudeOAuthSessionEntry
}

// ClaudeOAuthFingerprintGateResult captures gate output for logging and header override.
type ClaudeOAuthFingerprintGateResult struct {
	SessionID       string
	Slot            int
	Violation       string
	SessionMismatch bool
	HeaderSessionID string
	BodySessionID   string
	DeviceID        string
	AccountID       string
	UserHash        string
	Format          string
}

// ClaudeOAuthFingerprintError is returned when enforce mode blocks a request.
type ClaudeOAuthFingerprintError struct {
	Code    string
	Message string
}

func (e *ClaudeOAuthFingerprintError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// ClaudeOAuthFingerprintEnabled reports whether OAuth fingerprint handling is active.
func ClaudeOAuthFingerprintEnabled(cfg *config.Config, apiKey string) bool {
	if cfg == nil || !cfg.ClaudeOAuthFingerprint.Enabled {
		return false
	}
	return strings.Contains(apiKey, "sk-ant-oat")
}

func claudeOAuthFingerprintSessionTTL(cfg *config.Config) time.Duration {
	if cfg == nil {
		return time.Hour
	}
	ttlStr := strings.TrimSpace(cfg.ClaudeOAuthFingerprint.SessionTTL)
	if ttlStr == "" {
		return time.Hour
	}
	parsed, err := time.ParseDuration(ttlStr)
	if err != nil || parsed <= 0 {
		return time.Hour
	}
	return parsed
}

func claudeOAuthFingerprintEnforce(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cfg.ClaudeOAuthFingerprint.Mode), "enforce")
}

func claudeOAuthFingerprintMaxSessions(cfg *config.Config) int {
	if cfg == nil || cfg.ClaudeOAuthFingerprint.MaxSessions <= 0 {
		return 4
	}
	return cfg.ClaudeOAuthFingerprint.MaxSessions
}

func startClaudeOAuthRegistryCleanup() {
	go func() {
		ticker := time.NewTicker(claudeOAuthFingerprintCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredClaudeOAuthSessions(time.Now())
		}
	}()
}

func purgeExpiredClaudeOAuthSessions(now time.Time) {
	claudeOAuthRegistryMu.Lock()
	defer claudeOAuthRegistryMu.Unlock()
	for authID, registry := range claudeOAuthRegistries {
		purgeExpiredSessionsLocked(registry, now)
		if len(registry.sessions) == 0 && registry.account.Format == "" {
			delete(claudeOAuthRegistries, authID)
		}
	}
}

func purgeExpiredSessionsLocked(registry *claudeOAuthAccountRegistry, now time.Time) {
	if registry == nil {
		return
	}
	for sessionID, entry := range registry.sessions {
		if entry == nil || !entry.expiresAt.After(now) {
			delete(registry.sessions, sessionID)
		}
	}
}

func registryForAuth(authID string) *claudeOAuthAccountRegistry {
	claudeOAuthRegistryCleanupOnce.Do(startClaudeOAuthRegistryCleanup)
	claudeOAuthRegistryMu.Lock()
	defer claudeOAuthRegistryMu.Unlock()
	registry := claudeOAuthRegistries[authID]
	if registry == nil {
		registry = &claudeOAuthAccountRegistry{sessions: make(map[string]*claudeOAuthSessionEntry)}
		claudeOAuthRegistries[authID] = registry
	}
	return registry
}

// ContextWithClaudeOAuthFingerprint stores gate output on the request context.
func ContextWithClaudeOAuthFingerprint(ctx context.Context, result *ClaudeOAuthFingerprintGateResult) context.Context {
	if ctx == nil || result == nil {
		return ctx
	}
	return context.WithValue(ctx, claudeOAuthContextKey{}, result)
}

// ClaudeOAuthFingerprintGateResultFromContext reads gate output from context.
func ClaudeOAuthFingerprintGateResultFromContext(ctx context.Context) *ClaudeOAuthFingerprintGateResult {
	if ctx == nil {
		return nil
	}
	result, _ := ctx.Value(claudeOAuthContextKey{}).(*ClaudeOAuthFingerprintGateResult)
	return result
}

// ClaudeOAuthFingerprintApplySessionHeader sets the outbound Claude Code session header.
func ClaudeOAuthFingerprintApplySessionHeader(r *http.Request, sessionID string) {
	if r == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	r.Header.Set(ClaudeCodeSessionHeader, strings.TrimSpace(sessionID))
}

// ClaudeOAuthFingerprintGate resolves, registers, and normalizes OAuth outbound identity.
func ClaudeOAuthFingerprintGate(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, inboundHeaders http.Header, body []byte, model string) ([]byte, *ClaudeOAuthFingerprintGateResult, error) {
	_ = ctx
	_ = model
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return body, nil, nil
	}

	headerSession := extractClaudeOAuthSessionFromHeader(inboundHeaders)
	bodySession := extractClaudeOAuthSessionFromBody(body)
	inboundIdentity := extractClaudeOAuthIdentityFromBody(body)

	result := &ClaudeOAuthFingerprintGateResult{
		HeaderSessionID: headerSession,
		BodySessionID:   bodySession,
	}
	if headerSession != "" && bodySession != "" && headerSession != bodySession {
		result.SessionMismatch = true
	}

	sessionID := resolveClaudeOAuthCanonicalSessionID(headerSession, bodySession)
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	result.SessionID = sessionID

	authID := strings.TrimSpace(auth.ID)
	registry := registryForAuth(authID)
	ttl := claudeOAuthFingerprintSessionTTL(cfg)
	now := time.Now()

	claudeOAuthRegistryMu.Lock()
	purgeExpiredSessionsLocked(registry, now)
	identityViolation := pinClaudeOAuthAccountIdentity(registry, authID, inboundIdentity)
	slot, sessionViolation := registerClaudeOAuthSessionLocked(registry, sessionID, now, ttl, claudeOAuthFingerprintMaxSessions(cfg))
	fillClaudeOAuthGateResultIdentity(result, registry.account)
	result.Slot = slot
	result.Violation = firstClaudeOAuthViolation(identityViolation, sessionViolation)
	claudeOAuthRegistryMu.Unlock()

	if result.Violation != "" {
		logClaudeOAuthViolation(authID, result, cfg)
		if claudeOAuthFingerprintEnforce(cfg) {
			return body, result, claudeOAuthFingerprintErrorFor(result.Violation)
		}
	}

	outBody, errSet := setClaudeOAuthOutboundUserID(body, registry.account, sessionID)
	if errSet != nil {
		return body, result, fmt.Errorf("claude oauth fingerprint: set user_id: %w", errSet)
	}
	return outBody, result, nil
}

func firstClaudeOAuthViolation(identityViolation, sessionViolation string) string {
	if identityViolation != "" {
		return identityViolation
	}
	return sessionViolation
}

func claudeOAuthFingerprintErrorFor(violation string) error {
	switch violation {
	case claudeOAuthViolationIdentityMismatch:
		return &ClaudeOAuthFingerprintError{
			Code:    claudeOAuthViolationIdentityMismatch,
			Message: "claude oauth fingerprint: account identity mismatch",
		}
	case claudeOAuthViolationSessionLimit:
		return &ClaudeOAuthFingerprintError{
			Code:    claudeOAuthViolationSessionLimit,
			Message: "claude oauth fingerprint: active session limit exceeded",
		}
	default:
		return &ClaudeOAuthFingerprintError{Code: violation, Message: "claude oauth fingerprint: request blocked"}
	}
}

func logClaudeOAuthViolation(authID string, result *ClaudeOAuthFingerprintGateResult, cfg *config.Config) {
	if result == nil || cfg == nil {
		return
	}
	mode := strings.TrimSpace(cfg.ClaudeOAuthFingerprint.Mode)
	log.WithFields(log.Fields{
		"auth_id":   authID,
		"session":   truncateClaudeOAuthLogToken(result.SessionID),
		"slot":      result.Slot,
		"violation": result.Violation,
		"mode":      mode,
	}).Warn("claude oauth fingerprint: violation")
}

func fillClaudeOAuthGateResultIdentity(result *ClaudeOAuthFingerprintGateResult, account claudeOAuthAccountIdentity) {
	if result == nil {
		return
	}
	result.DeviceID = account.DeviceID
	result.AccountID = account.AccountUUID
	result.UserHash = account.UserHash
	result.Format = account.Format
}

func extractClaudeOAuthSessionFromHeader(headers http.Header) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get(ClaudeCodeSessionHeader))
}

func extractClaudeOAuthSessionFromBody(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	return extractClaudeCodeSessionIDFromPayload(payload)
}

func resolveClaudeOAuthCanonicalSessionID(headerSession, bodySession string) string {
	if bodySession != "" {
		return bodySession
	}
	return headerSession
}

type claudeOAuthInboundIdentity struct {
	Format      string
	UserHash    string
	AccountUUID string
	DeviceID    string
}

func extractClaudeOAuthIdentityFromBody(payload []byte) claudeOAuthInboundIdentity {
	userID := strings.TrimSpace(gjson.GetBytes(payload, "metadata.user_id").String())
	if userID == "" {
		return claudeOAuthInboundIdentity{}
	}
	if matches := claudeOAuthLegacyUserIDPattern.FindStringSubmatch(userID); len(matches) == 4 {
		return claudeOAuthInboundIdentity{
			Format:      "legacy",
			UserHash:    matches[1],
			AccountUUID: matches[2],
		}
	}
	if strings.HasPrefix(userID, "{") {
		return claudeOAuthInboundIdentity{
			Format:      "json",
			DeviceID:    strings.TrimSpace(gjson.Get(userID, "device_id").String()),
			AccountUUID: strings.TrimSpace(gjson.Get(userID, "account_uuid").String()),
		}
	}
	return claudeOAuthInboundIdentity{}
}

func pinClaudeOAuthAccountIdentity(registry *claudeOAuthAccountRegistry, authID string, inbound claudeOAuthInboundIdentity) string {
	if registry == nil {
		return ""
	}
	if registry.account.Format == "" {
		registry.account = firstClaudeOAuthAccountIdentity(authID, inbound)
		return ""
	}
	if inbound.Format == "" {
		return ""
	}
	if registry.account.Format == "legacy" {
		if inbound.UserHash != "" && !strings.EqualFold(inbound.UserHash, registry.account.UserHash) {
			return claudeOAuthViolationIdentityMismatch
		}
		if inbound.AccountUUID != "" && inbound.AccountUUID != registry.account.AccountUUID {
			return claudeOAuthViolationIdentityMismatch
		}
		return ""
	}
	if inbound.DeviceID != "" && inbound.DeviceID != registry.account.DeviceID {
		return claudeOAuthViolationIdentityMismatch
	}
	if inbound.AccountUUID != "" && inbound.AccountUUID != registry.account.AccountUUID {
		return claudeOAuthViolationIdentityMismatch
	}
	return ""
}

func firstClaudeOAuthAccountIdentity(authID string, inbound claudeOAuthInboundIdentity) claudeOAuthAccountIdentity {
	if inbound.Format == "legacy" && inbound.UserHash != "" && inbound.AccountUUID != "" {
		return claudeOAuthAccountIdentity{
			Format:      "legacy",
			UserHash:    inbound.UserHash,
			AccountUUID: inbound.AccountUUID,
		}
	}
	if inbound.Format == "json" && (inbound.DeviceID != "" || inbound.AccountUUID != "") {
		deviceID := inbound.DeviceID
		if deviceID == "" {
			deviceID = claudeOAuthDerivedToken(authID, "device")
		}
		accountUUID := inbound.AccountUUID
		if accountUUID == "" {
			accountUUID = claudeOAuthDerivedUUID(authID, "account")
		}
		return claudeOAuthAccountIdentity{
			Format:      "json",
			DeviceID:    deviceID,
			AccountUUID: accountUUID,
		}
	}
	return claudeOAuthAccountIdentity{
		Format:      "json",
		DeviceID:    claudeOAuthDerivedToken(authID, "device"),
		AccountUUID: claudeOAuthDerivedUUID(authID, "account"),
	}
}

func claudeOAuthDerivedUUID(authID, kind string) string {
	name := strings.Join([]string{"cli-proxy-api", "claude", "oauth-fp", kind, strings.TrimSpace(authID)}, ":")
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

func claudeOAuthDerivedToken(authID, kind string) string {
	sum := sha256.Sum256([]byte("cli-proxy-api:claude:oauth-fp:" + kind + ":" + strings.TrimSpace(authID)))
	return hex.EncodeToString(sum[:16])
}

func registerClaudeOAuthSessionLocked(registry *claudeOAuthAccountRegistry, sessionID string, now time.Time, ttl time.Duration, maxSessions int) (int, string) {
	if registry == nil || sessionID == "" {
		return 0, ""
	}
	if entry, ok := registry.sessions[sessionID]; ok && entry != nil && entry.expiresAt.After(now) {
		entry.expiresAt = now.Add(ttl)
		return entry.slot, ""
	}

	if len(activeClaudeOAuthSessionsLocked(registry, now)) >= maxSessions {
		return 0, claudeOAuthViolationSessionLimit
	}

	slot := nextClaudeOAuthSessionSlotLocked(registry, now, maxSessions)
	registry.sessions[sessionID] = &claudeOAuthSessionEntry{
		sessionID: sessionID,
		slot:      slot,
		expiresAt: now.Add(ttl),
	}
	return slot, ""
}

func activeClaudeOAuthSessionsLocked(registry *claudeOAuthAccountRegistry, now time.Time) []*claudeOAuthSessionEntry {
	var active []*claudeOAuthSessionEntry
	for _, entry := range registry.sessions {
		if entry != nil && entry.expiresAt.After(now) {
			active = append(active, entry)
		}
	}
	return active
}

func nextClaudeOAuthSessionSlotLocked(registry *claudeOAuthAccountRegistry, now time.Time, maxSessions int) int {
	used := make(map[int]struct{}, maxSessions)
	for _, entry := range registry.sessions {
		if entry != nil && entry.expiresAt.After(now) {
			used[entry.slot] = struct{}{}
		}
	}
	for slot := 1; slot <= maxSessions; slot++ {
		if _, ok := used[slot]; !ok {
			return slot
		}
	}
	return maxSessions
}

func setClaudeOAuthOutboundUserID(body []byte, account claudeOAuthAccountIdentity, sessionID string) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	var userID string
	switch account.Format {
	case "legacy":
		if account.UserHash == "" || account.AccountUUID == "" {
			return body, fmt.Errorf("missing legacy account identity")
		}
		userID = fmt.Sprintf("user_%s_account_%s_session_%s", account.UserHash, account.AccountUUID, sessionID)
	default:
		payload := map[string]string{
			"device_id":    account.DeviceID,
			"account_uuid": account.AccountUUID,
			"session_id":   sessionID,
		}
		raw, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return body, errMarshal
		}
		userID = string(raw)
	}
	return sjson.SetBytes(body, "metadata.user_id", userID)
}

// ResetClaudeOAuthFingerprintRegistry clears in-memory registries (tests).
func ResetClaudeOAuthFingerprintRegistry() {
	claudeOAuthRegistryMu.Lock()
	claudeOAuthRegistries = make(map[string]*claudeOAuthAccountRegistry)
	claudeOAuthRegistryMu.Unlock()
}
