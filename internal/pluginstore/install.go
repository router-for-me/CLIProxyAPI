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
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/cpu"
)

type InstallOptions struct {
	PluginsDir string
	GOOS       string
	GOARCH     string
	// PluginLoaded reports whether the plugin's dynamic library is currently
	// loaded by the running host. Windows installs are rejected only when they
	// would overwrite an existing target file while it returns true.
	PluginLoaded func() bool
	// BeforeWrite runs after the archive has been downloaded and verified, but
	// before an existing target plugin file is replaced.
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
	Skipped     bool   `json:"skipped"`
}

func (c Client) Install(ctx context.Context, plugin Plugin, options InstallOptions) (InstallResult, error) {
	if errValidate := ValidatePlugin(plugin); errValidate != nil {
		return InstallResult{}, errValidate
	}
	options = normalizeInstallOptions(options)
	release, errRelease := c.FetchLatestRelease(ctx, plugin)
	if errRelease != nil {
		return InstallResult{}, errRelease
	}
	latestVersion, errVersion := ReleaseVersion(release)
	if errVersion != nil {
		return InstallResult{}, errVersion
	}
	plugin.Version = latestVersion
	return c.installRelease(ctx, plugin, release, latestVersion, options)
}

// InstallVersion installs a plugin artifact from a fixed release tag/version.
func (c Client) InstallVersion(ctx context.Context, plugin Plugin, releaseTag string, version string, options InstallOptions) (InstallResult, error) {
	if errValidate := ValidatePlugin(plugin); errValidate != nil {
		return InstallResult{}, errValidate
	}
	options = normalizeInstallOptions(options)
	version = normalizeVersion(version)
	if !validPluginVersion(version) {
		return InstallResult{}, fmt.Errorf("invalid plugin version %q", version)
	}
	releaseTag = strings.TrimSpace(releaseTag)
	if releaseTag == "" {
		releaseTag = version
	}
	release, errRelease := c.FetchReleaseByTag(ctx, plugin, releaseTag)
	if errRelease != nil {
		return InstallResult{}, errRelease
	}
	releaseVersion, errVersion := ReleaseVersion(release)
	if errVersion != nil {
		return InstallResult{}, errVersion
	}
	if releaseVersion != version {
		return InstallResult{}, fmt.Errorf("release tag %q resolved version %q, want %q", releaseTag, releaseVersion, version)
	}
	plugin.Version = version
	return c.installRelease(ctx, plugin, release, version, options)
}

func (c Client) installRelease(ctx context.Context, plugin Plugin, release Release, version string, options InstallOptions) (InstallResult, error) {
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
	plugin.Version = version
	return InstallArchive(archiveData, plugin, options)
}

