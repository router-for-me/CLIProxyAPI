package misc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Separator used to visually group related log lines.
var credentialSeparator = strings.Repeat("-", 67)

// OpenFileForSecureRewrite opens a secret file, tightens its permissions when possible, and then truncates it.
func OpenFileForSecureRewrite(path string) (*os.File, error) {
	return openFileForSecureRewrite(path, os.O_WRONLY|os.O_CREATE)
}

// OpenExistingFileForSecureRewrite securely rewrites a secret file only when it already exists.
func OpenExistingFileForSecureRewrite(path string) (*os.File, error) {
	return openFileForSecureRewrite(path, os.O_WRONLY)
}

func openFileForSecureRewrite(path string, flags int) (*os.File, error) {
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open file for secure rewrite: %w", err)
	}
	ensureOpenFileMode0600(file)
	if errTruncate := file.Truncate(0); errTruncate != nil {
		_ = file.Close()
		return nil, fmt.Errorf("truncate secure file: %w", errTruncate)
	}
	return file, nil
}

// EnsureFileMode0600 tightens an existing secret file's permissions when the filesystem supports it.
func EnsureFileMode0600(path string) {
	pathInfo, errLstat := os.Lstat(path)
	if errLstat != nil {
		log.WithError(errLstat).Warn("could not inspect secret file for permission hardening")
		return
	}
	if !pathInfo.Mode().IsRegular() {
		log.Warn("could not restrict permissions of a non-regular secret file")
		return
	}
	if pathInfo.Mode().Perm() == 0o600 {
		return
	}

	file, errOpen := os.OpenFile(path, os.O_RDWR, 0)
	if errOpen != nil {
		log.WithError(errOpen).Warn("could not open secret file for permission hardening")
		return
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Warn("could not close secret file after permission hardening")
		}
	}()

	fileInfo, errStat := file.Stat()
	if errStat != nil {
		log.WithError(errStat).Warn("could not inspect open secret file for permission hardening")
		return
	}
	if !os.SameFile(pathInfo, fileInfo) {
		log.Warn("secret file changed during permission hardening")
		return
	}
	ensureOpenFileMode0600(file)
}

func ensureOpenFileMode0600(file *os.File) {
	info, errStat := file.Stat()
	if errStat == nil && info.Mode().Perm() == 0o600 {
		return
	}
	if errChmod := file.Chmod(0o600); errChmod != nil {
		log.WithError(errChmod).Warn("could not restrict secret file permissions to 0600")
	}
}

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

// MergeMetadata serializes the source struct into a map and merges the provided metadata into it.
func MergeMetadata(source any, metadata map[string]any) (map[string]any, error) {
	var data map[string]any

	// Fast path: if source is already a map, just copy it to avoid mutation of original
	if srcMap, ok := source.(map[string]any); ok {
		data = make(map[string]any, len(srcMap)+len(metadata))
		for k, v := range srcMap {
			data[k] = v
		}
	} else {
		// Slow path: marshal to JSON and back to map to respect JSON tags
		temp, err := json.Marshal(source)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal source: %w", err)
		}
		if err := json.Unmarshal(temp, &data); err != nil {
			return nil, fmt.Errorf("failed to unmarshal to map: %w", err)
		}
	}

	// Merge extra metadata
	if metadata != nil {
		if data == nil {
			data = make(map[string]any)
		}
		for k, v := range metadata {
			data[k] = v
		}
	}

	return data, nil
}
