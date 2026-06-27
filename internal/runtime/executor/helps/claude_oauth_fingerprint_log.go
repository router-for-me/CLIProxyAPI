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
	return []string{
		"ua=" + truncateClaudeOAuthHeaderLogValue(headers, "User-Agent", 40),
		"pkg=" + truncateClaudeOAuthHeaderLogValue(headers, "X-Stainless-Package-Version", 16),
		"srt=" + truncateClaudeOAuthHeaderLogValue(headers, "X-Stainless-Runtime", 16),
		"slang=" + truncateClaudeOAuthHeaderLogValue(headers, "X-Stainless-Lang", 16),
		"rtver=" + truncateClaudeOAuthHeaderLogValue(headers, "X-Stainless-Runtime-Version", 16),
		"os=" + truncateClaudeOAuthHeaderLogValue(headers, "X-Stainless-Os", 16),
		"arch=" + truncateClaudeOAuthHeaderLogValue(headers, "X-Stainless-Arch", 16),
		"retry=" + truncateClaudeOAuthHeaderLogValue(headers, "X-Stainless-Retry-Count", 8),
		"timeout=" + truncateClaudeOAuthHeaderLogValue(headers, "X-Stainless-Timeout", 16),
		"app=" + truncateClaudeOAuthHeaderLogValue(headers, "X-App", 16),
		"anthver=" + truncateClaudeOAuthHeaderLogValue(headers, "Anthropic-Version", 16),
		"direct=" + truncateClaudeOAuthHeaderLogValue(headers, "Anthropic-Dangerous-Direct-Browser-Access", 8),
		"accept=" + truncateClaudeOAuthHeaderLogValueEscaped(headers, "Accept", 32),
		"enc=" + truncateClaudeOAuthHeaderLogValueEscaped(headers, "Accept-Encoding", 64),
		"beta=" + truncateClaudeOAuthHeaderLogValue(headers, "Anthropic-Beta", 512),
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

func claudeOAuthBillingLogParts(body []byte) []string {
	billing := claudeOAuthBillingFromBody(body)
	if !billing.Found {
		return nil
	}
	return []string{
		"billing=" + truncateClaudeOAuthOptionalLogValue(escapeClaudeOAuthLogValue(billing.Raw), true, 128),
	}
}

type claudeOAuthBillingLog struct {
	Found bool
	Raw   string
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
		billing.Found = true
		billing.Raw = strings.TrimSpace(strings.TrimPrefix(text, "x-anthropic-billing-header:"))
		return false
	})
	return billing
}

func truncateClaudeOAuthHeaderLogValue(headers http.Header, name string, max int) string {
	values, ok := headers[http.CanonicalHeaderKey(name)]
	if !ok {
		return "n/a"
	}
	if len(values) == 0 {
		return "-"
	}
	return truncateClaudeOAuthOptionalLogValue(strings.Join(values, ","), true, max)
}

func truncateClaudeOAuthHeaderLogValueEscaped(headers http.Header, name string, max int) string {
	values, ok := headers[http.CanonicalHeaderKey(name)]
	if !ok {
		return "n/a"
	}
	if len(values) == 0 {
		return "-"
	}
	return truncateClaudeOAuthOptionalLogValue(escapeClaudeOAuthLogValue(strings.Join(values, ",")), true, max)
}

func escapeClaudeOAuthLogValue(value string) string {
	replacer := strings.NewReplacer(
		" ", "%20",
		"\t", "%09",
		"\n", "%0a",
		"\r", "%0d",
	)
	return replacer.Replace(value)
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

func truncateClaudeOAuthOptionalLogValue(value string, exists bool, max int) string {
	if !exists {
		return "n/a"
	}
	return truncateClaudeOAuthLogValue(value, max)
}

func itoa(v int) string {
	if v <= 0 {
		return "-"
	}
	return strconv.Itoa(v)
}
