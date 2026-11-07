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
	"runtime"
	"strconv"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
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
	shadowPluginCacheDirName     = "cache"
	shadowPluginLocalDirName     = ".shadow"
	shadowPluginDigestLength     = 32
)

type cachedPluginDigest struct {
	size        int64
	modTimeNano int64
	digest      string
}

var (
	shadowPluginCleanupOnce   sync.Once
	cleanedShadowTempDirs     sync.Map
	pluginDigestCache         sync.Map
	pruneShadowBeforeLockHook func(shadowFileName string)
)

func shadowCopyPlugin(file pluginFile) (string, error) {
	path, release, errShadow := shadowCopyPluginInternal(file, false)
	if release != nil {
		release()
	}
	return path, errShadow
}

func acquireShadowPluginLoad(file pluginFile) (string, func(), error) {
	return shadowCopyPluginInternal(file, true)
}

func acquireShadowPluginFallbackLoad(file pluginFile, failedLoadPath string) (string, func(), error) {
	source := filepath.Clean(file.Path)
	dir, errDir := shadowPluginFallbackDir(source)
	if errDir != nil {
		return "", nil, errDir
	}
	if failedLoadPath != "" && filepath.Clean(filepath.Dir(failedLoadPath)) == filepath.Clean(dir) {
		return "", nil, fmt.Errorf("shadow plugin already in fallback directory")
	}
	size, digest, errHash := cachedOrHashPluginFile(source)
	if errHash != nil {
		return "", nil, errHash
	}
	cleanupShadowTempFilesOnce(dir)
	return shadowCopyPluginToDirWithGuard(file, source, dir, size, digest, true)
}

func shadowCopyPluginInternal(file pluginFile, holdGuard bool) (string, func(), error) {
	shadowPluginCleanupOnce.Do(func() {
		removeStaleShadowPluginDirs(shadowPluginBaseDir(), os.Getpid())
	})
	source := filepath.Clean(file.Path)
	size, digest, errHash := hashPluginFile(source)
	if errHash != nil {
		return "", nil, errHash
	}
	if localDir, errLocalDir := shadowPluginLocalDir(source); errLocalDir == nil {
		cleanupShadowTempFilesOnce(localDir)
		if target, release, errCopy := shadowCopyPluginToDirWithGuard(file, source, localDir, size, digest, holdGuard); errCopy == nil {
			return target, release, nil
		}
	}
	dir, errDir := shadowPluginFallbackDir(source)
	if errDir != nil {
		return "", nil, errDir
	}
	cleanupShadowTempFilesOnce(dir)
	return shadowCopyPluginToDirWithGuard(file, source, dir, size, digest, holdGuard)
}

func cleanupShadowTempFilesOnce(dir string) {
	cleaned := filepath.Clean(dir)
	if _, loaded := cleanedShadowTempDirs.LoadOrStore(cleaned, struct{}{}); loaded {
		return
	}
	removeStaleShadowTempFiles(cleaned)
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
	size, digest, errHash := hashPluginFile(source)
	if errHash != nil {
		return "", errHash
	}
	return shadowCopyPluginToDirWithDigest(file, dir, size, digest)
}

func shadowCopyPluginToDirWithGuard(file pluginFile, sourcePath string, dir string, size int64, digest string, holdGuard bool) (string, func(), error) {
	releaseGuard := func() {}
	if holdGuard {
		sourceDir := filepath.Dir(filepath.Clean(sourcePath))
		unlockCoord, errLock := acquireShadowCoordLock(sourceDir, dir, false, false)
		if errLock != nil {
			return "", nil, errLock
		}
		releaseGuard = unlockCoord
	}
	target, errCopy := shadowCopyPluginToDirWithDigest(file, dir, size, digest)
	if errCopy != nil {
		releaseGuard()
		return "", nil, errCopy
	}
	return target, releaseGuard, nil
}

