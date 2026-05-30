package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const (
	contentSafetyLogDirEnv       = "CLIPROXY_CONTENT_SAFETY_LOG_DIR"
	contentSafetyLogSubdir       = "content-safety-451"
	contentSafetyLogMaxBodyBytes = 256 * 1024
	contentSafetyLogMaxStringLen = 8192
	contentSafetyLogMaxErrorLen  = 4096
)

type contentSafetyLogRecord struct {
	Timestamp              string         `json:"timestamp"`
	RequestID              string         `json:"request_id,omitempty"`
	Provider               string         `json:"provider,omitempty"`
	AuthID                 string         `json:"auth_id,omitempty"`
	AuthIndex              string         `json:"auth_index,omitempty"`
	AuthLabel              string         `json:"auth_label,omitempty"`
	AuthPrefix             string         `json:"auth_prefix,omitempty"`
	RouteModel             string         `json:"route_model,omitempty"`
	RequestedModel         string         `json:"requested_model,omitempty"`
	UpstreamModel          string         `json:"upstream_model,omitempty"`
	StatusCode             int            `json:"status_code,omitempty"`
	Error                  string         `json:"error,omitempty"`
	RequestBody            map[string]any `json:"request_body,omitempty"`
	RequestBodyText        string         `json:"request_body_text,omitempty"`
	RequestBodyTruncated   bool           `json:"request_body_truncated,omitempty"`
	RequestBodyBytes       int            `json:"request_body_bytes,omitempty"`
	OriginalRequest        map[string]any `json:"original_request,omitempty"`
	OriginalRequestText    string         `json:"original_request_text,omitempty"`
	OriginalRequestTrunc   bool           `json:"original_request_truncated,omitempty"`
	OriginalRequestBytes   int            `json:"original_request_bytes,omitempty"`
	OriginalRequestPresent bool           `json:"original_request_present,omitempty"`
}

func (m *Manager) recordContentSafetyRequest(ctx context.Context, auth *Auth, provider, routeModel, upstreamModel string, opts cliproxyexecutor.Options, payload []byte, err error) {
	if m == nil || !isRequestScopedContentSafetyError(err) {
		return
	}
	path := m.contentSafetyLogFilePath(time.Now())
	if path == "" {
		return
	}

	record := contentSafetyLogRecord{
		Timestamp:        time.Now().UTC().Format(time.RFC3339Nano),
		RequestID:        logging.GetRequestID(ctx),
		Provider:         strings.TrimSpace(provider),
		RouteModel:       strings.TrimSpace(routeModel),
		RequestedModel:   requestedModelAliasFromOptions(opts, routeModel),
		UpstreamModel:    strings.TrimSpace(upstreamModel),
		StatusCode:       statusCodeFromError(err),
		Error:            truncateContentSafetyString(err.Error(), contentSafetyLogMaxErrorLen),
		RequestBodyBytes: len(payload),
	}
	if record.StatusCode == 0 {
		record.StatusCode = http.StatusUnavailableForLegalReasons
	}
	if auth != nil {
		record.AuthID = strings.TrimSpace(auth.ID)
		record.AuthIndex = authMetricIndex(auth)
		record.AuthLabel = strings.TrimSpace(auth.Label)
		record.AuthPrefix = strings.TrimSpace(auth.Prefix)
	}

	body, bodyText, truncated := sanitizeContentSafetyPayload(payload)
	record.RequestBody = body
	record.RequestBodyText = bodyText
	record.RequestBodyTruncated = truncated

	if len(opts.OriginalRequest) > 0 && !bytes.Equal(opts.OriginalRequest, payload) {
		original, originalText, originalTruncated := sanitizeContentSafetyPayload(opts.OriginalRequest)
		record.OriginalRequest = original
		record.OriginalRequestText = originalText
		record.OriginalRequestTrunc = originalTruncated
		record.OriginalRequestBytes = len(opts.OriginalRequest)
		record.OriginalRequestPresent = true
	}

	line, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		logEntryWithRequestID(ctx).WithError(errMarshal).Warn("marshal content safety log failed")
		return
	}
	line = append(line, '\n')

	if errWrite := appendContentSafetyLogLine(path, line); errWrite != nil {
		logEntryWithRequestID(ctx).WithError(errWrite).Warn("write content safety log failed")
	}
}

