package homeplugins

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
	"golang.org/x/sys/cpu"
	"gopkg.in/yaml.v3"
)

type Platform struct {
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
	Variant string `json:"variant,omitempty"`
}

type PluginRuntime interface {
	PluginBusy(id string) bool
	UnloadPlugin(id string) bool
}

type PluginLoadInspector interface {
	PluginRegistered(id string) bool
}

type SyncReport struct {
	SchemaVersion int                   `json:"schema_version"`
	TaskID        uint                  `json:"task_id,omitempty"`
	Task          string                `json:"task"`
	NodeID        string                `json:"node_id,omitempty"`
	Status        string                `json:"status"`
	Phase         string                `json:"phase"`
	OK            bool                  `json:"ok"`
	StartedAt     time.Time             `json:"started_at"`
	FinishedAt    time.Time             `json:"finished_at,omitempty"`
	UpdatedAt     time.Time             `json:"updated_at"`
	Platform      Platform              `json:"platform"`
	Plugins       []PluginInstallStatus `json:"plugins"`
	Error         string                `json:"error,omitempty"`
}

type PluginInstallStatus struct {
	ID            string `json:"id"`
	Version       string `json:"version,omitempty"`
	ReleaseTag    string `json:"release_tag,omitempty"`
	Repository    string `json:"repository,omitempty"`
	InstallStatus string `json:"install_status"`
	LoadStatus    string `json:"load_status,omitempty"`
	Path          string `json:"path,omitempty"`
	Skipped       bool   `json:"skipped,omitempty"`
	Overwritten   bool   `json:"overwritten,omitempty"`
	Error         string `json:"error,omitempty"`
}

const (
	pluginTaskName         = "plugin-sync"
	pluginDeleteTaskName   = "plugin-delete"
	pluginTaskStatusOK     = "success"
	pluginTaskStatusError  = "failed"
	pluginTaskPhaseInstall = "install"
	pluginTaskPhaseLoad    = "load"
	pluginTaskPhaseDelete  = "delete"

	pluginInstallStatusInstalled = "installed"
	pluginInstallStatusSkipped   = "skipped"
	pluginInstallStatusFailed    = "failed"
	pluginInstallStatusDeleted   = "deleted"
	pluginInstallStatusMissing   = "missing"
	pluginLoadStatusLoaded       = "loaded"
	pluginLoadStatusFailed       = "failed"
)

// CurrentPlatform reports the platform used by pluginhost discovery.
func CurrentPlatform() Platform {
	return Platform{
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Variant: cpuVariant(),
	}
}

func NormalizePlatform(platform Platform) Platform {
	goos := strings.ToLower(strings.TrimSpace(platform.GOOS))
	switch goos {
	case "mac", "macos", "osx":
		goos = "darwin"
	}
	goarch := strings.ToLower(strings.TrimSpace(platform.GOARCH))
	switch goarch {
	case "x64", "x86_64":
		goarch = "amd64"
	case "aarch64":
		goarch = "arm64"
	}
	variant := strings.ToLower(strings.TrimSpace(platform.Variant))
	return Platform{GOOS: goos, GOARCH: goarch, Variant: variant}
}

func Sync(ctx context.Context, cfg *config.Config, pluginRuntime PluginRuntime) error {
	_, errSync := SyncPlatformWithReport(ctx, cfg, pluginRuntime, CurrentPlatform())
	return errSync
}

func SyncPlatform(ctx context.Context, cfg *config.Config, pluginRuntime PluginRuntime, platform Platform) error {
	_, errSync := SyncPlatformWithReport(ctx, cfg, pluginRuntime, platform)
	return errSync
}

func SyncWithReport(ctx context.Context, cfg *config.Config, pluginRuntime PluginRuntime) (SyncReport, error) {
	return SyncPlatformWithReport(ctx, cfg, pluginRuntime, CurrentPlatform())
}

