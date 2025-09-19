package misc

import (
	"path/filepath"

	log "github.com/sirupsen/logrus"
)

// LogSavingCredentials emits a consistent log message when persisting auth material.
func LogSavingCredentials(path string) {
	if path == "" {
		return
	}
	// Use filepath.Clean so logs remain stable even if callers pass redundant separators.
	log.Infof("Saving credentials to %s", filepath.Clean(path))
}
