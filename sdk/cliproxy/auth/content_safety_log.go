package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	contentSafetyLogDirEnv               = "CLIPROXY_CONTENT_SAFETY_LOG_DIR"
	contentSafetyLogSubdir               = "content-safety-451"
	contentSafetyLogMaxBodyBytes         = 256 * 1024
	contentSafetyLogMaxPreviewBytes      = 1024
	contentSafetyLogFallbackPreviewBytes = 256
	contentSafetyLogMaxStringLen         = 1024
	contentSafetyLogMaxMetadataLen       = 256
	contentSafetyLogMaxErrorLen          = 1024
	contentSafetyLogFallbackErrorLen     = 256
	contentSafetyLogMaxRecordBytes       = 16 * 1024
	contentSafetyLogOmittedPreview       = "[omitted to cap log line]"
)

type contentSafetyLogRecord struct {
	Timestamp                string `json:"timestamp"`
	RequestID                string `json:"request_id,omitempty"`
	Provider                 string `json:"provider,omitempty"`
	AuthID                   string `json:"auth_id,omitempty"`
	AuthIndex                string `json:"auth_index,omitempty"`
	AuthLabel                string `json:"auth_label,omitempty"`
	AuthPrefix               string `json:"auth_prefix,omitempty"`
	RouteModel               string `json:"route_model,omitempty"`
	RequestedModel           string `json:"requested_model,omitempty"`
	UpstreamModel            string `json:"upstream_model,omitempty"`
	RequestPath              string `json:"request_path,omitempty"`
	StatusCode               int    `json:"status_code,omitempty"`
	SafetyCode               string `json:"safety_code,omitempty"`
	SafetyDirection          string `json:"safety_direction,omitempty"`
	Error                    string `json:"error,omitempty"`
	PayloadPreview           string `json:"payload_preview,omitempty"`
	PayloadTruncated         bool   `json:"payload_truncated,omitempty"`
	PayloadBytes             int    `json:"payload_bytes,omitempty"`
	PayloadSHA256            string `json:"payload_sha256,omitempty"`
	OriginalRequestPresent   bool   `json:"original_request_present,omitempty"`
	OriginalRequestPreview   string `json:"original_request_preview,omitempty"`
	OriginalRequestTruncated bool   `json:"original_request_truncated,omitempty"`
	OriginalRequestBytes     int    `json:"original_request_bytes,omitempty"`
	OriginalRequestSHA256    string `json:"original_request_sha256,omitempty"`
}

type contentSafetyPayloadSummary struct {
	Preview   string
	Truncated bool
	Bytes     int
	SHA256    string
}

