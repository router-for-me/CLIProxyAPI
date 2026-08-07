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

// SecretFileMode is the permission used for files holding secrets (OAuth tokens,
// API keys, management keys). Only the owning user may read or write them.
const SecretFileMode os.FileMode = 0o600

// CreateSecretFile truncates or creates path with SecretFileMode. Unlike os.Create
// it never depends on the process umask, and it also tightens the mode of files
// that already exist with wider permissions.
func CreateSecretFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, SecretFileMode)
	if err != nil {
		return nil, err
	}
	// O_CREATE only applies the mode to newly created files; re-apply it so an
	// existing world-readable file gets fixed on the next write.
	if err = f.Chmod(SecretFileMode); err != nil && !os.IsPermission(err) {
		log.Debugf("failed to tighten permissions on %s: %v", path, err)
	}
	return f, nil
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