func acquireShadowCoordLock(sourceDir string, fallbackDir string, exclusive bool, nonBlocking bool) (func(), error) {
	baseDir := fallbackDir
	if strings.TrimSpace(sourceDir) != "" {
		baseDir = filepath.Join(shadowPluginBaseDir(), shadowPluginCacheDirName, shadowDirectoryDigest(sourceDir))
	}
	if errMkdir := os.MkdirAll(baseDir, 0o700); errMkdir != nil {
		return nil, errMkdir
	}
	lockPath := filepath.Join(baseDir, ".cliproxy-coord.lock")
	lockFile, errOpen := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if errOpen != nil {
		return nil, errOpen
	}
	if errLock := lockShadowCoordFile(lockFile, exclusive, nonBlocking); errLock != nil {
		if errClose := lockFile.Close(); errClose != nil {
			log.WithError(errClose).Debug("pluginhost: coord lock file close failed")
		}
		return nil, errLock
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			unlockShadowCoordFile(lockFile)
			if errClose := lockFile.Close(); errClose != nil {
				log.WithError(errClose).Debug("pluginhost: coord lock file close failed")
			}
		})
	}, nil
}

func shadowCopyPluginToDirWithDigest(file pluginFile, dir string, size int64, digest string) (string, error) {
	source := filepath.Clean(file.Path)
	ext := filepath.Ext(source)
	target := shadowPluginPath(dir, file.ID, digest, ext)
	if shadowPluginMatches(target, size, digest) {
		return target, nil
	}

	tmp, errTemp := os.CreateTemp(dir, shadowPluginTempPattern(os.Getpid(), file.ID, ext))
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
		if errClose := tmp.Close(); errClose != nil {
			log.WithError(errClose).Debug("pluginhost: failed to close temp shadow plugin file")
		}
		return "", errOpen
	}
	defer func() {
		if errClose := in.Close(); errClose != nil {
			log.WithError(errClose).Debug("pluginhost: failed to close plugin source file")
		}
	}()
	hasher := sha256.New()
	copiedSize, errCopy := io.Copy(io.MultiWriter(tmp, hasher), in)
	if errCopy != nil {
		if errClose := tmp.Close(); errClose != nil {
			log.WithError(errClose).Debug("pluginhost: failed to close temp shadow plugin file")
		}
		return "", errCopy
	}
	if errClose := tmp.Close(); errClose != nil {
		return "", errClose
	}
	copiedDigest := hex.EncodeToString(hasher.Sum(nil))
	target = shadowPluginPath(dir, file.ID, copiedDigest, ext)
	if errPublish := publishShadowTempFile(tmpName, target, copiedSize, copiedDigest); errPublish != nil {
		return "", errPublish
	}
	removeTemp = false
	return target, nil
}

func publishShadowTempFile(tmpName string, target string, size int64, digest string) error {
	if errPublish := publishShadowPlugin(tmpName, target); errPublish == nil {
		return nil
	}
	if shadowPluginMatches(target, size, digest) {
		removeShadowPlugin(tmpName)
		return nil
	}
	if errRename := os.Rename(tmpName, target); errRename == nil {
		return nil
	}
	if shadowPluginMatches(target, size, digest) {
		removeShadowPlugin(tmpName)
		return nil
	}
	if errRemove := os.Remove(target); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		if shadowPluginMatches(target, size, digest) {
			removeShadowPlugin(tmpName)
			return nil
		}
		removeShadowPlugin(target)
		return fmt.Errorf("remove stale shadow plugin: %w", errRemove)
	}
	if errRename := os.Rename(tmpName, target); errRename != nil {
		if shadowPluginMatches(target, size, digest) {
			removeShadowPlugin(tmpName)
			return nil
		}
		return fmt.Errorf("move shadow plugin: %w", errRename)
	}
	return nil
}

func hashPluginFile(path string) (int64, string, error) {
	cleaned := filepath.Clean(path)
	file, errOpen := os.Open(cleaned)
	if errOpen != nil {
		return 0, "", errOpen
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Debug("pluginhost: failed to close plugin file after hashing")
		}
	}()
	hasher := sha256.New()
	size, errCopy := io.Copy(hasher, file)
	if errCopy != nil {
		return 0, "", errCopy
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if info, errStat := file.Stat(); errStat == nil && info.Mode().IsRegular() {
		pluginDigestCache.Store(cleaned, cachedPluginDigest{
			size:        size,
			modTimeNano: info.ModTime().UnixNano(),
			digest:      digest,
		})
	}
	return size, digest, nil
}

