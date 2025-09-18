package util

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// CookieSnapshotPath derives the cookie snapshot file path from the main token JSON path.
// It replaces the .json suffix with .cookies, or appends .cookies if missing.
func CookieSnapshotPath(mainPath string) string {
	if strings.HasSuffix(mainPath, ".json") {
		return strings.TrimSuffix(mainPath, ".json") + ".cookies"
	}
	return mainPath + ".cookies"
}

// IsRegularFile reports whether the given path exists and is a regular file.
func IsRegularFile(path string) bool {
	if path == "" {
		return false
	}
	if st, err := os.Stat(path); err == nil && !st.IsDir() {
		return true
	}
	return false
}

// ReadJSON reads and unmarshals a JSON file into v.
// Returns os.ErrNotExist if the file does not exist.
func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.ErrNotExist
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// WriteJSON marshals v as JSON and writes to path, creating parent directories as needed.
func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	return enc.Encode(v)
}

// RemoveFile removes the file if it exists.
func RemoveFile(path string) error {
	if IsRegularFile(path) {
		return os.Remove(path)
	}
	return nil
}

// TryReadCookieSnapshotInto tries to read a cookie snapshot into v.
// It attempts the .cookies suffix; returns (true, nil) when found and decoded,
// or (false, nil) when none exists.
func TryReadCookieSnapshotInto(mainPath string, v any) (bool, error) {
	snap := CookieSnapshotPath(mainPath)
	if err := ReadJSON(snap, v); err != nil {
		if err == os.ErrNotExist {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// WriteCookieSnapshot writes v to the snapshot path derived from mainPath using the .cookies suffix.
func WriteCookieSnapshot(mainPath string, v any) error {
	return WriteJSON(CookieSnapshotPath(mainPath), v)
}

// RemoveCookieSnapshots removes both modern and legacy snapshot files.
func RemoveCookieSnapshots(mainPath string) { _ = RemoveFile(CookieSnapshotPath(mainPath)) }
