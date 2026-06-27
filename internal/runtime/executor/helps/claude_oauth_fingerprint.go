package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/claudeoauth"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	claudeOAuthViolationSessionLimit      = "session_limit"
	claudeOAuthViolationMissingBilling    = "missing_billing"
	claudeOAuthViolationInvalidCCVersion  = "invalid_cc_version"
	claudeOAuthViolationMissingSession    = "missing_session"
	claudeOAuthHTTPStatusTooManySessions  = 529
	claudeOAuthSessionLimitErrorBody      = `{"type":"error","error":{"type":"too_many_sessions","message":"too many sessions"}}`
	claudeOAuthFingerprintCleanupInterval = 15 * time.Minute
)

var (
	claudeOAuthLegacyUserIDPattern = regexp.MustCompile(`^user_([a-fA-F0-9]{64})_account_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})_session_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	claudeOAuthCCVersionPattern    = regexp.MustCompile(`^\d+\.\d+\.\d+\.[0-9a-fA-F]{3}$`)

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
	RequestID              string
	SessionID              string
	AffinitySessionID      string
	Slot                   int
	Violation              string
	OverrideApplied        bool
	SessionMismatch        bool
	HeaderSessionID        string
	BodySessionID          string
	InboundDeviceID        string
	InboundDeviceIDExists  bool
	InboundAccountID       string
	InboundAccountIDExists bool
	InboundUserHash        string
	InboundUserHashExists  bool
	InboundFormat          string
	DeviceID               string
	AccountID              string
	UserHash               string
	Format                 string
}

// ClaudeOAuthFingerprintError is returned when the session gate blocks a request.
type ClaudeOAuthFingerprintError struct {
	Code    string
	Message string
}

func (e *ClaudeOAuthFingerprintError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == claudeOAuthViolationSessionLimit {
		return claudeOAuthSessionLimitErrorBody
	}
	return e.Message
}

// HTTPStatus returns the HTTP status code for a fingerprint gate error.
func (e *ClaudeOAuthFingerprintError) HTTPStatus() int {
	if e == nil {
		return http.StatusTooManyRequests
	}
	switch e.Code {
	case claudeOAuthViolationSessionLimit,
		claudeOAuthViolationMissingBilling,
		claudeOAuthViolationInvalidCCVersion,
		claudeOAuthViolationMissingSession:
		return claudeOAuthHTTPStatusTooManySessions
	}
	return http.StatusTooManyRequests
}

// ClaudeOAuthFingerprintHTTPStatus resolves the HTTP status for fingerprint gate errors.
func ClaudeOAuthFingerprintHTTPStatus(err error) int {
	var fpErr *ClaudeOAuthFingerprintError
	if errors.As(err, &fpErr) && fpErr != nil {
		return fpErr.HTTPStatus()
	}
	return http.StatusTooManyRequests
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

func ClaudeOAuthOutboundSessionIDFromContext(ctx context.Context) (string, bool) {
	result := ClaudeOAuthFingerprintGateResultFromContext(ctx)
	if result == nil {
		return "", false
	}
	sessionID := strings.TrimSpace(result.SessionID)
	if sessionID == "" {
		return "", false
	}
	return sessionID, true
}

// ClaudeOAuthFingerprintGate resolves, registers, and normalizes OAuth outbound identity.
func ClaudeOAuthFingerprintGate(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, inboundHeaders http.Header, body []byte, model string) ([]byte, *ClaudeOAuthFingerprintGateResult, error) {
	return ClaudeOAuthFingerprintGateWithSessionPayload(ctx, cfg, auth, inboundHeaders, body, body, model)
}

// ClaudeOAuthFingerprintGateWithSessionPayload resolves, registers, and normalizes OAuth outbound identity.
// sessionPayload is the request view used for session-affinity selection. The
// outbound body remains separate so Claude OAuth identity/header normalization
// does not leak affinity key prefixes such as "claude:" or "msg:" upstream.
func ClaudeOAuthFingerprintGateWithSessionPayload(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, inboundHeaders http.Header, sessionPayload, body []byte, model string) ([]byte, *ClaudeOAuthFingerprintGateResult, error) {
	if cfg == nil || !cfg.ClaudeOAuthFingerprint.Enabled {
		return body, nil, nil
	}
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return body, nil, nil
	}
	if len(sessionPayload) == 0 {
		sessionPayload = body
	}

	headerSession := extractClaudeOAuthSessionFromHeader(inboundHeaders)
	bodySession := extractClaudeOAuthSessionFromBody(body)
	inboundIdentity := extractClaudeOAuthIdentityFromBody(body)
	affinitySession := cliproxyauth.ExtractSessionID(inboundHeaders, sessionPayload, nil)

	result := &ClaudeOAuthFingerprintGateResult{
		RequestID:              shortClaudeOAuthRequestID(ctx),
		HeaderSessionID:        headerSession,
		BodySessionID:          bodySession,
		AffinitySessionID:      affinitySession,
		InboundDeviceID:        inboundIdentity.DeviceID,
		InboundDeviceIDExists:  inboundIdentity.DeviceIDExists,
		InboundAccountID:       inboundIdentity.AccountUUID,
		InboundAccountIDExists: inboundIdentity.AccountUUIDExists,
		InboundUserHash:        inboundIdentity.UserHash,
		InboundUserHashExists:  inboundIdentity.UserHashExists,
		InboundFormat:          inboundIdentity.Format,
	}
	if headerSession != "" && bodySession != "" && headerSession != bodySession {
		result.SessionMismatch = true
	}

	result.SessionID = resolveClaudeOAuthCanonicalSessionID(headerSession, bodySession)

	authID := strings.TrimSpace(auth.ID)
	if violation := validateClaudeOAuthInboundFingerprint(body, result.SessionID); violation != "" {
		result.Violation = violation
		fillClaudeOAuthGateResultIdentity(result, claudeOAuthAccountIdentity{})
		MaybeLogClaudeOAuthFingerprintInbound(cfg, auth, inboundHeaders, body, model, result)
		logClaudeOAuthViolation(authID, result, cfg)
		return body, result, claudeOAuthFingerprintErrorFor(violation)
	}

	registry := registryForAuth(authID)
	overrideDevice := claudeoauth.OverrideDevice(cfg)
	profileIdentity, hasProfileIdentity := claudeOAuthAccountIdentityFromProfile(auth)
	hasProfileMetadata := claudeOAuthHasProfileMetadata(auth)
	if overrideDevice && hasProfileMetadata && !hasProfileIdentity {
		result.Violation = "missing_profile"
		fillClaudeOAuthGateResultIdentity(result, claudeOAuthAccountIdentity{})
		MaybeLogClaudeOAuthFingerprintInbound(cfg, auth, inboundHeaders, body, model, result)
		return body, result, fmt.Errorf("claude oauth fingerprint: missing complete auth profile")
	}
	applyOverrideDevice := overrideDevice && hasProfileIdentity
	ttl := claudeOAuthFingerprintSessionTTL(cfg)
	now := time.Now()

	claudeOAuthRegistryMu.Lock()
	purgeExpiredSessionsLocked(registry, now)
	if applyOverrideDevice {
		registry.account = profileIdentity
	} else if registry.account.Format == "" {
		registry.account = firstClaudeOAuthAccountIdentity(authID, inboundIdentity)
	}
	var slot int
	var sessionViolation string
	if affinitySession != "" {
		slot, sessionViolation = registerClaudeOAuthSessionLocked(registry, affinitySession, now, ttl, claudeOAuthFingerprintMaxSessions(cfg))
	}
	account := registry.account
	fillClaudeOAuthGateResultIdentity(result, account)
	result.Slot = slot
	result.Violation = sessionViolation
	claudeOAuthRegistryMu.Unlock()

	MaybeLogClaudeOAuthFingerprintInbound(cfg, auth, inboundHeaders, body, model, result)

	if result.Violation != "" {
		logClaudeOAuthViolation(authID, result, cfg)
		return body, result, claudeOAuthFingerprintErrorFor(result.Violation)
	}

	if !applyOverrideDevice {
		return body, result, nil
	}

	outBody, changed, errSet := setClaudeOAuthOutboundUserID(body, account)
	if errSet != nil {
		return body, result, fmt.Errorf("claude oauth fingerprint: set user_id: %w", errSet)
	}
	result.OverrideApplied = changed
	return outBody, result, nil
}

func claudeOAuthFingerprintErrorFor(violation string) error {
	switch violation {
	case claudeOAuthViolationSessionLimit:
		return &ClaudeOAuthFingerprintError{
			Code:    claudeOAuthViolationSessionLimit,
			Message: "too many sessions",
		}
	default:
		return &ClaudeOAuthFingerprintError{Code: violation, Message: "claude oauth fingerprint: request blocked"}
	}
}

func logClaudeOAuthViolation(authID string, result *ClaudeOAuthFingerprintGateResult, cfg *config.Config) {
	if result == nil || cfg == nil {
		return
	}
	log.WithFields(log.Fields{
		"auth_id":   authID,
		"session":   truncateClaudeOAuthLogToken(result.SessionID),
		"slot":      result.Slot,
		"violation": result.Violation,
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

func claudeOAuthAccountIdentityFromProfile(auth *cliproxyauth.Auth) (claudeOAuthAccountIdentity, bool) {
	profile, ok := claudeoauth.ProfileFromAuth(auth)
	if !ok || !claudeoauth.ValidDeviceID(profile.DeviceID) {
		return claudeOAuthAccountIdentity{}, false
	}
	return claudeOAuthAccountIdentity{
		Format:      "json",
		DeviceID:    strings.TrimSpace(profile.DeviceID),
		AccountUUID: strings.TrimSpace(profile.AccountUUID),
	}, true
}

func claudeOAuthHasProfileMetadata(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	raw, ok := auth.Metadata[claudeoauth.ProfileMetadataKey]
	return ok && raw != nil
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

func validateClaudeOAuthInboundFingerprint(body []byte, sessionID string) string {
	ccVersion, ok := extractClaudeOAuthBillingCCVersion(body)
	if !ok {
		return claudeOAuthViolationMissingBilling
	}
	if !claudeOAuthCCVersionPattern.MatchString(strings.TrimSpace(ccVersion)) {
		return claudeOAuthViolationInvalidCCVersion
	}
	if strings.TrimSpace(sessionID) == "" {
		return claudeOAuthViolationMissingSession
	}
	return ""
}

func extractClaudeOAuthBillingCCVersion(body []byte) (string, bool) {
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() {
		return "", false
	}
	var version string
	found := false
	system.ForEach(func(_, part gjson.Result) bool {
		text := part.Get("text").String()
		if !strings.HasPrefix(text, "x-anthropic-billing-header:") {
			return true
		}
		found = true
		for _, segment := range strings.Split(strings.TrimPrefix(text, "x-anthropic-billing-header:"), ";") {
			segment = strings.TrimSpace(segment)
			if strings.HasPrefix(segment, "cc_version=") {
				version = strings.TrimSpace(strings.TrimPrefix(segment, "cc_version="))
				return false
			}
		}
		return false
	})
	return version, found
}

type claudeOAuthInboundIdentity struct {
	Format            string
	UserHash          string
	UserHashExists    bool
	AccountUUID       string
	AccountUUIDExists bool
	DeviceID          string
	DeviceIDExists    bool
}

func extractClaudeOAuthIdentityFromBody(payload []byte) claudeOAuthInboundIdentity {
	userResult := gjson.GetBytes(payload, "metadata.user_id")
	if !userResult.Exists() {
		return claudeOAuthInboundIdentity{}
	}
	userID := strings.TrimSpace(userResult.String())
	if userID == "" {
		return claudeOAuthInboundIdentity{}
	}
	if matches := claudeOAuthLegacyUserIDPattern.FindStringSubmatch(userID); len(matches) == 4 {
		return claudeOAuthInboundIdentity{
			Format:            "legacy",
			UserHash:          matches[1],
			UserHashExists:    true,
			AccountUUID:       matches[2],
			AccountUUIDExists: true,
		}
	}
	if strings.HasPrefix(userID, "{") {
		deviceResult := gjson.Get(userID, "device_id")
		accountResult := gjson.Get(userID, "account_uuid")
		identity := claudeOAuthInboundIdentity{Format: "json"}
		if deviceResult.Exists() {
			identity.DeviceID = strings.TrimSpace(deviceResult.String())
			identity.DeviceIDExists = true
		}
		if accountResult.Exists() {
			identity.AccountUUID = strings.TrimSpace(accountResult.String())
			identity.AccountUUIDExists = true
		}
		return identity
	}
	return claudeOAuthInboundIdentity{}
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
	return hex.EncodeToString(sum[:])
}

func shortClaudeOAuthRequestID(ctx context.Context) string {
	requestID := strings.TrimSpace(logging.GetRequestID(ctx))
	if requestID == "" {
		requestID = logging.GenerateRequestID()
	}
	if len(requestID) > 8 {
		return requestID[:8]
	}
	return requestID
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

func setClaudeOAuthOutboundUserID(body []byte, account claudeOAuthAccountIdentity) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	userID := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String())
	if userID == "" {
		return body, false, nil
	}
	if matches := claudeOAuthLegacyUserIDPattern.FindStringSubmatch(userID); len(matches) == 4 {
		userHash := strings.TrimSpace(account.DeviceID)
		if userHash == "" {
			userHash = strings.TrimSpace(account.UserHash)
		}
		accountUUID := strings.TrimSpace(account.AccountUUID)
		if userHash == "" {
			return body, false, fmt.Errorf("missing account identity")
		}
		outUserID, errSetDevice := sjson.Set("{}", "device_id", userHash)
		if errSetDevice != nil {
			return body, false, errSetDevice
		}
		outUserID, errSetAccount := sjson.Set(outUserID, "account_uuid", accountUUID)
		if errSetAccount != nil {
			return body, false, errSetAccount
		}
		outUserID, errSetSession := sjson.Set(outUserID, "session_id", matches[3])
		if errSetSession != nil {
			return body, false, errSetSession
		}
		out, errSet := sjson.SetBytes(body, "metadata.user_id", outUserID)
		return out, true, errSet
	}
	if !strings.HasPrefix(userID, "{") {
		return body, false, nil
	}
	if strings.TrimSpace(account.DeviceID) == "" {
		return body, false, fmt.Errorf("missing account identity")
	}
	outUserID, errSetDevice := sjson.Set(userID, "device_id", strings.TrimSpace(account.DeviceID))
	if errSetDevice != nil {
		return body, false, errSetDevice
	}
	outUserID, errSetAccount := sjson.Set(outUserID, "account_uuid", strings.TrimSpace(account.AccountUUID))
	if errSetAccount != nil {
		return body, false, errSetAccount
	}
	out, errSet := sjson.SetBytes(body, "metadata.user_id", outUserID)
	return out, outUserID != userID, errSet
}

// ResetClaudeOAuthFingerprintRegistry clears in-memory registries (tests).
func ResetClaudeOAuthFingerprintRegistry() {
	claudeOAuthRegistryMu.Lock()
	claudeOAuthRegistries = make(map[string]*claudeOAuthAccountRegistry)
	claudeOAuthRegistryMu.Unlock()
}
