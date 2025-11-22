package iflow

import (
	"fmt"
	"strings"
)

// NormalizeCookie normalizes raw cookie strings for iFlow authentication flows.
func NormalizeCookie(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("cookie cannot be empty")
	}

	combined := strings.Join(strings.Fields(trimmed), " ")
	if !strings.HasSuffix(combined, ";") {
		combined += ";"
	}
	if !strings.Contains(combined, "BXAuth=") {
		return "", fmt.Errorf("cookie missing BXAuth field")
	}
	return combined, nil
}

// SanitizeIFlowFileName normalizes user identifiers for safe filename usage.
func SanitizeIFlowFileName(raw string) string {
	if raw == "" {
		return ""
	}
	cleanEmail := strings.ReplaceAll(raw, "*", "x")
	var result strings.Builder
	for _, r := range cleanEmail {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '@' || r == '.' || r == '-' {
			result.WriteRune(r)
		}
	}
	return strings.TrimSpace(result.String())
}
