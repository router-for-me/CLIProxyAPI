package pluginstore

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	log "github.com/sirupsen/logrus"
)

type InstallOptions struct {
	PluginsDir string
	GOOS       string
	GOARCH     string
	// PluginLoaded reports whether the plugin's dynamic library is currently
	// loaded by the running host. Windows installs are rejected while it returns
	// true unless BeforeWrite can unload the plugin before replacement.
	PluginLoaded func() bool
	// BeforeWrite runs after the archive has been downloaded and verified, but
	// before the target plugin file is replaced.
	BeforeWrite func() error
}

// ErrLoadedPluginLocked is returned when an install would overwrite a plugin
// library that is loaded by the running process on Windows.
var ErrLoadedPluginLocked = errors.New("loaded plugin library cannot be overwritten while the server is running")

type InstallResult struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Path        string `json:"path"`
	Overwritten bool   `json:"overwritten"`
}

func (c Client) Install(ctx context.Context, plugin Plugin, options InstallOptions) (InstallResult, error) {
	if errValidate := ValidatePlugin(plugin); errValidate != nil {
		return InstallResult{}, errValidate
	}
	options = normalizeInstallOptions(options)
	if loadedPluginInstallBlocked(options) && options.BeforeWrite == nil {
		return InstallResult{}, ErrLoadedPluginLocked
	}
	release, errRelease := c.FetchLatestRelease(ctx, plugin)
	if errRelease != nil {
		return InstallResult{}, errRelease
	}
	latestVersion, errVersion := ReleaseVersion(release)
	if errVersion != nil {
		return InstallResult{}, errVersion
	}
	plugin.Version = latestVersion
	archiveAsset, checksumAsset, errAssets := SelectReleaseAssets(release, plugin.ID, plugin.Version, options.GOOS, options.GOARCH)
	if errAssets != nil {
		return InstallResult{}, errAssets
	}
	archiveData, errArchive := c.DownloadAsset(ctx, archiveAsset)
	if errArchive != nil {
		return InstallResult{}, fmt.Errorf("download %s: %w", archiveAsset.Name, errArchive)
	}
	checksumData, errChecksum := c.DownloadAsset(ctx, checksumAsset)
	if errChecksum != nil {
		return InstallResult{}, fmt.Errorf("download checksums.txt: %w", errChecksum)
	}
	checksums, errParse := ParseChecksums(checksumData)
	if errParse != nil {
		return InstallResult{}, errParse
	}
	if errVerify := VerifyChecksum(archiveAsset.Name, archiveData, checksums); errVerify != nil {
		return InstallResult{}, errVerify
	}
	return InstallArchive(archiveData, plugin, options)
}

func InstallArchive(archiveData []byte, plugin Plugin, options InstallOptions) (InstallResult, error) {
	options = normalizeInstallOptions(options)
	id := strings.TrimSpace(plugin.ID)
	if !pluginhost.ValidatePluginID(id) {
		return InstallResult{}, fmt.Errorf("invalid plugin id %q", plugin.ID)
	}
	reader, errZip := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
	if errZip != nil {
		return InstallResult{}, fmt.Errorf("open zip: %w", errZip)
	}

	libraryData, mode, errLibrary := readTargetLibrary(reader, id, options.GOOS)
	if errLibrary != nil {
		return InstallResult{}, errLibrary
	}

	targetPath, errTarget := installTargetPath(options, id)
	if errTarget != nil {
		return InstallResult{}, errTarget
	}
	overwritten := false
	if _, errStat := os.Stat(targetPath); errStat == nil {
		overwritten = true
	} else if !errors.Is(errStat, os.ErrNotExist) {
		return InstallResult{}, fmt.Errorf("stat target plugin: %w", errStat)
	}
	// Re-check immediately before writing: the plugin may have been loaded
	// while the archive was being downloaded and verified.
	if options.BeforeWrite != nil {
		if errBeforeWrite := options.BeforeWrite(); errBeforeWrite != nil {
			return InstallResult{}, fmt.Errorf("prepare plugin write: %w", errBeforeWrite)
		}
	}
	if loadedPluginInstallBlocked(options) {
		return InstallResult{}, ErrLoadedPluginLocked
	}
	if errWrite := writeFileAtomic(targetPath, libraryData, mode); errWrite != nil {
		return InstallResult{}, errWrite
	}
	return InstallResult{
		ID:          id,
		Version:     strings.TrimSpace(plugin.Version),
		Path:        targetPath,
		Overwritten: overwritten,
	}, nil
}

func installTargetPath(options InstallOptions, id string) (string, error) {
	defaultPath := filepath.Join(options.PluginsDir, options.GOOS, options.GOARCH, id+pluginhost.PluginExtension(options.GOOS))
	if options.GOOS != runtime.GOOS || options.GOARCH != runtime.GOARCH {
		return defaultPath, nil
	}
	files, errDiscover := pluginhost.DiscoverPluginFiles(options.PluginsDir)
	if errDiscover != nil {
		return "", fmt.Errorf("discover current plugin files: %w", errDiscover)
	}
	for _, file := range files {
		if file.ID == id && strings.TrimSpace(file.Path) != "" {
			return file.Path, nil
		}
	}
	return defaultPath, nil
}

