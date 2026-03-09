package util

import (
	"encoding/base64"
	"fmt"
	"hash/crc32"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

var (
	claudeToolUseIDSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	claudeToolUseIDCounter   uint64
	claudeToolUseIDPattern   = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

const claudeToolIDEncodingPrefix = "toolu_enc0_"

// SanitizeClaudeToolID ensures the given id conforms to Claude's
// tool_use.id regex ^[a-zA-Z0-9_-]+$. Non-conforming characters are
// replaced with '_'; an empty result gets a generated fallback.
func SanitizeClaudeToolID(id string) string {
	s := claudeToolUseIDSanitizer.ReplaceAllString(id, "_")
	if s == "" {
		s = fmt.Sprintf("toolu_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&claudeToolUseIDCounter, 1))
	}
	return s
}

// EncodeClaudeToolID preserves the original upstream id while converting it
// into a Claude-compatible tool_use.id. Valid ids are kept as-is unless they
// already look like an encoded payload, in which case they are re-encoded to
// keep DecodeClaudeToolID unambiguous.
func EncodeClaudeToolID(id string) string {
	if id == "" {
		return SanitizeClaudeToolID(id)
	}
	if claudeToolUseIDPattern.MatchString(id) && !looksEncodedClaudeToolID(id) {
		return id
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(id))
	checksum := fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(id)))
	return claudeToolIDEncodingPrefix + encoded + "_" + checksum
}

// DecodeClaudeToolID restores the original upstream id from a Claude-safe
// tool_use.id produced by EncodeClaudeToolID. Non-encoded ids are returned
// unchanged.
func DecodeClaudeToolID(id string) string {
	decoded, ok := decodeClaudeToolID(id)
	if !ok {
		return id
	}
	return decoded
}

func looksEncodedClaudeToolID(id string) bool {
	_, ok := decodeClaudeToolID(id)
	return ok
}

func decodeClaudeToolID(id string) (string, bool) {
	if !strings.HasPrefix(id, claudeToolIDEncodingPrefix) {
		return "", false
	}
	payload := strings.TrimPrefix(id, claudeToolIDEncodingPrefix)
	lastSep := strings.LastIndex(payload, "_")
	if lastSep <= 0 || lastSep == len(payload)-1 {
		return "", false
	}
	encodedPart := payload[:lastSep]
	checksumPart := payload[lastSep+1:]
	if len(checksumPart) != 8 {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encodedPart)
	if err != nil || len(decoded) == 0 {
		return "", false
	}
	if want := fmt.Sprintf("%08x", crc32.ChecksumIEEE(decoded)); !strings.EqualFold(checksumPart, want) {
		return "", false
	}
	return string(decoded), true
}
