package misc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MoeForge/moego/json"
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

// MergeMetadata serializes the source struct into a map and merges the provided metadata into it.
func MergeMetadata(source any, metadata map[string]any) (map[string]any, error) {
	var data map[string]any

	// Fast path: if source is already a map, just copy it to avoid mutation of original
	if srcMap, ok := source.(map[string]any); ok {
		data = make(map[string]any, len(srcMap)+len(metadata))
		for k, v := range srcMap {
			data[k] = v
		}
	} else if source != nil {
		// Slow path: marshal to JSON and back to map to respect JSON tags
		temp, errMarshal := json.Marshal(source)
		if errMarshal != nil {
			return nil, fmt.Errorf("failed to marshal source: %w", errMarshal)
		}
		if errUnmarshal := json.Unmarshal(temp, &data); errUnmarshal != nil {
			return nil, fmt.Errorf("failed to unmarshal to map: %w", errUnmarshal)
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

var syncAtomicTempFile = func(file *os.File) error {
	return file.Sync()
}

// WriteFileAtomic writes data to path atomically by writing to a temporary file
// in the same directory, syncing it to disk, closing it, and renaming it over
// the target path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("file path is empty")
	}
	if perm == 0 {
		perm = 0o600
	}

	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("failed to create directory: %w", errMkdir)
	}

	tmpFile, errCreate := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if errCreate != nil {
		return fmt.Errorf("failed to create temp file: %w", errCreate)
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			if errRemove := os.Remove(tmpPath); errRemove != nil && !os.IsNotExist(errRemove) {
				log.Errorf("atomic file write: remove temp file error: %v", errRemove)
			}
		}
	}()

	if errChmod := tmpFile.Chmod(perm); errChmod != nil {
		if errClose := tmpFile.Close(); errClose != nil {
			log.Errorf("atomic file write: close temp file after chmod error: %v", errClose)
		}
		return fmt.Errorf("failed to chmod temp file: %w", errChmod)
	}

	if _, errWrite := tmpFile.Write(data); errWrite != nil {
		if errClose := tmpFile.Close(); errClose != nil {
			log.Errorf("atomic file write: close temp file after write error: %v", errClose)
		}
		return fmt.Errorf("failed to write temp file: %w", errWrite)
	}

	if errSync := syncAtomicTempFile(tmpFile); errSync != nil {
		if errClose := tmpFile.Close(); errClose != nil {
			log.Errorf("atomic file write: close temp file after sync error: %v", errClose)
		}
		return fmt.Errorf("failed to sync temp file: %w", errSync)
	}

	if errClose := tmpFile.Close(); errClose != nil {
		return fmt.Errorf("failed to close temp file: %w", errClose)
	}

	if errRename := os.Rename(tmpPath, path); errRename != nil {
		return fmt.Errorf("failed to rename temp file: %w", errRename)
	}

	cleanup = false
	return nil
}