func SyncPlatformWithReport(ctx context.Context, cfg *config.Config, pluginRuntime PluginRuntime, platform Platform) (SyncReport, error) {
	if cfg == nil || !cfg.Home.Enabled || !cfg.Plugins.Enabled {
		return newSyncReport(platform), nil
	}
	platform = NormalizePlatform(platform)
	report := newSyncReport(platform)
	if platform.GOOS == "" {
		errPlatform := fmt.Errorf("home plugins: goos is required")
		finishReport(&report, errPlatform)
		return report, errPlatform
	}
	if platform.GOARCH == "" {
		errPlatform := fmt.Errorf("home plugins: goarch is required")
		finishReport(&report, errPlatform)
		return report, errPlatform
	}
	report.Platform = platform
	root := strings.TrimSpace(cfg.Plugins.Dir)
	if root == "" {
		root = "plugins"
	}
	client := newPluginStoreClient(cfg)
	var syncErrors []error
	ids := make([]string, 0, len(cfg.Plugins.Configs))
	for id := range cfg.Plugins.Configs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		item := cfg.Plugins.Configs[id]
		if !pluginConfigEnabled(item) {
			continue
		}
		manifest, okManifest, errManifest := storeManifestFromPluginConfig(id, item)
		if errManifest != nil {
			status := PluginInstallStatus{
				ID:            strings.TrimSpace(id),
				InstallStatus: pluginInstallStatusFailed,
				Error:         errManifest.Error(),
			}
			report.Plugins = append(report.Plugins, status)
			syncErrors = append(syncErrors, errManifest)
			continue
		}
		if !okManifest {
			continue
		}
		status := pluginStatusFromManifest(manifest)
		result, errSync := installManifest(ctx, client, manifest, root, platform, pluginRuntime)
		if errSync != nil {
			status.InstallStatus = pluginInstallStatusFailed
			status.Error = errSync.Error()
			report.Plugins = append(report.Plugins, status)
			syncErrors = append(syncErrors, errSync)
			continue
		}
		status.Path = strings.TrimSpace(result.Path)
		status.Skipped = result.Skipped
		status.Overwritten = result.Overwritten
		if result.Skipped {
			status.InstallStatus = pluginInstallStatusSkipped
		} else {
			status.InstallStatus = pluginInstallStatusInstalled
		}
		report.Plugins = append(report.Plugins, status)
	}
	errSync := errors.Join(syncErrors...)
	finishReport(&report, errSync)
	return report, errSync
}

func installManifest(ctx context.Context, client sdkpluginstore.Client, manifest sdkpluginstore.Manifest, root string, platform Platform, pluginRuntime PluginRuntime) (sdkpluginstore.InstallResult, error) {
	id := strings.TrimSpace(manifest.ID)
	if id == "" {
		return sdkpluginstore.InstallResult{}, fmt.Errorf("home plugins: manifest plugin id is empty")
	}
	pluginIsBusy := func() bool {
		return pluginRuntime != nil && pluginRuntime.PluginBusy(id)
	}
	result, errInstall := client.InstallManifest(ctx, manifest, sdkpluginstore.InstallOptions{
		PluginsDir:   root,
		GOOS:         platform.GOOS,
		GOARCH:       platform.GOARCH,
		PluginLoaded: pluginIsBusy,
	})
	if errInstall != nil {
		return sdkpluginstore.InstallResult{}, fmt.Errorf("home plugins: install %s: %w", id, errInstall)
	}
	return result, nil
}

