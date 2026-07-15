package misc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

type credentialFileOps struct {
	createTemp func(string, string) (*os.File, error)
	rename     func(string, string) error
}

// WriteCredentialFileAtomic writes JSON credential data to a private temporary
// file and replaces the target only after the temporary file is synced and closed.
func WriteCredentialFileAtomic(path string, data any) error {
	return writeCredentialFileAtomic(path, data, credentialFileOps{
		createTemp: os.CreateTemp,
		rename:     os.Rename,
	})
}

func writeCredentialFileAtomic(path string, data any, ops credentialFileOps) (returnErr error) {
	temp, errCreate := ops.createTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if errCreate != nil {
		return fmt.Errorf("failed to create temporary credential file: %w", errCreate)
	}
	tempPath := temp.Name()
	closed := false
	removeTemp := true
	defer func() {
		if !closed {
			if errClose := temp.Close(); errClose != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("failed to close temporary credential file: %w", errClose))
			}
		}
		if removeTemp {
			if errRemove := os.Remove(tempPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("failed to remove temporary credential file: %w", errRemove))
			}
		}
	}()

	if errChmod := temp.Chmod(0600); errChmod != nil {
		return fmt.Errorf("failed to restrict temporary credential file permissions: %w", errChmod)
	}
	if errEncode := json.NewEncoder(temp).Encode(data); errEncode != nil {
		return fmt.Errorf("failed to encode temporary credential file: %w", errEncode)
	}
	if errSync := temp.Sync(); errSync != nil {
		return fmt.Errorf("failed to sync temporary credential file: %w", errSync)
	}
	errClose := temp.Close()
	closed = true
	if errClose != nil {
		return fmt.Errorf("failed to close temporary credential file: %w", errClose)
	}
	if errRename := ops.rename(tempPath, path); errRename != nil {
		return fmt.Errorf("failed to replace credential file: %w", errRename)
	}
	removeTemp = false
	return nil
}
