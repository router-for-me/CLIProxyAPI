package misc

import (
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

var credentialSeparator = strings.Repeat("-", 70)

// LogSavingCredentials emits a consistent log message when persisting auth material.
func LogSavingCredentials(path string) {
	if path == "" {
		return
	}
	// Use filepath.Clean so logs remain stable even if callers pass redundant separators.
	log.Infof("Saving credentials to %s", filepath.Clean(path))
}

// LogCredentialSeparator adds a visual separator to group auth/key processing logs.
func LogCredentialSeparator() {
	log.Info(credentialSeparator)
}