func cachedOrHashPluginFile(path string) (int64, string, error) {
	cleaned := filepath.Clean(path)
	if info, errStat := os.Stat(cleaned); errStat == nil && info.Mode().IsRegular() {
		if raw, okCache := pluginDigestCache.Load(cleaned); okCache {
			if cached, okType := raw.(cachedPluginDigest); okType &&
				cached.size == info.Size() &&
				cached.modTimeNano == info.ModTime().UnixNano() &&
				cached.digest != "" {
				return cached.size, cached.digest, nil
			}
		}
	}
	return hashPluginFile(cleaned)
}

func shadowPluginBaseDir() string {
	return filepath.Join(os.TempDir(), "cliproxy-pluginhost")
}

func shadowPluginLocalDir(sourcePath string) (string, error) {
	dir := filepath.Join(filepath.Dir(filepath.Clean(sourcePath)), shadowPluginLocalDirName)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return "", errMkdir
	}
	return dir, nil
}

func shadowPluginDir() (string, error) {
	dir := filepath.Join(shadowPluginBaseDir(), shadowPluginCacheDirName)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return "", errMkdir
	}
	return dir, nil
}

func shadowPluginFallbackDir(sourcePath string) (string, error) {
	baseDir, errDir := shadowPluginDir()
	if errDir != nil {
		return "", errDir
	}
	sourceDir := filepath.Dir(filepath.Clean(sourcePath))
	dir := filepath.Join(baseDir, shadowDirectoryDigest(sourceDir))
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return "", errMkdir
	}
	return dir, nil
}

func shadowDirectoryDigest(dir string) string {
	cleaned := filepath.Clean(dir)
	if absDir, errAbs := filepath.Abs(cleaned); errAbs == nil {
		cleaned = filepath.Clean(absDir)
	}
	sum := sha256.Sum256([]byte(cleaned))
	return hex.EncodeToString(sum[:])[:shadowPluginDigestLength]
}

func shadowPluginProcessDirName(pid int) string {
	return fmt.Sprintf("%s%d", shadowPluginProcessDirPrefix, pid)
}

func shadowPluginTempPattern(pid int, id string, extension string) string {
	return fmt.Sprintf("%s%s%d-%s-*%s", shadowPluginTempPrefix, shadowPluginProcessDirPrefix, pid, id, extension)
}

func shadowTempFileOwnedByLiveProcess(name string) bool {
	if !strings.HasPrefix(name, shadowPluginTempPrefix) {
		return false
	}
	rest := strings.TrimPrefix(name, shadowPluginTempPrefix)
	if !strings.HasPrefix(rest, shadowPluginProcessDirPrefix) {
		return false
	}
	rest = strings.TrimPrefix(rest, shadowPluginProcessDirPrefix)
	sep := strings.IndexByte(rest, '-')
	if sep <= 0 {
		return false
	}
	pid, errParse := strconv.Atoi(rest[:sep])
	if errParse != nil || pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	return shadowPluginProcessAlive(pid)
}

func removeStaleShadowTempFiles(dir string) {
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		return
	}
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, shadowPluginTempPrefix) && !shadowTempFileOwnedByLiveProcess(name) {
			removeShadowPlugin(filepath.Join(dir, name))
		}
	}
}

func shadowDirHasLiveProcessActivity(entries []os.DirEntry) bool {
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, shadowPluginTempPrefix) && shadowTempFileOwnedByLiveProcess(name) {
			return true
		}
	}
	return false
}

func removeStaleShadowPlugins(dir string, keepNames ...string) {
	pruneShadowDirForSourceDir(dir, "", keepNames)
}

func shadowSourceDirHasLiveProcessActivity(dir string, sourceDir string) bool {
	if entries, errRead := os.ReadDir(dir); errRead == nil && shadowDirHasLiveProcessActivity(entries) {
		return true
	}
	if sourceDir == "" {
		return false
	}
	fallbackDir := filepath.Join(shadowPluginBaseDir(), shadowPluginCacheDirName, shadowDirectoryDigest(sourceDir))
	if filepath.Clean(fallbackDir) == filepath.Clean(dir) {
		return false
	}
	fallbackEntries, errRead := os.ReadDir(fallbackDir)
	if errRead != nil {
		return false
	}
	return shadowDirHasLiveProcessActivity(fallbackEntries)
}