func (m *Manager) recordContentSafetyRequest(ctx context.Context, auth *Auth, provider, routeModel, upstreamModel string, opts cliproxyexecutor.Options, payload []byte, err error) {
	if m == nil || !isRequestScopedContentSafetyError(err) {
		return
	}
	path := m.contentSafetyLogFilePath(time.Now())
	if path == "" {
		return
	}
	safetyCode, safetyDirection := contentSafetyErrorDetails(err)

	record := contentSafetyLogRecord{
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		RequestID:       truncateContentSafetyString(logging.GetRequestID(ctx), contentSafetyLogMaxMetadataLen),
		Provider:        truncateContentSafetyString(strings.TrimSpace(provider), contentSafetyLogMaxMetadataLen),
		RouteModel:      truncateContentSafetyString(strings.TrimSpace(routeModel), contentSafetyLogMaxMetadataLen),
		RequestedModel:  truncateContentSafetyString(requestedModelAliasFromOptions(opts, routeModel), contentSafetyLogMaxMetadataLen),
		UpstreamModel:   truncateContentSafetyString(strings.TrimSpace(upstreamModel), contentSafetyLogMaxMetadataLen),
		RequestPath:     truncateContentSafetyString(contentSafetyMetadataString(opts.Metadata, cliproxyexecutor.RequestPathMetadataKey), contentSafetyLogMaxMetadataLen),
		StatusCode:      statusCodeFromError(err),
		SafetyCode:      truncateContentSafetyString(safetyCode, contentSafetyLogMaxMetadataLen),
		SafetyDirection: truncateContentSafetyString(safetyDirection, contentSafetyLogMaxMetadataLen),
		Error:           truncateContentSafetyString(err.Error(), contentSafetyLogMaxErrorLen),
	}
	if record.StatusCode == 0 {
		record.StatusCode = http.StatusUnavailableForLegalReasons
	}
	if auth != nil {
		record.AuthID = truncateContentSafetyString(strings.TrimSpace(auth.ID), contentSafetyLogMaxMetadataLen)
		record.AuthIndex = authMetricIndex(auth)
		record.AuthLabel = truncateContentSafetyString(strings.TrimSpace(auth.Label), contentSafetyLogMaxMetadataLen)
		record.AuthPrefix = truncateContentSafetyString(strings.TrimSpace(auth.Prefix), contentSafetyLogMaxMetadataLen)
	}

	payloadSummary := summarizeContentSafetyPayload(payload)
	record.PayloadPreview = payloadSummary.Preview
	record.PayloadTruncated = payloadSummary.Truncated
	record.PayloadBytes = payloadSummary.Bytes
	record.PayloadSHA256 = payloadSummary.SHA256

	if len(opts.OriginalRequest) > 0 && !bytes.Equal(opts.OriginalRequest, payload) {
		originalSummary := summarizeContentSafetyPayload(opts.OriginalRequest)
		record.OriginalRequestPresent = true
		record.OriginalRequestPreview = originalSummary.Preview
		record.OriginalRequestTruncated = originalSummary.Truncated
		record.OriginalRequestBytes = originalSummary.Bytes
		record.OriginalRequestSHA256 = originalSummary.SHA256
	}

	line, errMarshal := marshalContentSafetyLogRecord(record)
	if errMarshal != nil {
		logEntryWithRequestID(ctx).WithError(errMarshal).Warn("marshal content safety log failed")
		return
	}

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

func contentSafetyMetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func contentSafetyErrorDetails(err error) (string, string) {
	code := normalizeContentSafetyCode(errorCodeFromError(err))
	message := errorString(err)
	switch {
	case isMiniMaxInputNewSensitiveSignal(code, message):
		return fallbackContentSafetyCode(code, "1026"), "input"
	case isMiniMaxOutputNewSensitiveSignal(code, message):
		return fallbackContentSafetyCode(code, "1027"), "output"
	default:
		return code, ""
	}
}

func normalizeContentSafetyCode(code string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(code)), `"'(),:;[]{}<>`)
}

func fallbackContentSafetyCode(code string, fallback string) string {
	if strings.TrimSpace(code) != "" {
		return code
	}
	return fallback
}

func summarizeContentSafetyPayload(payload []byte) contentSafetyPayloadSummary {
	if len(payload) == 0 {
		return contentSafetyPayloadSummary{}
	}
	preview, truncated := sanitizeContentSafetyPayloadPreview(payload)
	return contentSafetyPayloadSummary{
		Preview:   preview,
		Truncated: truncated,
		Bytes:     len(payload),
		SHA256:    contentSafetySHA256Hex(payload),
	}
}

func sanitizeContentSafetyPayloadPreview(payload []byte) (string, bool) {
	if len(payload) == 0 {
		return "", false
	}
	truncated := len(payload) > contentSafetyLogMaxBodyBytes

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var decoded any
	if errDecode := decoder.Decode(&decoded); errDecode == nil {
		encoded, errMarshal := json.Marshal(redactContentSafetyValue(decoded))
		if errMarshal == nil {
			preview, previewTruncated := truncateContentSafetyStringWithFlag(string(encoded), contentSafetyLogMaxPreviewBytes)
			return preview, truncated || previewTruncated
		}
	}

	raw := payload
	if len(raw) > contentSafetyLogMaxBodyBytes {
		raw = raw[:contentSafetyLogMaxBodyBytes]
	}
	text := sanitizeContentSafetyString(string(raw))
	preview, previewTruncated := truncateContentSafetyStringWithFlag(text, contentSafetyLogMaxPreviewBytes)
	return preview, truncated || previewTruncated
}