func readTargetLibrary(reader *zip.Reader, id string, goos string) ([]byte, os.FileMode, error) {
	targetName := strings.TrimSpace(id) + pluginhost.PluginExtension(goos)
	var target *zip.File
	for _, file := range reader.File {
		cleanedName, errClean := cleanZipName(file.Name)
		if errClean != nil {
			return nil, 0, errClean
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if !regularZipFile(file) {
			return nil, 0, fmt.Errorf("zip entry %s is not a regular file", file.Name)
		}
		if !hasDynamicLibraryExtension(cleanedName) {
			continue
		}
		if cleanedName != targetName {
			if path.Base(cleanedName) == targetName {
				return nil, 0, fmt.Errorf("target dynamic library must be at zip root")
			}
			return nil, 0, fmt.Errorf("dynamic library filename must be %s", targetName)
		}
		if target != nil {
			return nil, 0, fmt.Errorf("zip contains multiple target dynamic libraries")
		}
		target = file
	}
	if target == nil {
		return nil, 0, fmt.Errorf("zip does not contain %s", targetName)
	}

	handle, errOpen := target.Open()
	if errOpen != nil {
		return nil, 0, fmt.Errorf("open %s: %w", targetName, errOpen)
	}
	defer func() {
		if errClose := handle.Close(); errClose != nil {
			log.WithError(errClose).Debug("failed to close plugin archive entry")
		}
	}()
	data, errRead := io.ReadAll(handle)
	if errRead != nil {
		return nil, 0, fmt.Errorf("read %s: %w", targetName, errRead)
	}
	mode := target.FileInfo().Mode().Perm()
	if mode == 0 {
		mode = 0o755
	}
	return data, mode, nil
}

func cleanZipName(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("zip entry has empty name")
	}
	if strings.Contains(name, `\`) {
		return "", fmt.Errorf("zip entry %s uses backslash path separators", name)
	}
	if path.IsAbs(name) {
		return "", fmt.Errorf("zip entry %s is absolute", name)
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("zip entry %s escapes archive root", name)
	}
	return cleaned, nil
}

func regularZipFile(file *zip.File) bool {
	mode := file.FileInfo().Mode()
	return mode.IsRegular() || mode.Type() == 0
}

func hasDynamicLibraryExtension(name string) bool {
	lowerName := strings.ToLower(name)
	return strings.HasSuffix(lowerName, ".dylib") || strings.HasSuffix(lowerName, ".so") || strings.HasSuffix(lowerName, ".dll")
}

func writeFileAtomic(targetPath string, data []byte, mode os.FileMode) error {
	targetDir := filepath.Dir(targetPath)
	if errMkdir := os.MkdirAll(targetDir, 0o755); errMkdir != nil {
		return fmt.Errorf("create plugin directory: %w", errMkdir)
	}

	temp, errTemp := os.CreateTemp(targetDir, "."+filepath.Base(targetPath)+".tmp-*")
	if errTemp != nil {
		return fmt.Errorf("create temp plugin file: %w", errTemp)
	}
	tempPath := temp.Name()
	removeTemp := true
	closed := false
	defer func() {
		if !closed {
			if errClose := temp.Close(); errClose != nil {
				log.WithError(errClose).Debug("failed to close temp plugin file")
			}
		}
		if removeTemp {
			if errRemove := os.Remove(tempPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
				log.WithError(errRemove).Debug("failed to remove temp plugin file")
			}
		}
	}()

	if errChmod := temp.Chmod(mode); errChmod != nil {
		return fmt.Errorf("chmod temp plugin file: %w", errChmod)
	}
	if _, errWrite := temp.Write(data); errWrite != nil {
		return fmt.Errorf("write temp plugin file: %w", errWrite)
	}
	if errSync := temp.Sync(); errSync != nil {
		return fmt.Errorf("sync temp plugin file: %w", errSync)
	}
	if errClose := temp.Close(); errClose != nil {
		return fmt.Errorf("close temp plugin file: %w", errClose)
	}
	closed = true
	if errRename := os.Rename(tempPath, targetPath); errRename != nil {
		if runtime.GOOS == "windows" {
			if errRemove := os.Remove(targetPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
				return fmt.Errorf("remove old plugin file: %w", errRemove)
			}
			if errRenameRetry := os.Rename(tempPath, targetPath); errRenameRetry == nil {
				removeTemp = false
				return nil
			} else {
				return fmt.Errorf("install plugin file: %w", errRenameRetry)
			}
		}
		return fmt.Errorf("install plugin file: %w", errRename)
	}
	removeTemp = false
	return nil
}

func loadedPluginInstallBlocked(options InstallOptions) bool {
	return options.PluginLoaded != nil && strings.EqualFold(options.GOOS, "windows") && options.PluginLoaded()
}

func normalizeInstallOptions(options InstallOptions) InstallOptions {
	options.PluginsDir = strings.TrimSpace(options.PluginsDir)
	if options.PluginsDir == "" {
		options.PluginsDir = "plugins"
	}
	options.GOOS = strings.TrimSpace(options.GOOS)
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	options.GOARCH = strings.TrimSpace(options.GOARCH)
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	return options
}
