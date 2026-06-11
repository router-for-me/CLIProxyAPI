package util

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

const claudeToolIDMaxLength = 64

var (
	claudeToolUseIDSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	claudeToolUseIDSuffix    = regexp.MustCompile(`(?:-|_)[0-9]+(?:-|_)[0-9]+$`)
	claudeToolUseIDCounter   uint64
)

// SanitizeClaudeToolID ensures the given id conforms to Claude's
// tool_use.id regex ^[a-zA-Z0-9_-]+$ and keeps it within the 64-char
// upstream limit. Non-conforming characters are replaced with '_'; an
// empty result gets a generated fallback.
func SanitizeClaudeToolID(id string) string {
	s := claudeToolUseIDSanitizer.ReplaceAllString(id, "_")
	if s == "" {
		s = fmt.Sprintf("toolu_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&claudeToolUseIDCounter, 1))
	}
	if len(s) <= claudeToolIDMaxLength {
		return s
	}
	hash := sha256.Sum256([]byte(s))
	hashSuffix := hex.EncodeToString(hash[:])[:8]
	base := s
	if suffix := claudeToolUseIDSuffix.FindStringIndex(base); suffix != nil {
		base = base[:suffix[0]]
	}
	prefixBudget := claudeToolIDMaxLength - len(hashSuffix) - 1
	if prefixBudget <= 0 {
		return hashSuffix
	}
	if len(base) > prefixBudget {
		base = base[:prefixBudget]
		base = strings.TrimRight(base, "_")
	}
	if base == "" {
		base = "toolu"
	}
	shortID := base + "_" + hashSuffix
	if len(shortID) > claudeToolIDMaxLength {
		shortID = shortID[:claudeToolIDMaxLength]
	}
	return shortID
}