func InstallArchive(archiveData []byte, plugin Plugin, options InstallOptions) (InstallResult, error) {
	options = normalizeInstallOptions(options)
	id := strings.TrimSpace(plugin.ID)
	if !validPluginID(id) {
		return InstallResult{}, fmt.Errorf("invalid plugin id %q", plugin.ID)
	}
	version := normalizeVersion(plugin.Version)
	if !validPluginVersion(version) {
		return InstallResult{}, fmt.Errorf("invalid plugin version %q", plugin.Version)
	}
	plugin.Version = version
	reader, errZip := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
	if errZip != nil {
		return InstallResult{}, fmt.Errorf("open zip: %w", errZip)
	}

	libraryData, mode, errLibrary := readTargetLibrary(reader, id, version, options.GOOS)
	if errLibrary != nil {
		return InstallResult{}, errLibrary
	}

	targetPath, errTarget := installTargetPath(options, id, version)
	if errTarget != nil {
		return InstallResult{}, errTarget
	}
	overwritten := false
	if _, errStat := os.Stat(targetPath); errStat == nil {
		overwritten = true
	} else if !errors.Is(errStat, os.ErrNotExist) {
		return InstallResult{}, fmt.Errorf("stat target plugin: %w", errStat)
	}
	if overwritten {
		existingData, errReadExisting := os.ReadFile(targetPath)
		if errReadExisting != nil {
			return InstallResult{}, fmt.Errorf("read target plugin: %w", errReadExisting)
		}
		if bytes.Equal(existingData, libraryData) {
			return InstallResult{
				ID:          id,
				Version:     strings.TrimSpace(plugin.Version),
				Path:        targetPath,
				Overwritten: true,
				Skipped:     true,
			}, nil
		}
	}
	// Re-check immediately before replacing an existing file: the same version
	// may have been loaded while the archive was being downloaded and verified.
	if overwritten && options.BeforeWrite != nil {
		if errBeforeWrite := options.BeforeWrite(); errBeforeWrite != nil {
			return InstallResult{}, fmt.Errorf("prepare plugin write: %w", errBeforeWrite)
		}
	}
	if overwritten && loadedPluginInstallBlocked(options) {
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

func installTargetPath(options InstallOptions, id string, version string) (string, error) {
	version = normalizeVersion(version)
	if !validPluginVersion(version) {
		return "", fmt.Errorf("invalid plugin version %q", version)
	}
	return filepath.Join(options.PluginsDir, options.GOOS, options.GOARCH, versionedPluginFileName(id, version, options.GOOS)), nil
}

func readTargetLibrary(reader *zip.Reader, id string, version string, goos string) ([]byte, os.FileMode, error) {
	targetName := strings.TrimSpace(id) + pluginExtension(goos)
	versionedTargetName := versionedPluginFileName(id, version, goos)
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
		if cleanedName != targetName && cleanedName != versionedTargetName {
			if path.Base(cleanedName) == targetName || path.Base(cleanedName) == versionedTargetName {
				return nil, 0, fmt.Errorf("target dynamic library must be at zip root")
			}
			return nil, 0, fmt.Errorf("dynamic library filename must be %s or %s", targetName, versionedTargetName)
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

func versionedPluginFileName(id string, version string, goos string) string {
	return strings.TrimSpace(id) + "-v" + normalizeVersion(version) + pluginExtension(goos)
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

type pluginFileInfo struct {
	ID      string
	Path    string
	Version string
}

func discoverCurrentPluginFiles(root string) ([]pluginFileInfo, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "plugins"
	}
	candidates := pluginCandidateDirs(root, runtime.GOOS, runtime.GOARCH, cpuVariant())
	extension := pluginExtension(runtime.GOOS)
	selected := make([]pluginFileInfo, 0)
	seen := make(map[string]struct{})
	for _, dir := range candidates {
		entries, errReadDir := os.ReadDir(dir)
		if errReadDir != nil {
			if os.IsNotExist(errReadDir) {
				continue
			}
			return nil, errReadDir
		}
		files := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry == nil || !entry.Type().IsRegular() {
				continue
			}
			if strings.HasSuffix(strings.ToLower(entry.Name()), extension) {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
		sort.Strings(files)
		for _, path := range files {
			file, okFile := pluginFileInfoFromPath(path, extension)
			if !okFile {
				continue
			}
			if _, exists := seen[file.ID]; exists {
				continue
			}
			seen[file.ID] = struct{}{}
			selected = append(selected, file)
		}
	}
	return selected, nil
}

func pluginCandidateDirs(root string, goos string, goarch string, variant string) []string {
	dirs := make([]string, 0, 3)
	if variant != "" {
		dirs = append(dirs, filepath.Join(root, goos, goarch+"-"+variant))
	}
	dirs = append(dirs, filepath.Join(root, goos, goarch))
	dirs = append(dirs, root)
	return dirs
}

func pluginIDFromPath(path string) string {
	file, ok := pluginFileInfoFromPath(path, "")
	if ok {
		return file.ID
	}
	base := filepath.Base(path)
	lowerBase := strings.ToLower(base)
	for _, extension := range []string{".so", ".dylib", ".dll"} {
		if strings.HasSuffix(lowerBase, extension) {
			return base[:len(base)-len(extension)]
		}
	}
	return base
}

func pluginFileInfoFromPath(filePath string, requiredExtension string) (pluginFileInfo, bool) {
	base := filepath.Base(filePath)
	lowerBase := strings.ToLower(base)
	extension := strings.TrimSpace(requiredExtension)
	if extension != "" {
		if !strings.HasSuffix(lowerBase, strings.ToLower(extension)) {
			return pluginFileInfo{}, false
		}
	} else {
		for _, candidateExtension := range []string{".so", ".dylib", ".dll"} {
			if strings.HasSuffix(lowerBase, candidateExtension) {
				extension = candidateExtension
				break
			}
		}
		if extension == "" {
			return pluginFileInfo{}, false
		}
	}
	name := base[:len(base)-len(extension)]
	id := name
	version := ""
	if versionIndex := strings.LastIndex(name, "-v"); versionIndex > 0 {
		candidateID := name[:versionIndex]
		candidateVersion := name[versionIndex+2:]
		if validPluginID(candidateID) && validPluginVersion(candidateVersion) {
			id = candidateID
			version = candidateVersion
		}
	}
	if !validPluginID(id) {
		return pluginFileInfo{}, false
	}
	return pluginFileInfo{ID: id, Path: filePath, Version: version}, true
}

func pluginExtension(goos string) string {
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "darwin", "mac", "macos", "osx":
		return ".dylib"
	case "windows":
		return ".dll"
	default:
		return ".so"
	}
}

func cpuVariant() string {
	if runtime.GOARCH != "amd64" {
		return ""
	}
	if cpu.X86.HasAVX512F && cpu.X86.HasAVX512BW && cpu.X86.HasAVX512CD && cpu.X86.HasAVX512DQ && cpu.X86.HasAVX512VL {
		return "v4"
	}
	if cpu.X86.HasAVX && cpu.X86.HasAVX2 && cpu.X86.HasBMI1 && cpu.X86.HasBMI2 && cpu.X86.HasFMA {
		return "v3"
	}
	if cpu.X86.HasSSE3 && cpu.X86.HasSSSE3 && cpu.X86.HasSSE41 && cpu.X86.HasSSE42 && cpu.X86.HasPOPCNT {
		return "v2"
	}
	return "v1"
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
