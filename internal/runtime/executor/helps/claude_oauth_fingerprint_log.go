package helps

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

var (
	claudeOAuthFingerprintLogMu    sync.Mutex
	claudeOAuthFingerprintSafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
)

// ClaudeOAuthFingerprintLogEnabled reports whether per-request fingerprint lines should print.
func ClaudeOAuthFingerprintLogEnabled(cfg *config.Config) bool {
	if cfg == nil || !cfg.ClaudeOAuthFingerprint.Enabled {
		return false
	}
	return cfg.ClaudeOAuthFingerprint.LogFingerprint
}

// MaybeLogClaudeOAuthFingerprint appends one outbound fingerprint line to a per-account log file.
func MaybeLogClaudeOAuthFingerprint(cfg *config.Config, auth *cliproxyauth.Auth, inboundHeaders, outboundHeaders http.Header, outboundBody []byte, model string, gateResult *ClaudeOAuthFingerprintGateResult) {
	MaybeLogClaudeOAuthFingerprintOutbound(cfg, auth, inboundHeaders, outboundHeaders, outboundBody, model, gateResult)
}

func MaybeLogClaudeOAuthFingerprintInbound(cfg *config.Config, auth *cliproxyauth.Auth, inboundHeaders http.Header, inboundBody []byte, model string, gateResult *ClaudeOAuthFingerprintGateResult) {
	maybeLogClaudeOAuthFingerprintLine(cfg, auth, "in", inboundHeaders, nil, inboundBody, model, gateResult)
}

func MaybeLogClaudeOAuthFingerprintOutbound(cfg *config.Config, auth *cliproxyauth.Auth, inboundHeaders, outboundHeaders http.Header, outboundBody []byte, model string, gateResult *ClaudeOAuthFingerprintGateResult) {
	maybeLogClaudeOAuthFingerprintLine(cfg, auth, "out", inboundHeaders, outboundHeaders, outboundBody, model, gateResult)
}

func maybeLogClaudeOAuthFingerprintLine(cfg *config.Config, auth *cliproxyauth.Auth, direction string, inboundHeaders, outboundHeaders http.Header, body []byte, model string, gateResult *ClaudeOAuthFingerprintGateResult) {
	if !ClaudeOAuthFingerprintLogEnabled(cfg) {
		return
	}
	line := formatClaudeOAuthFingerprintLine(direction, inboundHeaders, outboundHeaders, body, model, gateResult)
	if line == "" {
		return
	}
	if errWrite := appendClaudeOAuthFingerprintLogLine(cfg, auth, line); errWrite != nil {
		log.WithError(errWrite).Warn("claude oauth fingerprint: failed to write log file")
	}
}

func claudeOAuthFingerprintLogDir(cfg *config.Config) string {
	if cfg != nil {
		if dir := strings.TrimSpace(cfg.ClaudeOAuthFingerprint.LogDir); dir != "" {
			return dir
		}
	}
	return filepath.Join(logging.ResolveLogDirectory(cfg), "oauth-fingerprint")
}

func claudeOAuthFingerprintLogFileName(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return "unknown.log"
	}
	_, account := auth.AccountInfo()
	account = strings.TrimSpace(account)
	if account != "" {
		return claudeOAuthFingerprintSafeName.ReplaceAllString(account, "_") + ".log"
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return "unknown.log"
	}
	return claudeOAuthFingerprintSafeName.ReplaceAllString(authID, "_") + ".log"
}

func appendClaudeOAuthFingerprintLogLine(cfg *config.Config, auth *cliproxyauth.Auth, line string) error {
	dir := claudeOAuthFingerprintLogDir(cfg)
	if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
		return errMkdir
	}
	path := filepath.Join(dir, claudeOAuthFingerprintLogFileName(auth))

	claudeOAuthFingerprintLogMu.Lock()
	defer claudeOAuthFingerprintLogMu.Unlock()

	f, errOpen := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if errOpen != nil {
		return errOpen
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.WithError(errClose).Warn("claude oauth fingerprint: failed to close log file")
		}
	}()

	if _, errWrite := f.WriteString(line + "\n"); errWrite != nil {
		return errWrite
	}
	return nil
}