func DeleteWithReport(ctx context.Context, cfg *config.Config, pluginRuntime PluginRuntime, taskID uint, pluginID string) SyncReport {
	_ = ctx
	platform := CurrentPlatform()
	report := newSyncReport(platform)
	report.TaskID = taskID
	report.Task = pluginDeleteTaskName
	report.Phase = pluginTaskPhaseDelete
	pluginID = strings.TrimSpace(pluginID)
	status := PluginInstallStatus{ID: pluginID}
	if cfg == nil {
		status.InstallStatus = pluginInstallStatusFailed
		status.Error = "home plugins: config is nil"
		report.Plugins = append(report.Plugins, status)
		finishReport(&report, errors.New(status.Error))
		return report
	}
	root := strings.TrimSpace(cfg.Plugins.Dir)
	if root == "" {
		root = "plugins"
	}
	path, deleted, errDelete := deletePluginArtifact(root, pluginID, pluginRuntime)
	status.Path = strings.TrimSpace(path)
	switch {
	case errDelete != nil:
		status.InstallStatus = pluginInstallStatusFailed
		status.Error = errDelete.Error()
	case deleted:
		status.InstallStatus = pluginInstallStatusDeleted
	default:
		status.InstallStatus = pluginInstallStatusMissing
	}
	report.Plugins = append(report.Plugins, status)
	finishReport(&report, errDelete)
	return report
}

func deletePluginArtifact(root string, id string, pluginRuntime PluginRuntime) (string, bool, error) {
	id = strings.TrimSpace(id)
	if !validPluginFileID(id) {
		return "", false, fmt.Errorf("invalid plugin id %q", id)
	}
	path, errPath := currentPluginFilePath(root, id)
	if errPath != nil {
		return "", false, errPath
	}
	if path == "" {
		return "", false, nil
	}
	if pluginRuntime != nil && pluginRuntime.PluginBusy(id) {
		if !pluginRuntime.UnloadPlugin(id) && pluginRuntime.PluginBusy(id) {
			return path, false, sdkpluginstore.ErrLoadedPluginLocked
		}
	}
	if errRemove := os.Remove(path); errRemove != nil {
		if errors.Is(errRemove, os.ErrNotExist) {
			return path, false, nil
		}
		return path, false, errRemove
	}
	return path, true, nil
}

func currentPluginFilePath(root string, id string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "plugins"
	}
	platform := CurrentPlatform()
	extension := pluginExtension(platform.GOOS)
	var selected pluginFileInfo
	for _, dir := range pluginCandidateDirs(root, platform.GOOS, platform.GOARCH, platform.Variant) {
		entries, errReadDir := os.ReadDir(dir)
		if errReadDir != nil {
			if errors.Is(errReadDir, os.ErrNotExist) {
				continue
			}
			return "", errReadDir
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
		for _, filePath := range files {
			file, okFile := pluginFileFromPath(filePath, extension)
			if !okFile || file.ID != id {
				continue
			}
			if pluginFilePreferred(file, selected) {
				selected = file
			}
		}
	}
	return selected.Path, nil
}

type pluginFileInfo struct {
	ID      string
	Path    string
	Version string
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
	file, ok := pluginFileFromPath(path, "")
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

func pluginFileFromPath(filePath string, requiredExtension string) (pluginFileInfo, bool) {
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
		if validPluginFileID(candidateID) && validPluginFileVersion(candidateVersion) {
			id = candidateID
			version = candidateVersion
		}
	}
	if !validPluginFileID(id) {
		return pluginFileInfo{}, false
	}
	return pluginFileInfo{ID: id, Path: filePath, Version: version}, true
}

func pluginFilePreferred(candidate pluginFileInfo, current pluginFileInfo) bool {
	if strings.TrimSpace(current.Path) == "" {
		return true
	}
	if candidate.Version == "" {
		return false
	}
	if current.Version == "" {
		return true
	}
	return sdkpluginstore.UpdateAvailable(current.Version, candidate.Version)
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

func validPluginFileID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return false
	}
	for _, char := range id {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-', char == '_', char == '.':
		default:
			return false
		}
	}
	return true
}

func validPluginFileVersion(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" || strings.HasPrefix(version, "v") {
		return false
	}
	first := version[0]
	return first >= '0' && first <= '9'
}

