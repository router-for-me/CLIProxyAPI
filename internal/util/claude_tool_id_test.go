package util

import (
	"strings"
	"testing"
)

func TestSanitizeClaudeToolID_ShortensLongIDsToClaudeLimit(t *testing.T) {
	longID := "mcp__synthpilot__connect_hardware_server-1776264133559318385-226896"
	if len(longID) <= 64 {
		t.Fatalf("test setup error: longID len = %d, want > 64", len(longID))
	}

	sanitized := SanitizeClaudeToolID(longID)

	if sanitized == longID {
		t.Fatalf("SanitizeClaudeToolID left long id unchanged: %q", sanitized)
	}
	if len(sanitized) > 64 {
		t.Fatalf("SanitizeClaudeToolID returned len %d, want <= 64: %q", len(sanitized), sanitized)
	}
	if !strings.HasPrefix(sanitized, "mcp__synthpilot__connect_hardware_server_") {
		t.Fatalf("SanitizeClaudeToolID should keep readable prefix, got %q", sanitized)
	}
}