func formatClaudeOAuthFingerprintLine(direction string, inboundHeaders, outboundHeaders http.Header, body []byte, model string, gateResult *ClaudeOAuthFingerprintGateResult) string {
	if gateResult == nil {
		return ""
	}
	direction = strings.TrimSpace(direction)
	if direction == "" {
		direction = "out"
	}
	parts := []string{
		time.Now().Format("01-02 15:04:05"),
		"dir=" + direction,
		"req=" + truncateClaudeOAuthLogToken(gateResult.RequestID),
		"session=" + truncateClaudeOAuthLogToken(claudeOAuthLogSession(direction, inboundHeaders, outboundHeaders, body)),
		"header_session=" + truncateClaudeOAuthLogToken(claudeOAuthHeaderSession(direction, inboundHeaders, outboundHeaders)),
	}
	if direction == "in" {
		parts = append(parts, claudeOAuthIdentityLogParts(claudeOAuthInboundLogIdentity(gateResult))...)
	} else {
		parts = append(parts, claudeOAuthIdentityLogParts(claudeOAuthLogIdentityFromBody(body))...)
		parts = append(parts, claudeOAuthInboundMismatchLogParts(gateResult, body)...)
	}
	if direction == "out" && outboundHeaders != nil {
		parts = append(parts, claudeOAuthOutboundHeaderLogParts(outboundHeaders)...)
	}
	if gateResult.Slot > 0 {
		parts = append(parts, "slot="+itoa(gateResult.Slot))
	}
	if model != "" {
		parts = append(parts, "model="+truncateClaudeOAuthLogValue(model, 32))
	}
	parts = append(parts, claudeOAuthBillingLogParts(body)...)
	violation := gateResult.Violation
	if violation == "" {
		violation = "-"
	}
	parts = append(parts, "violation="+violation)
	if gateResult.SessionMismatch {
		parts = append(parts,
			"warn=session_mismatch",
			"hdr="+truncateClaudeOAuthLogToken(gateResult.HeaderSessionID),
			"body="+truncateClaudeOAuthLogToken(gateResult.BodySessionID),
		)
	}
	_ = inboundHeaders
	return strings.Join(parts, " ")
}

func claudeOAuthOutboundHeaderLogParts(headers http.Header) []string {
	// Log only headers that vary per client/version or are not fully pinned by
	// claude-header-defaults + stabilize-device-profile. Constants such as
	// X-App, Anthropic-Version, X-Stainless-Runtime/Lang/Retry-Count, and Timeout
	// are omitted.
	return []string{
		"ua=" + truncateClaudeOAuthLogValue(headers.Get("User-Agent"), 40),
		"pkg=" + truncateClaudeOAuthLogValue(headers.Get("X-Stainless-Package-Version"), 16),
		"rtver=" + truncateClaudeOAuthLogValue(headers.Get("X-Stainless-Runtime-Version"), 16),
		"os=" + truncateClaudeOAuthLogValue(headers.Get("X-Stainless-Os"), 16),
		"arch=" + truncateClaudeOAuthLogValue(headers.Get("X-Stainless-Arch"), 16),
		"beta=" + truncateClaudeOAuthLogValue(headers.Get("Anthropic-Beta"), 64),
	}
}

func claudeOAuthLogSession(direction string, inboundHeaders, outboundHeaders http.Header, body []byte) string {
	headerSession := ""
	if direction == "in" {
		headerSession = extractClaudeOAuthSessionFromHeader(inboundHeaders)
	} else {
		headerSession = extractClaudeOAuthSessionFromHeader(outboundHeaders)
	}
	return resolveClaudeOAuthCanonicalSessionID(headerSession, extractClaudeOAuthSessionFromBody(body))
}

func claudeOAuthHeaderSession(direction string, inboundHeaders, outboundHeaders http.Header) string {
	if direction == "in" {
		return extractClaudeOAuthSessionFromHeader(inboundHeaders)
	}
	return extractClaudeOAuthSessionFromHeader(outboundHeaders)
}

func claudeOAuthIdentityLogParts(identity claudeOAuthLogIdentity) []string {
	if identity.Format == "legacy" {
		return []string{
			"user=" + truncateClaudeOAuthLogToken(identity.UserHash),
			"account=" + truncateClaudeOAuthLogToken(identity.AccountID),
		}
	}
	return []string{
		"device=" + truncateClaudeOAuthLogToken(identity.DeviceID),
		"account=" + truncateClaudeOAuthLogToken(identity.AccountID),
	}
}

