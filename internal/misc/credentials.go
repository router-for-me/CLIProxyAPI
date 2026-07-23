package misc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// WriteCredentialFileAtomic writes credential data through a private temporary
// file in the destination directory and atomically replaces the destination.
func WriteCredentialFileAtomic(path string, data []byte) error {
	return writeCredentialFileAtomicWithRename(path, data, os.Rename)
}

func writeCredentialFileAtomicWithRename(path string, data []byte, rename func(string, string) error) error {
	if rename == nil {
		return fmt.Errorf("replace credential file: rename function is nil")
	}

	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create credential temp file: %w", err)
	}
	tempPath := temp.Name()
	closed := false
	removeTemp := true
	defer func() {
		if !closed {
			_ = temp.Close()
		}
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if err = temp.Chmod(0o600); err != nil {
		return fmt.Errorf("set credential temp file permissions: %w", err)
	}
	if _, err = temp.Write(data); err != nil {
		return fmt.Errorf("write credential temp file: %w", err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("sync credential temp file: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close credential temp file: %w", err)
	}
	closed = true

	if info, errStat := os.Lstat(path); errStat == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("credential path is not a regular file")
		}
		// This also clears Windows' read-only attribute so MoveFileEx can replace
		// the destination without removing it first.
		if err = os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("tighten existing credential file permissions: %w", err)
		}
	} else if !os.IsNotExist(errStat) {
		return fmt.Errorf("inspect existing credential file: %w", errStat)
	}

	if err = rename(tempPath, path); err != nil {
		return fmt.Errorf("replace credential file: %w", err)
	}
	removeTemp = false

	if err = syncCredentialDirectory(dir); err != nil {
		return fmt.Errorf("sync credential directory: %w", err)
	}
	return nil
}

// TightenCredentialFilePermissions restricts an existing regular credential
// file to mode 0600. On Windows, os.Chmod only controls the read-only attribute.
func TightenCredentialFilePermissions(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("credential path is not a regular file")
	}
	return os.Chmod(path, 0o600)
}

func syncCredentialDirectory(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err = directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
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
