package misc

import (
	"fmt"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Separator used to visually group related log lines.
var credentialSeparator = strings.Repeat("-", 67)

// LogSavingCredentials emits a consistent log message when persisting auth material.
func LogSavingCredentials(path string) {
	if path == "" {
		return
	}
	// Use filepath.Clean so logs remain stable even if callers pass redundant separators.
	fmt.Printf("Saving credentials to %s\n", filepath.Clean(path))
}

// LogCredentialSeparator adds a visual separator to group auth/key processing logs.
func LogCredentialSeparator() {
	log.Debug(credentialSeparator)
}

// ValidateCredentialPath rejects unsafe credential file paths and returns a cleaned path.
func ValidateCredentialPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("credential path is empty")
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", fmt.Errorf("credential path contains NUL byte")
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." {
		return "", fmt.Errorf("credential path is invalid")
	}
	if cleaned != trimmed {
		return "", fmt.Errorf("credential path must be clean and traversal-free")
	}
	return cleaned, nil
}