func contentSafetySHA256Hex(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func marshalContentSafetyLogRecord(record contentSafetyLogRecord) ([]byte, error) {
	line, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		return nil, errMarshal
	}
	if len(line) <= contentSafetyLogMaxRecordBytes {
		return append(line, '\n'), nil
	}

	compact := record
	compact.Error, _ = truncateContentSafetyStringWithFlag(compact.Error, contentSafetyLogFallbackErrorLen)
	if compact.PayloadPreview != "" {
		compact.PayloadPreview, _ = truncateContentSafetyStringWithFlag(compact.PayloadPreview, contentSafetyLogFallbackPreviewBytes)
		compact.PayloadTruncated = true
	}
	if compact.OriginalRequestPreview != "" {
		compact.OriginalRequestPreview, _ = truncateContentSafetyStringWithFlag(compact.OriginalRequestPreview, contentSafetyLogFallbackPreviewBytes)
		compact.OriginalRequestTruncated = true
	}
	line, errMarshal = json.Marshal(compact)
	if errMarshal != nil {
		return nil, errMarshal
	}
	if len(line) <= contentSafetyLogMaxRecordBytes {
		return append(line, '\n'), nil
	}

	minimal := compact
	minimal.AuthID = ""
	minimal.AuthLabel = ""
	minimal.AuthPrefix = ""
	minimal.Error, _ = truncateContentSafetyStringWithFlag(minimal.Error, 128)
	if minimal.PayloadPreview != "" {
		minimal.PayloadPreview = contentSafetyLogOmittedPreview
		minimal.PayloadTruncated = true
	}
	if minimal.OriginalRequestPreview != "" {
		minimal.OriginalRequestPreview = contentSafetyLogOmittedPreview
		minimal.OriginalRequestTruncated = true
	}
	line, errMarshal = json.Marshal(minimal)
	if errMarshal != nil {
		return nil, errMarshal
	}
	if len(line) <= contentSafetyLogMaxRecordBytes {
		return append(line, '\n'), nil
	}

	fallback := contentSafetyLogRecord{
		Timestamp:              record.Timestamp,
		RequestID:              record.RequestID,
		Provider:               record.Provider,
		AuthIndex:              record.AuthIndex,
		RouteModel:             record.RouteModel,
		RequestedModel:         record.RequestedModel,
		UpstreamModel:          record.UpstreamModel,
		RequestPath:            record.RequestPath,
		StatusCode:             record.StatusCode,
		SafetyCode:             record.SafetyCode,
		SafetyDirection:        record.SafetyDirection,
		Error:                  truncateContentSafetyString(record.Error, 128),
		PayloadBytes:           record.PayloadBytes,
		PayloadSHA256:          record.PayloadSHA256,
		PayloadPreview:         contentSafetyLogOmittedPreview,
		PayloadTruncated:       true,
		OriginalRequestPresent: record.OriginalRequestPresent,
	}
	if record.OriginalRequestPresent {
		fallback.OriginalRequestBytes = record.OriginalRequestBytes
		fallback.OriginalRequestSHA256 = record.OriginalRequestSHA256
		fallback.OriginalRequestPreview = contentSafetyLogOmittedPreview
		fallback.OriginalRequestTruncated = true
	}
	line, errMarshal = json.Marshal(fallback)
	if errMarshal != nil {
		return nil, errMarshal
	}
	if len(line) > contentSafetyLogMaxRecordBytes {
		return nil, fmt.Errorf("content safety log line exceeds %d bytes after compaction", contentSafetyLogMaxRecordBytes)
	}
	return append(line, '\n'), nil
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

func truncateContentSafetyStringWithFlag(value string, maxLen int) (string, bool) {
	if maxLen <= 0 || len(value) <= maxLen {
		return value, false
	}
	return truncateContentSafetyString(value, maxLen), true
}
