// Package bundledplugins copies plugin libraries shipped with the binary into
// the configured plugins directory, so they load without manual installation.
package bundledplugins

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SourceDirEnv overrides the bundled plugin directory; the default is
// bundled-plugins next to the executable.
const SourceDirEnv = "CLIPROXY_BUNDLED_PLUGINS_DIR"

// markerSuffix names the sidecar recording the hash of the last installed copy.
const markerSuffix = ".bundled"

var libraryExtensions = map[string]bool{".so": true, ".dll": true, ".dylib": true}

// SourceDir returns the bundled plugin directory.
func SourceDir() string {
	if dir := strings.TrimSpace(os.Getenv(SourceDirEnv)); dir != "" {
		return dir
	}
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(executable), "bundled-plugins")
}

// Install copies sourceDir/<GOOS>/<GOARCH>/<library> to pluginsDir with the
// same layout. A target is replaced only when it still has the content this
// installer wrote last time, so a library the user changed is left alone.
// It returns the installed or updated target paths.
func Install(sourceDir, pluginsDir string) ([]string, error) {
	if sourceDir == "" || pluginsDir == "" {
		return nil, nil
	}
	platform := filepath.Join(runtime.GOOS, runtime.GOARCH)
	entries, err := os.ReadDir(filepath.Join(sourceDir, platform))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read bundled plugins: %w", err)
	}
	var installed []string
	for _, entry := range entries {
		if entry.IsDir() || !libraryExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			continue
		}
		source := filepath.Join(sourceDir, platform, entry.Name())
		target := filepath.Join(pluginsDir, platform, entry.Name())
		changed, errInstall := installLibrary(source, target)
		if errInstall != nil {
			return installed, errInstall
		}
		if changed {
			installed = append(installed, target)
		}
	}
	return installed, nil
}

func installLibrary(source, target string) (bool, error) {
	sourceHash, err := fileHash(source)
	if err != nil {
		return false, err
	}
	targetHash, err := fileHash(target)
	switch {
	case err == nil && targetHash == sourceHash:
		return false, writeMarker(target, sourceHash)
	case err == nil && readMarker(target) != targetHash:
		return false, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return false, err
	}
	if err := copyAtomic(source, target); err != nil {
		return false, err
	}
	return true, writeMarker(target, sourceHash)
}

// copyAtomic renames a complete temporary copy over the target, so a library
// already mapped by the running process is never rewritten in place.
func copyAtomic(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create plugin directory: %w", err)
	}
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open bundled plugin: %w", err)
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), ".bundled-*")
	if err != nil {
		return fmt.Errorf("create plugin copy: %w", err)
	}
	_, errCopy := io.Copy(tmp, in)
	errClose := tmp.Close()
	if errCopy == nil && errClose == nil {
		errCopy = os.Chmod(tmp.Name(), 0o755)
	}
	if errCopy == nil && errClose == nil {
		errCopy = os.Rename(tmp.Name(), target)
	}
	if errCopy != nil || errClose != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("install bundled plugin %s: %w", filepath.Base(target), errors.Join(errCopy, errClose))
	}
	return nil
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readMarker(target string) string {
	raw, err := os.ReadFile(target + markerSuffix)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func writeMarker(target, hash string) error {
	if readMarker(target) == hash {
		return nil
	}
	return os.WriteFile(target+markerSuffix, []byte(hash+"\n"), 0o644)
}
