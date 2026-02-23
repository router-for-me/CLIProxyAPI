package iflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// ExtractBXAuth extracts the BXAuth value from a cookie string.
func ExtractBXAuth(cookie string) string {
	parts := strings.Split(cookie, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "BXAuth=") {
			return strings.TrimPrefix(part, "BXAuth=")
		}
	}
	return ""
}

// CheckDuplicateBXAuth checks if the given BXAuth value already exists in any iflow auth file.
// Returns the path of the existing file if found, empty string otherwise.
func CheckDuplicateBXAuth(authDir, bxAuth string) (string, error) {
	if bxAuth == "" {
		return "", nil
	}

	entries, err := os.ReadDir(authDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read auth dir failed: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "iflow-") || !strings.HasSuffix(name, ".json") {
			continue
		}

		filePath := filepath.Join(authDir, name)
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var tokenData struct {
			Cookie string `json:"cookie"`
		}
		if err := json.Unmarshal(data, &tokenData); err != nil {
			continue
		}

		existingBXAuth := ExtractBXAuth(tokenData.Cookie)
		if existingBXAuth != "" && existingBXAuth == bxAuth {
			return filePath, nil
		}
	}

	return "", nil
}
