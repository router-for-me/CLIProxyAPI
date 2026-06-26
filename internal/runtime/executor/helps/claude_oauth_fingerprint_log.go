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

// MaybeLogClaudeOAuthFingerprint appends one concise fingerprint line to a per-account log file.
func MaybeLogClaudeOAuthFingerprint(cfg *config.Config, auth *cliproxyauth.Auth, inboundHeaders, outboundHeaders http.Header, outboundBody []byte, model string, gateResult *ClaudeOAuthFingerprintGateResult) {
	if !ClaudeOAuthFingerprintLogEnabled(cfg) {
		return
	}
	line := formatClaudeOAuthFingerprintLine(inboundHeaders, outboundHeaders, outboundBody, model, gateResult)
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

func formatClaudeOAuthFingerprintLine(inboundHeaders, outboundHeaders http.Header, outboundBody []byte, model string, gateResult *ClaudeOAuthFingerprintGateResult) string {
	if gateResult == nil {
		return ""
	}
	parts := []string{
		time.Now().Format("01-02 15:04:05"),
		"session=" + truncateClaudeOAuthLogToken(gateResult.SessionID),
	}
	parts = append(parts, claudeOAuthIdentityLogParts(gateResult, outboundBody)...)
	if outboundHeaders != nil {
		parts = append(parts,
			"ua="+truncateClaudeOAuthLogValue(outboundHeaders.Get("User-Agent"), 40),
			"pkg="+truncateClaudeOAuthLogValue(outboundHeaders.Get("X-Stainless-Package-Version"), 16),
			"os="+truncateClaudeOAuthLogValue(outboundHeaders.Get("X-Stainless-Os"), 16),
			"arch="+truncateClaudeOAuthLogValue(outboundHeaders.Get("X-Stainless-Arch"), 16),
		)
	}
	if gateResult.Slot > 0 {
		parts = append(parts, "slot="+itoa(gateResult.Slot))
	}
	if model != "" {
		parts = append(parts, "model="+truncateClaudeOAuthLogValue(model, 32))
	}
	if entry := claudeOAuthEntrypointFromBody(outboundBody); entry != "" {
		parts = append(parts, "entry="+entry)
	}
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

func claudeOAuthIdentityLogParts(gateResult *ClaudeOAuthFingerprintGateResult, outboundBody []byte) []string {
	if gateResult.Format == "legacy" {
		return []string{
			"user=" + truncateClaudeOAuthLogToken(gateResult.UserHash),
			"account=" + truncateClaudeOAuthLogToken(gateResult.AccountID),
		}
	}
	deviceID := gateResult.DeviceID
	accountID := gateResult.AccountID
	if len(outboundBody) > 0 {
		userID := gjson.GetBytes(outboundBody, "metadata.user_id").String()
		if strings.HasPrefix(userID, "{") {
			if v := strings.TrimSpace(gjson.Get(userID, "device_id").String()); v != "" {
				deviceID = v
			}
			if v := strings.TrimSpace(gjson.Get(userID, "account_uuid").String()); v != "" {
				accountID = v
			}
		}
	}
	return []string{
		"device=" + truncateClaudeOAuthLogToken(deviceID),
		"account=" + truncateClaudeOAuthLogToken(accountID),
	}
}

func claudeOAuthEntrypointFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() {
		return ""
	}
	var entry string
	system.ForEach(func(_, part gjson.Result) bool {
		text := part.Get("text").String()
		if !strings.HasPrefix(text, "x-anthropic-billing-header:") {
			return true
		}
		for _, segment := range strings.Split(strings.TrimPrefix(text, "x-anthropic-billing-header:"), ";") {
			segment = strings.TrimSpace(segment)
			if strings.HasPrefix(segment, "cc_entrypoint=") {
				entry = strings.TrimPrefix(segment, "cc_entrypoint=")
				return false
			}
		}
		return true
	})
	return truncateClaudeOAuthLogValue(entry, 16)
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