func pruneShadowDirForSourceDir(dir string, sourceDir string, keepNames []string) {
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		return
	}
	keep := make(map[string]struct{}, len(keepNames))
	for _, name := range keepNames {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			keep[filepath.Base(trimmed)] = struct{}{}
		}
	}
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, shadowPluginTempPrefix) {
			if !shadowTempFileOwnedByLiveProcess(name) {
				removeShadowPlugin(filepath.Join(dir, name))
			}
			continue
		}
		if strings.HasPrefix(name, shadowPluginPrefix) {
			if _, okKeep := keep[name]; okKeep {
				continue
			}
			if hook := pruneShadowBeforeLockHook; hook != nil {
				hook(name)
			}
			removeStaleShadowPluginCandidate(dir, sourceDir, name)
		}
	}
}

func removeStaleShadowPluginCandidate(dir string, sourceDir string, name string) {
	unlockCoord, errLock := acquireShadowCoordLock(sourceDir, dir, true, true)
	if errLock != nil {
		return
	}
	defer unlockCoord()
	if shadowSourceDirHasLiveProcessActivity(dir, sourceDir) {
		return
	}
	if sourceDir != "" && shadowMatchesCurrentSourceDir(sourceDir, name) {
		return
	}
	removeShadowPlugin(filepath.Join(dir, name))
}

func shadowMatchesCurrentSourceDir(sourceDir string, shadowFileName string) bool {
	entries, errRead := os.ReadDir(sourceDir)
	if errRead != nil {
		return false
	}
	ext := filepath.Ext(shadowFileName)
	for _, entry := range entries {
		if entry == nil || !entry.Type().IsRegular() {
			continue
		}
		filePath := filepath.Join(sourceDir, entry.Name())
		file, okFile := pluginFileFromPath(filePath, ext)
		if !okFile {
			continue
		}
		if !strings.HasPrefix(shadowFileName, shadowPluginPrefix+file.ID+"-") {
			continue
		}
		_, digest, errHash := cachedOrHashPluginFile(filePath)
		if errHash != nil {
			continue
		}
		if filepath.Base(shadowPluginPath("", file.ID, digest, ext)) == shadowFileName {
			return true
		}
	}
	return false
}

type shadowDirTarget struct {
	shadowDir string
	sourceDir string
}

func cleanupStaleShadowPlugins(root string, activeFiles []pluginFile) {
	keepNames := make([]string, 0, len(activeFiles))
	seenDirs := make(map[string]struct{})
	targets := make([]shadowDirTarget, 0, len(activeFiles)*2+4)
	addShadowDirsForSourceDir := func(sourceDir string) {
		cleanedSourceDir := filepath.Clean(sourceDir)
		for _, dir := range []string{
			filepath.Join(cleanedSourceDir, shadowPluginLocalDirName),
			filepath.Join(shadowPluginBaseDir(), shadowPluginCacheDirName, shadowDirectoryDigest(cleanedSourceDir)),
		} {
			cleaned := filepath.Clean(dir)
			if _, exists := seenDirs[cleaned]; exists {
				continue
			}
			seenDirs[cleaned] = struct{}{}
			targets = append(targets, shadowDirTarget{
				shadowDir: cleaned,
				sourceDir: cleanedSourceDir,
			})
		}
	}
	for _, candidateDir := range candidateDirs(root, runtime.GOOS, runtime.GOARCH) {
		addShadowDirsForSourceDir(candidateDir)
	}
	seenSources := make(map[string]struct{}, len(activeFiles))
	for _, file := range activeFiles {
		id := strings.TrimSpace(file.ID)
		path := strings.TrimSpace(file.Path)
		if id == "" || path == "" {
			continue
		}
		source := filepath.Clean(path)
		if _, seen := seenSources[source]; seen {
			continue
		}
		seenSources[source] = struct{}{}
		addShadowDirsForSourceDir(filepath.Dir(source))
		_, digest, errHash := cachedOrHashPluginFile(source)
		if errHash != nil {
			continue
		}
		keepNames = append(keepNames, filepath.Base(shadowPluginPath("", id, digest, filepath.Ext(source))))
	}
	for _, target := range targets {
		pruneShadowDirForSourceDir(target.shadowDir, target.sourceDir, keepNames)
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
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Debug("pluginhost: failed to close shadow plugin file")
		}
	}()
	hasher := sha256.New()
	if _, errCopy := io.Copy(hasher, file); errCopy != nil {
		return false
	}
	return hex.EncodeToString(hasher.Sum(nil)) == digest
}