func MarkLoadResults(report *SyncReport, inspector PluginLoadInspector) error {
	if report == nil {
		return nil
	}
	report.Phase = pluginTaskPhaseLoad
	var loadErrors []error
	for index := range report.Plugins {
		status := &report.Plugins[index]
		if status.InstallStatus == pluginInstallStatusFailed {
			if status.LoadStatus == "" {
				status.LoadStatus = pluginInstallStatusSkipped
			}
			if strings.TrimSpace(status.Error) != "" {
				loadErrors = append(loadErrors, errors.New(status.Error))
			} else {
				loadErrors = append(loadErrors, fmt.Errorf("home plugins: plugin %s install failed", status.ID))
			}
			continue
		}
		if inspector != nil && inspector.PluginRegistered(status.ID) {
			status.LoadStatus = pluginLoadStatusLoaded
			continue
		}
		status.LoadStatus = pluginLoadStatusFailed
		errLoad := fmt.Errorf("home plugins: plugin %s installed but not loaded", status.ID)
		if strings.TrimSpace(status.Error) == "" {
			status.Error = errLoad.Error()
		}
		loadErrors = append(loadErrors, errLoad)
	}
	errLoad := errors.Join(loadErrors...)
	finishReport(report, errLoad)
	return errLoad
}

func newSyncReport(platform Platform) SyncReport {
	now := time.Now().UTC()
	return SyncReport{
		SchemaVersion: 1,
		Task:          pluginTaskName,
		Status:        pluginTaskStatusOK,
		Phase:         pluginTaskPhaseInstall,
		OK:            true,
		StartedAt:     now,
		UpdatedAt:     now,
		Platform:      NormalizePlatform(platform),
		Plugins:       []PluginInstallStatus{},
	}
}

func finishReport(report *SyncReport, errTask error) {
	if report == nil {
		return
	}
	now := time.Now().UTC()
	report.FinishedAt = now
	report.UpdatedAt = now
	report.OK = errTask == nil
	if errTask != nil {
		report.Status = pluginTaskStatusError
		report.Error = errTask.Error()
		return
	}
	report.Status = pluginTaskStatusOK
	report.Error = ""
}

func pluginStatusFromManifest(manifest sdkpluginstore.Manifest) PluginInstallStatus {
	return PluginInstallStatus{
		ID:            strings.TrimSpace(manifest.ID),
		Version:       strings.TrimSpace(manifest.Version),
		ReleaseTag:    strings.TrimSpace(manifest.ReleaseTag),
		Repository:    strings.TrimSpace(manifest.Repository),
		InstallStatus: pluginInstallStatusFailed,
	}
}

func storeManifestFromPluginConfig(id string, item config.PluginInstanceConfig) (sdkpluginstore.Manifest, bool, error) {
	if item.Raw.Kind == 0 {
		return sdkpluginstore.Manifest{}, false, nil
	}
	storeNode := yamlMappingValue(&item.Raw, "store")
	if storeNode == nil || storeNode.Kind == 0 {
		return sdkpluginstore.Manifest{}, false, nil
	}
	var manifest sdkpluginstore.Manifest
	if errDecode := storeNode.Decode(&manifest); errDecode != nil {
		return sdkpluginstore.Manifest{}, false, fmt.Errorf("home plugins: decode store manifest for %s: %w", id, errDecode)
	}
	if strings.TrimSpace(manifest.ID) == "" {
		manifest.ID = strings.TrimSpace(id)
	}
	if errValidate := manifest.Validate(); errValidate != nil {
		return sdkpluginstore.Manifest{}, false, fmt.Errorf("home plugins: invalid store manifest for %s: %w", id, errValidate)
	}
	return manifest, true, nil
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		if keyNode == nil || keyNode.Value != key {
			continue
		}
		return node.Content[i+1]
	}
	return nil
}

var newPluginStoreClient = func(cfg *config.Config) sdkpluginstore.Client {
	client := &http.Client{}
	if cfg != nil && strings.TrimSpace(cfg.ProxyURL) != "" {
		util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: strings.TrimSpace(cfg.ProxyURL)}, client)
	}
	return sdkpluginstore.NewClient(client, "")
}

func pluginConfigEnabled(item config.PluginInstanceConfig) bool {
	return item.Enabled != nil && *item.Enabled
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