func (m *Manager) contentSafetyLogFilePath(now time.Time) string {
	dir := strings.TrimSpace(os.Getenv(contentSafetyLogDirEnv))
	if dir == "" {
		cfg := m.configSnapshot()
		dir = filepath.Join(logging.ResolveLogDirectory(cfg), contentSafetyLogSubdir)
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(dir), now.Format("2006-01-02")+".jsonl")
}

func (m *Manager) configSnapshot() *internalconfig.Config {
	if m == nil {
		return nil
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return cfg
}

func appendContentSafetyLogLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create content safety log directory: %w", err)
	}
	f, errOpen := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if errOpen != nil {
		return fmt.Errorf("open content safety log file: %w", errOpen)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.WithError(errClose).Warn("close content safety log file failed")
		}
	}()
	if _, errWrite := f.Write(line); errWrite != nil {
		return fmt.Errorf("write content safety log file: %w", errWrite)
	}
	return nil
}

func sanitizeContentSafetyPayload(payload []byte) (map[string]any, string, bool) {
	if len(payload) == 0 {
		return nil, "", false
	}
	truncated := len(payload) > contentSafetyLogMaxBodyBytes

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var decoded any
	if errDecode := decoder.Decode(&decoded); errDecode == nil {
		if obj, ok := redactContentSafetyValue(decoded).(map[string]any); ok {
			return obj, "", truncated
		}
		encoded, errMarshal := json.Marshal(redactContentSafetyValue(decoded))
		if errMarshal == nil {
			return nil, string(encoded), truncated
		}
	}

	raw := payload
	if len(raw) > contentSafetyLogMaxBodyBytes {
		raw = raw[:contentSafetyLogMaxBodyBytes]
	}
	text := truncateContentSafetyString(string(raw), contentSafetyLogMaxBodyBytes)
	return nil, text, truncated
}

func redactContentSafetyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if isContentSafetySensitiveKey(key) {
				out[key] = "[redacted]"
				continue
			}
			out[key] = redactContentSafetyValue(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactContentSafetyValue(child)
		}
		return out
	case string:
		return sanitizeContentSafetyString(typed)
	default:
		return typed
	}
}

func isContentSafetySensitiveKey(key string) bool {
	normalized := normalizeContentSafetyKey(key)
	switch normalized {
	case "authorization", "apikey", "xapikey", "accesskey", "secretkey", "bearer",
		"password", "secret", "clientsecret", "cookie", "setcookie", "proxy",
		"accesstoken", "refreshtoken", "idtoken", "token":
		return true
	default:
		return strings.HasSuffix(normalized, "token") ||
			strings.HasSuffix(normalized, "secret") ||
			strings.HasSuffix(normalized, "apikey")
	}
}

func normalizeContentSafetyKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	var b strings.Builder
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sanitizeContentSafetyString(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return value
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "data:image/") ||
		strings.HasPrefix(lower, "data:audio/") ||
		strings.HasPrefix(lower, "data:video/") ||
		looksLikeLargeBase64(trimmed) {
		return fmt.Sprintf("[redacted large string len=%d]", len(value))
	}
	return truncateContentSafetyString(value, contentSafetyLogMaxStringLen)
}

func looksLikeLargeBase64(value string) bool {
	if len(value) < contentSafetyLogMaxStringLen {
		return false
	}
	total := 0
	whitespace := 0
	checked := 0
	base64Chars := 0
	for _, r := range value {
		total++
		if unicode.IsSpace(r) {
			whitespace++
			continue
		}
		checked++
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=' {
			base64Chars++
		}
		if checked >= contentSafetyLogMaxStringLen {
			break
		}
	}
	return total > 0 && whitespace*100/total <= 1 && checked > 0 && base64Chars*100/checked >= 98
}

func truncateContentSafetyString(value string, maxLen int) string {
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen] + fmt.Sprintf("...[truncated %d bytes]", len(value)-maxLen)
}
