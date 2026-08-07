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

// OpenFileForSecureRewrite opens a secret file, tightens its permissions, and then truncates it.
func OpenFileForSecureRewrite(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open file for secure rewrite: %w", err)
	}
	if errChmod := file.Chmod(0o600); errChmod != nil {
		_ = file.Close()
		return nil, fmt.Errorf("set secure file permissions: %w", errChmod)
	}
	if errTruncate := file.Truncate(0); errTruncate != nil {
		_ = file.Close()
		return nil, fmt.Errorf("truncate secure file: %w", errTruncate)
	}
	return file, nil
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
