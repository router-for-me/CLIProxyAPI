//go:build windows || (cgo && (linux || darwin || freebsd))

package pluginhost

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Plugin dynamic libraries are loaded from content-addressed shadow copies.
// A loaded Go plugin image must stay mapped for the lifetime of the host
// process (its runtime cannot be unloaded), so loading from a shadow copy
// keeps the original artifact replaceable and gives every distinct plugin
// build its own path, which dynamic loaders map as a fresh image.

const (
	shadowPluginPrefix           = "cliproxy-plugin-"
	shadowPluginTempPrefix       = ".cliproxy-plugin-"
	shadowPluginProcessDirPrefix = "pid-"
	shadowPluginDigestLength     = 32
)

var shadowPluginCleanupOnce sync.Once

func shadowCopyPlugin(file pluginFile) (string, error) {
	dir, errDir := shadowPluginDir()
	if errDir != nil {
		return "", errDir
	}
	shadowPluginCleanupOnce.Do(func() {
		removeStaleShadowPlugins(dir)
		removeStaleShadowPluginDirs(filepath.Dir(dir), os.Getpid())
	})
	return shadowCopyPluginToDir(file, dir)
}

// removeStaleShadowPluginDirs removes shadow directories left behind by
// host processes that already exited. Directories whose owner process is
// still alive are kept, because that process may reuse the resident plugin
// images inside them.
func removeStaleShadowPluginDirs(baseDir string, currentPID int) {
	entries, errRead := os.ReadDir(baseDir)
	if errRead != nil {
		return
	}
	for _, entry := range entries {
		if entry == nil || !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, shadowPluginProcessDirPrefix) {
			continue
		}
		pid, errParse := strconv.Atoi(strings.TrimPrefix(name, shadowPluginProcessDirPrefix))
		if errParse != nil || pid <= 0 || pid == currentPID {
			continue
		}
		if shadowPluginProcessAlive(pid) {
			continue
		}
		removeShadowPluginDir(filepath.Join(baseDir, name))
	}
}

func shadowCopyPluginToDir(file pluginFile, dir string) (string, error) {
	source := filepath.Clean(file.Path)
	tmp, errTemp := os.CreateTemp(dir, shadowPluginTempPrefix+file.ID+"-*"+filepath.Ext(source))
	if errTemp != nil {
		return "", errTemp
	}
	tmpName := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			removeShadowPlugin(tmpName)
		}
	}()

	in, errOpen := os.Open(source)
	if errOpen != nil {
		_ = tmp.Close()
		return "", errOpen
	}
	defer func() {
		_ = in.Close()
	}()
	hasher := sha256.New()
	size, errCopy := io.Copy(io.MultiWriter(tmp, hasher), in)
	if errCopy != nil {
		_ = tmp.Close()
		return "", errCopy
	}
	if errClose := tmp.Close(); errClose != nil {
		return "", errClose
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	target := shadowPluginPath(dir, file.ID, digest, filepath.Ext(source))
	if shadowPluginMatches(target, size, digest) {
		return target, nil
	}
	if errRemove := os.Remove(target); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		if shadowPluginMatches(target, size, digest) {
			return target, nil
		}
		removeShadowPlugin(target)
		return "", fmt.Errorf("remove stale shadow plugin: %w", errRemove)
	}
	if errRename := os.Rename(tmpName, target); errRename != nil {
		if shadowPluginMatches(target, size, digest) {
			return target, nil
		}
		return "", fmt.Errorf("move shadow plugin: %w", errRename)
	}
	removeTemp = false
	return target, nil
}

func shadowPluginDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "cliproxy-pluginhost", shadowPluginProcessDirName(os.Getpid()))
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return "", errMkdir
	}
	return dir, nil
}

func shadowPluginProcessDirName(pid int) string {
	return fmt.Sprintf("%s%d", shadowPluginProcessDirPrefix, pid)
}

func removeStaleShadowPlugins(dir string) {
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		return
	}
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, shadowPluginPrefix) || strings.HasPrefix(name, shadowPluginTempPrefix) {
			removeShadowPlugin(filepath.Join(dir, name))
		}
	}
}

func shadowPluginPath(dir string, id string, digest string, extension string) string {
	if len(digest) > shadowPluginDigestLength {
		digest = digest[:shadowPluginDigestLength]
	}
	return filepath.Join(dir, shadowPluginPrefix+id+"-"+digest+extension)
}

func shadowPluginMatches(path string, size int64, digest string) bool {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return false
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return false
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return false
	}
	defer func() {
		_ = file.Close()
	}()
	hasher := sha256.New()
	if _, errCopy := io.Copy(hasher, file); errCopy != nil {
		return false
	}
	return hex.EncodeToString(hasher.Sum(nil)) == digest
}