func claudeOAuthInboundMismatchLogParts(gateResult *ClaudeOAuthFingerprintGateResult, outboundBody []byte) []string {
	inbound := claudeOAuthInboundLogIdentity(gateResult)
	if inbound.Format == "" {
		return nil
	}
	outbound := claudeOAuthLogIdentityFromBody(outboundBody)
	if !claudeOAuthLogIdentityMismatch(inbound, outbound) {
		return nil
	}
	if inbound.Format == "legacy" {
		return []string{
			"warn=identity_mismatch",
			"in_user=" + truncateClaudeOAuthLogToken(inbound.UserHash),
			"in_account=" + truncateClaudeOAuthLogToken(inbound.AccountID),
		}
	}
	return []string{
		"warn=identity_mismatch",
		"in_device=" + truncateClaudeOAuthLogToken(inbound.DeviceID),
		"in_account=" + truncateClaudeOAuthLogToken(inbound.AccountID),
	}
}

type claudeOAuthLogIdentity struct {
	Format    string
	DeviceID  string
	AccountID string
	UserHash  string
}

func claudeOAuthInboundLogIdentity(gateResult *ClaudeOAuthFingerprintGateResult) claudeOAuthLogIdentity {
	if gateResult == nil {
		return claudeOAuthLogIdentity{}
	}
	return claudeOAuthLogIdentity{
		Format:    gateResult.InboundFormat,
		DeviceID:  gateResult.InboundDeviceID,
		AccountID: gateResult.InboundAccountID,
		UserHash:  gateResult.InboundUserHash,
	}
}

func claudeOAuthLogIdentityFromBody(body []byte) claudeOAuthLogIdentity {
	inbound := extractClaudeOAuthIdentityFromBody(body)
	return claudeOAuthLogIdentity{
		Format:    inbound.Format,
		DeviceID:  inbound.DeviceID,
		AccountID: inbound.AccountUUID,
		UserHash:  inbound.UserHash,
	}
}

func claudeOAuthLogIdentityMismatch(inbound, outbound claudeOAuthLogIdentity) bool {
	if inbound.Format == "legacy" {
		if outbound.Format != "legacy" {
			return true
		}
		return inbound.UserHash != "" && outbound.UserHash != "" && !strings.EqualFold(inbound.UserHash, outbound.UserHash) ||
			inbound.AccountID != "" && outbound.AccountID != "" && inbound.AccountID != outbound.AccountID
	}
	if inbound.DeviceID != "" && outbound.DeviceID != "" && inbound.DeviceID != outbound.DeviceID {
		return true
	}
	return inbound.AccountID != "" && outbound.AccountID != "" && inbound.AccountID != outbound.AccountID
}

func claudeOAuthBillingLogParts(body []byte) []string {
	billing := claudeOAuthBillingFromBody(body)
	if billing.Version == "" && billing.Entrypoint == "" && billing.CCH == "" {
		return nil
	}
	return []string{
		"ccver=" + truncateClaudeOAuthLogValue(billing.Version, 24),
		"entry=" + truncateClaudeOAuthLogValue(billing.Entrypoint, 16),
		"cch=" + truncateClaudeOAuthLogValue(billing.CCH, 8),
	}
}

type claudeOAuthBillingLog struct {
	Version    string
	Entrypoint string
	CCH        string
}

func claudeOAuthBillingFromBody(body []byte) claudeOAuthBillingLog {
	if len(body) == 0 {
		return claudeOAuthBillingLog{}
	}
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() {
		return claudeOAuthBillingLog{}
	}
	var billing claudeOAuthBillingLog
	system.ForEach(func(_, part gjson.Result) bool {
		text := part.Get("text").String()
		if !strings.HasPrefix(text, "x-anthropic-billing-header:") {
			return true
		}
		for _, segment := range strings.Split(strings.TrimPrefix(text, "x-anthropic-billing-header:"), ";") {
			segment = strings.TrimSpace(segment)
			switch {
			case strings.HasPrefix(segment, "cc_version="):
				billing.Version = strings.TrimPrefix(segment, "cc_version=")
			case strings.HasPrefix(segment, "cc_entrypoint="):
				billing.Entrypoint = strings.TrimPrefix(segment, "cc_entrypoint=")
			case strings.HasPrefix(segment, "cch="):
				billing.CCH = strings.TrimPrefix(segment, "cch=")
			}
		}
		return false
	})
	return billing
}

func truncateClaudeOAuthLogToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func truncateClaudeOAuthLogValue(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	if max > 0 && len(value) > max {
		return value[:max]
	}
	return value
}

func itoa(v int) string {
	if v <= 0 {
		return "-"
	}
	return strconv.Itoa(v)
}
