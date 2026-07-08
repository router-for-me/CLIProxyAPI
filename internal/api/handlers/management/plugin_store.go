package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/htmlsanitize"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const (
	// pluginReleaseCacheTTL bounds how long a resolved latest release version is
	// reused before the GitHub API is queried again.
	pluginReleaseCacheTTL = 10 * time.Minute
	// pluginReleaseFailureCacheTTL throttles retries after a failed lookup so a
	// rate-limited or unreachable API is not hammered on every listing.
	pluginReleaseFailureCacheTTL = 30 * time.Second
)

type pluginReleaseCacheEntry struct {
	version   string
	expiresAt time.Time
}

type pluginStoreListResponse struct {
	PluginsEnabled bool                   `json:"plugins_enabled"`
	PluginsDir     string                 `json:"plugins_dir"`
	Sources        []pluginStoreSource    `json:"sources"`
	SourceErrors   []pluginStoreSourceErr `json:"source_errors,omitempty"`
	Plugins        []pluginStoreListEntry `json:"plugins"`
}

type pluginStoreSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type pluginStoreSourceErr struct {
	SourceID   string `json:"source_id"`
	SourceName string `json:"source_name"`
	SourceURL  string `json:"source_url"`
	Message    string `json:"message"`
}

type pluginStoreListEntry struct {
	StoreID          string                `json:"store_id"`
	SourceID         string                `json:"source_id"`
	SourceName       string                `json:"source_name"`
	SourceURL        string                `json:"source_url"`
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Description      string                `json:"description"`
	Author           string                `json:"author"`
	Version          string                `json:"version"`
	Repository       string                `json:"repository"`
	InstallType      string                `json:"install_type"`
	AuthRequired     bool                  `json:"auth_required"`
	AuthConfigured   bool                  `json:"auth_configured"`
	Platforms        []pluginStorePlatform `json:"platforms,omitempty"`
	Logo             string                `json:"logo,omitempty"`
	Homepage         string                `json:"homepage,omitempty"`
	License          string                `json:"license,omitempty"`
	Tags             []string              `json:"tags,omitempty"`
	Installed        bool                  `json:"installed"`
	InstalledVersion string                `json:"installed_version"`
	Path             string                `json:"path"`
	Configured       bool                  `json:"configured"`
	Registered       bool                  `json:"registered"`
	Enabled          bool                  `json:"enabled"`
	EffectiveEnabled bool                  `json:"effective_enabled"`
	UpdateAvailable  bool                  `json:"update_available"`
}

type pluginStorePlatform struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

type pluginInstallResponse struct {
	Status          string `json:"status"`
	SourceID        string `json:"source_id"`
	SourceName      string `json:"source_name"`
	SourceURL       string `json:"source_url"`
	ID              string `json:"id"`
	Version         string `json:"version"`
	InstallType     string `json:"install_type"`
	Path            string `json:"path"`
	PluginsEnabled  bool   `json:"plugins_enabled"`
	RestartRequired bool   `json:"restart_required"`
}

type pluginInstallRequest struct {
	Version string `json:"version"`
}

type pluginLocalStatus struct {
	Installed        bool
	InstalledVersion string
	Path             string
	Configured       bool
	Registered       bool
	Enabled          bool
	EffectiveEnabled bool
}

type sourcedPlugin struct {
	source pluginstore.Source
	plugin pluginstore.Plugin
}

func (h *Handler) ListPluginStore(c *gin.Context) {
	pluginsEnabled, pluginsDir, proxyURL, sourceConfigs, storeAuth, configs, host := h.pluginStoreSnapshot()
	sources, errSources := h.pluginStoreSources(sourceConfigs)
	if errSources != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_store_source_invalid", "message": errSources.Error()})
		return
	}
	plugins, sourceErrors := h.fetchSourcedPlugins(c.Request.Context(), proxyURL, storeAuth, sources)
	if len(plugins) == 0 && len(sourceErrors) > 0 {
		c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_store_registry_failed", "message": sourceErrors[0].Message})
		return
	}
	statuses, errStatus := pluginLocalStatuses(pluginsEnabled, pluginsDir, configs, host)
	if errStatus != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_discovery_failed", "message": errStatus.Error()})
		return
	}

	latestInput := make([]pluginstore.Plugin, 0, len(plugins))
	for _, item := range plugins {
		latestInput = append(latestInput, item.plugin)
	}
	client := h.newPluginStoreClient(proxyURL, "", storeAuth)
	latestVersions := h.latestPluginVersions(c.Request.Context(), client, latestInput)

	entries := make([]pluginStoreListEntry, 0, len(plugins))
	for index, item := range plugins {
		plugin := item.plugin
		status := statuses[plugin.ID]
		installedVersion := status.InstalledVersion
		// Fall back to the registry version when the latest release is unknown.
		storeVersion := plugin.Version
		if latestVersions[index] != "" {
			storeVersion = latestVersions[index]
		}
		entries = append(entries, pluginStoreListEntry{
			StoreID:          htmlsanitize.String(item.source.ID + "/" + plugin.ID),
			SourceID:         htmlsanitize.String(item.source.ID),
			SourceName:       htmlsanitize.String(item.source.Name),
			SourceURL:        htmlsanitize.String(item.source.URL),
			ID:               htmlsanitize.String(plugin.ID),
			Name:             htmlsanitize.String(plugin.Name),
			Description:      htmlsanitize.String(plugin.Description),
			Author:           htmlsanitize.String(plugin.Author),
			Version:          htmlsanitize.String(storeVersion),
			Repository:       htmlsanitize.String(plugin.Repository),
			InstallType:      htmlsanitize.String(pluginstore.PluginInstallType(plugin)),
			AuthRequired:     plugin.AuthRequired,
			AuthConfigured:   pluginAuthConfigured(item.source, plugin, storeAuth),
			Platforms:        sanitizePluginStorePlatforms(pluginstore.PluginPlatforms(plugin)),
			Logo:             htmlsanitize.String(plugin.Logo),
			Homepage:         htmlsanitize.String(plugin.Homepage),
			License:          htmlsanitize.String(plugin.License),
			Tags:             htmlsanitize.Strings(plugin.Tags),
			Installed:        status.Installed,
			InstalledVersion: htmlsanitize.String(installedVersion),
			Path:             htmlsanitize.String(status.Path),
			Configured:       status.Configured,
			Registered:       status.Registered,
			Enabled:          status.Enabled,
			EffectiveEnabled: status.EffectiveEnabled,
			UpdateAvailable:  pluginstore.UpdateAvailable(installedVersion, storeVersion),
		})
	}

	c.JSON(http.StatusOK, pluginStoreListResponse{
		PluginsEnabled: pluginsEnabled,
		PluginsDir:     htmlsanitize.String(pluginsDir),
		Sources:        sanitizePluginStoreSources(sources),
		SourceErrors:   sanitizePluginStoreSourceErrors(sourceErrors),
		Plugins:        entries,
	})
}

func (h *Handler) InstallPluginFromStore(c *gin.Context) {
	h.installPluginFromStore(c, runtime.GOOS, runtime.GOARCH)
}

func (h *Handler) installPluginFromStore(c *gin.Context, goos, goarch string) {
	id, okID := pluginIDFromRequest(c)
	if !okID {
		return
	}
	requestedVersion, errVersionRequest := pluginInstallRequestedVersion(c)
	if errVersionRequest != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": errVersionRequest.Error()})
		return
	}
	installCtx := c.Request.Context()
	pluginsEnabled, pluginsDir, proxyURL, sourceConfigs, storeAuth, _, host := h.pluginStoreSnapshot()
	sources, errSources := h.pluginStoreSources(sourceConfigs)
	if errSources != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_store_source_invalid", "message": errSources.Error()})
		return
	}
	source, plugin, client, okPlugin := h.findPluginStoreInstallTarget(installCtx, proxyURL, storeAuth, sources, id, c.Query("source"), c)
	if !okPlugin {
		return
	}
	pluginIsBusy := func() bool { return pluginBusy(host, id) }
	installOptions := pluginstore.InstallOptions{
		PluginsDir:   pluginsDir,
		GOOS:         goos,
		GOARCH:       goarch,
		PluginLoaded: pluginIsBusy,
	}
	var manifest pluginstore.Manifest
	var result pluginstore.InstallResult
	var errInstall error
	switch pluginstore.PluginInstallType(plugin) {
	case pluginstore.InstallTypeDirect:
		var errManifest error
		manifest, errManifest = pluginStoreDirectManifest(source, plugin, requestedVersion)
		if errManifest != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_manifest_invalid", "message": errManifest.Error()})
			return
		}
		result, errInstall = client.InstallManifest(installCtx, manifest, installOptions)
	case pluginstore.InstallTypeGitHubRelease:
		result, errInstall = installPluginStoreGitHubRelease(installCtx, client, plugin, requestedVersion, installOptions)
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_manifest_invalid", "message": fmt.Sprintf("unsupported install type %q", plugin.Install.Type)})
		return
	}
	if errInstall != nil {
		if errors.Is(errInstall, pluginstore.ErrLoadedPluginLocked) {
			c.JSON(http.StatusConflict, gin.H{
				"error":            "plugin_update_requires_restart",
				"message":          "loaded plugin cannot be overwritten while the server is running",
				"restart_required": true,
			})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_install_failed", "message": errInstall.Error()})
		return
	}
	if manifest.ID == "" {
		var errManifest error
		manifest, errManifest = pluginStoreManifestForInstall(source, plugin, result)
		if errManifest != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "plugin_manifest_failed",
				"message": fmt.Sprintf("plugin file installed at %s but creating store manifest failed: %s", result.Path, errManifest.Error()),
				"path":    result.Path,
			})
			return
		}
	}
	restartRequired := false

	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "config_unavailable",
			"message": fmt.Sprintf("plugin file installed at %s but config is unavailable to enable it", result.Path),
			"path":    result.Path,
		})
		return
	}
	if errEnable := h.enablePluginConfigLocked(id, manifest); errEnable != nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "config_update_failed",
			"message": fmt.Sprintf("plugin file installed at %s but enabling it in config failed: %s", result.Path, errEnable.Error()),
			"path":    result.Path,
		})
		return
	}
	if errSave := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); errSave != nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "config_save_failed",
			"message": fmt.Sprintf("plugin file installed at %s but saving config failed: %s", result.Path, errSave.Error()),
			"path":    result.Path,
		})
		return
	}
	cfgSnapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()

	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), cfgSnapshot)
	log.WithFields(log.Fields{
		"plugin_id":    result.ID,
		"plugin_name":  plugin.Name,
		"source_id":    source.ID,
		"version":      result.Version,
		"install_type": result.InstallType,
		"path":         result.Path,
		"overwritten":  result.Overwritten,
	}).Info("pluginstore: plugin installed")

	c.JSON(http.StatusOK, pluginInstallResponse{
		Status:          "installed",
		SourceID:        htmlsanitize.String(source.ID),
		SourceName:      htmlsanitize.String(source.Name),
		SourceURL:       htmlsanitize.String(source.URL),
		ID:              htmlsanitize.String(result.ID),
		Version:         htmlsanitize.String(result.Version),
		InstallType:     htmlsanitize.String(result.InstallType),
		Path:            htmlsanitize.String(result.Path),
		PluginsEnabled:  pluginsEnabled,
		RestartRequired: restartRequired,
	})
}

func pluginStoreDirectManifest(source pluginstore.Source, plugin pluginstore.Plugin, requestedVersion string) (pluginstore.Manifest, error) {
	version := normalizePluginStoreRequestedVersion(requestedVersion)
	if version == "" {
		version = normalizePluginStoreRequestedVersion(plugin.Version)
	}
	if normalizePluginStoreRequestedVersion(plugin.Version) == version {
		plugin.Version = version
		return pluginstore.ManifestFromPlugin(source, plugin)
	}
	for _, candidate := range plugin.Versions {
		if normalizePluginStoreRequestedVersion(candidate.Version) != version {
			continue
		}
		plugin.Version = version
		plugin.Install = candidate.Install
		if strings.TrimSpace(plugin.Install.Type) == "" {
			plugin.Install.Type = pluginstore.InstallTypeDirect
		}
		return pluginstore.ManifestFromPlugin(source, plugin)
	}
	return pluginstore.Manifest{}, fmt.Errorf("direct plugin version %q not found", version)
}

func installPluginStoreGitHubRelease(ctx context.Context, client pluginstore.Client, plugin pluginstore.Plugin, requestedVersion string, options pluginstore.InstallOptions) (pluginstore.InstallResult, error) {
	version := normalizePluginStoreRequestedVersion(requestedVersion)
	if version == "" {
		return client.Install(ctx, plugin, options)
	}
	tags := pluginStoreReleaseTagCandidates(requestedVersion)
	errs := make([]error, 0, len(tags))
	for _, tag := range tags {
		result, errInstall := client.InstallVersion(ctx, plugin, tag, version, options)
		if errInstall == nil {
			return result, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", tag, errInstall))
	}
	return pluginstore.InstallResult{}, fmt.Errorf("install release by tag: %w", errors.Join(errs...))
}

func pluginStoreManifestForInstall(source pluginstore.Source, plugin pluginstore.Plugin, result pluginstore.InstallResult) (pluginstore.Manifest, error) {
	installType := strings.TrimSpace(result.InstallType)
	if installType == "" {
		installType = pluginstore.PluginInstallType(plugin)
	}
	switch installType {
	case pluginstore.InstallTypeDirect:
		plugin.Version = strings.TrimSpace(result.Version)
		plugin.Install = pluginstore.NormalizeInstallPlan(plugin.Install)
		return pluginstore.ManifestFromPlugin(source, plugin)
	case pluginstore.InstallTypeGitHubRelease:
		releaseTag := strings.TrimSpace(result.ReleaseTag)
		if releaseTag == "" {
			return pluginstore.Manifest{}, fmt.Errorf("release tag is required")
		}
		return pluginstore.ManifestFromRelease(source, plugin, pluginstore.Release{TagName: releaseTag})
	default:
		return pluginstore.Manifest{}, fmt.Errorf("unsupported install type %q", result.InstallType)
	}
}

func pluginInstallRequestedVersion(c *gin.Context) (string, error) {
	requestedVersion := strings.TrimSpace(c.Query("version"))
	if c == nil || c.Request == nil || c.Request.Body == nil || c.Request.Body == http.NoBody {
		return requestedVersion, nil
	}
	body, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		return "", fmt.Errorf("read install request: %w", errRead)
	}
	if strings.TrimSpace(string(body)) == "" {
		return requestedVersion, nil
	}
	var req pluginInstallRequest
	if errDecode := json.Unmarshal(body, &req); errDecode != nil {
		return "", fmt.Errorf("decode install request: %w", errDecode)
	}
	bodyVersion := strings.TrimSpace(req.Version)
	if requestedVersion == "" {
		return bodyVersion, nil
	}
	if bodyVersion == "" || normalizePluginStoreRequestedVersion(bodyVersion) == normalizePluginStoreRequestedVersion(requestedVersion) {
		return requestedVersion, nil
	}
	return "", fmt.Errorf("version query %q does not match request body version %q", requestedVersion, bodyVersion)
}

func pluginStoreReleaseTagCandidates(version string) []string {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(version), "v") {
		return []string{version, strings.TrimSpace(version[1:])}
	}
	return []string{version, "v" + version}
}

func normalizePluginStoreRequestedVersion(version string) string {
	version = strings.TrimSpace(version)
	if strings.HasPrefix(strings.ToLower(version), "v") {
		return strings.TrimSpace(version[1:])
	}
	return version
}

// enablePluginConfigLocked sets plugins.configs.<id>.enabled and store while
// preserving the rest of the plugin's raw configuration. Callers must hold h.mu.
func (h *Handler) enablePluginConfigLocked(id string, storeManifest pluginstore.Manifest) error {
	ensurePluginConfigMap(h.cfg)
	node := pluginConfigNode(h.cfg.Plugins.Configs[id])
	storeNode, errStoreNode := pluginStoreManifestYAMLNode(storeManifest)
	if errStoreNode != nil {
		return errStoreNode
	}
	setYAMLMappingValue(node, "enabled", boolYAMLNode(true))
	setYAMLMappingValue(node, "store", storeNode)
	updated, errConfig := pluginInstanceConfigFromNode(node)
	if errConfig != nil {
		return fmt.Errorf("decode plugin config: %w", errConfig)
	}
	h.cfg.Plugins.Configs[id] = updated
	return nil
}

func pluginStoreManifestYAMLNode(manifest pluginstore.Manifest) (*yaml.Node, error) {
	var node yaml.Node
	if errEncode := node.Encode(manifest); errEncode != nil {
		return nil, fmt.Errorf("encode store manifest: %w", errEncode)
	}
	return &node, nil
}

func (h *Handler) pluginStoreSnapshot() (bool, string, string, []string, []pluginstore.AuthConfig, map[string]config.PluginInstanceConfig, *pluginhost.Host) {
	if h == nil {
		return false, "plugins", "", nil, nil, map[string]config.PluginInstanceConfig{}, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return false, "plugins", "", nil, nil, map[string]config.PluginInstanceConfig{}, nil
	}
	pluginsEnabled := h.cfg.Plugins.Enabled
	pluginsDir := normalizedPluginsDir(h.cfg.Plugins.Dir)
	proxyURL := strings.TrimSpace(h.cfg.ProxyURL)
	sourceConfigs := append([]string(nil), h.cfg.Plugins.StoreSources...)
	storeAuth := append([]pluginstore.AuthConfig(nil), h.cfg.Plugins.StoreAuth...)
	configs := make(map[string]config.PluginInstanceConfig, len(h.cfg.Plugins.Configs))
	for id, item := range h.cfg.Plugins.Configs {
		configs[id] = item
	}
	return pluginsEnabled, pluginsDir, proxyURL, sourceConfigs, storeAuth, configs, h.pluginHost
}

func (h *Handler) pluginStoreSources(sourceConfigs []string) ([]pluginstore.Source, error) {
	if h != nil && strings.TrimSpace(h.pluginStoreRegistryURL) != "" {
		source := pluginstore.DefaultSource()
		source.URL = strings.TrimSpace(h.pluginStoreRegistryURL)
		return []pluginstore.Source{source}, nil
	}
	return pluginstore.NormalizeSources(sourceConfigs)
}

func (h *Handler) newPluginStoreClient(proxyURL string, registryURL string, storeAuth []pluginstore.AuthConfig) pluginstore.Client {
	registryURL = strings.TrimSpace(registryURL)
	var httpClient pluginstore.HTTPDoer
	if h != nil {
		httpClient = h.pluginStoreHTTPClient
	}
	if registryURL == "" {
		registryURL = pluginstore.DefaultRegistryURL
	}
	if httpClient != nil {
		return pluginstore.Client{HTTPClient: httpClient, RegistryURL: registryURL, Auth: storeAuth}
	}
	client := &http.Client{}
	if strings.TrimSpace(proxyURL) != "" {
		util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: strings.TrimSpace(proxyURL)}, client)
	}
	return pluginstore.Client{HTTPClient: client, RegistryURL: registryURL, Auth: storeAuth}
}

func (h *Handler) fetchSourcedPlugins(ctx context.Context, proxyURL string, storeAuth []pluginstore.AuthConfig, sources []pluginstore.Source) ([]sourcedPlugin, []pluginStoreSourceErr) {
	plugins := make([]sourcedPlugin, 0)
	sourceErrors := make([]pluginStoreSourceErr, 0)
	for _, source := range sources {
		client := h.newPluginStoreClient(proxyURL, source.URL, storeAuth)
		registry, errRegistry := client.FetchRegistry(ctx)
		if errRegistry != nil {
			sourceErrors = append(sourceErrors, pluginStoreSourceErr{
				SourceID:   source.ID,
				SourceName: source.Name,
				SourceURL:  source.URL,
				Message:    errRegistry.Error(),
			})
			continue
		}
		for _, plugin := range registry.Plugins {
			plugins = append(plugins, sourcedPlugin{source: source, plugin: plugin})
		}
	}
	return plugins, sourceErrors
}

func (h *Handler) findPluginStoreInstallTarget(ctx context.Context, proxyURL string, storeAuth []pluginstore.AuthConfig, sources []pluginstore.Source, id string, requestedSourceID string, c *gin.Context) (pluginstore.Source, pluginstore.Plugin, pluginstore.Client, bool) {
	requestedSourceID = strings.TrimSpace(requestedSourceID)
	if requestedSourceID != "" {
		for _, source := range sources {
			if source.ID != requestedSourceID {
				continue
			}
			client := h.newPluginStoreClient(proxyURL, source.URL, storeAuth)
			registry, errRegistry := client.FetchRegistry(ctx)
			if errRegistry != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_store_registry_failed", "message": errRegistry.Error()})
				return pluginstore.Source{}, pluginstore.Plugin{}, pluginstore.Client{}, false
			}
			plugin, okPlugin := registry.PluginByID(id)
			if !okPlugin {
				c.JSON(http.StatusNotFound, gin.H{"error": "plugin_not_found", "message": "plugin not found in registry source"})
				return pluginstore.Source{}, pluginstore.Plugin{}, pluginstore.Client{}, false
			}
			return source, plugin, client, true
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_store_source_not_found", "message": "plugin store source not found"})
		return pluginstore.Source{}, pluginstore.Plugin{}, pluginstore.Client{}, false
	}

	plugins, sourceErrors := h.fetchSourcedPlugins(ctx, proxyURL, storeAuth, sources)
	matches := make([]sourcedPlugin, 0)
	for _, item := range plugins {
		if item.plugin.ID == id {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		if len(plugins) == 0 && len(sourceErrors) > 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_store_registry_failed", "message": sourceErrors[0].Message})
			return pluginstore.Source{}, pluginstore.Plugin{}, pluginstore.Client{}, false
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_not_found", "message": "plugin not found in registry"})
		return pluginstore.Source{}, pluginstore.Plugin{}, pluginstore.Client{}, false
	}
	if len(matches) > 1 {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "plugin_store_source_required",
			"message": "multiple plugin store sources contain this plugin id; specify source",
			"sources": sanitizePluginStoreSources(sourcedPluginSources(matches)),
		})
		return pluginstore.Source{}, pluginstore.Plugin{}, pluginstore.Client{}, false
	}
	match := matches[0]
	return match.source, match.plugin, h.newPluginStoreClient(proxyURL, match.source.URL, storeAuth), true
}

func sourcedPluginSources(plugins []sourcedPlugin) []pluginstore.Source {
	sources := make([]pluginstore.Source, 0, len(plugins))
	for _, item := range plugins {
		sources = append(sources, item.source)
	}
	return sources
}

func sanitizePluginStoreSources(sources []pluginstore.Source) []pluginStoreSource {
	out := make([]pluginStoreSource, 0, len(sources))
	for _, source := range sources {
		out = append(out, pluginStoreSource{
			ID:   htmlsanitize.String(source.ID),
			Name: htmlsanitize.String(source.Name),
			URL:  htmlsanitize.String(source.URL),
		})
	}
	return out
}

func sanitizePluginStoreSourceErrors(sourceErrors []pluginStoreSourceErr) []pluginStoreSourceErr {
	if len(sourceErrors) == 0 {
		return nil
	}
	out := make([]pluginStoreSourceErr, 0, len(sourceErrors))
	for _, sourceError := range sourceErrors {
		out = append(out, pluginStoreSourceErr{
			SourceID:   htmlsanitize.String(sourceError.SourceID),
			SourceName: htmlsanitize.String(sourceError.SourceName),
			SourceURL:  htmlsanitize.String(sourceError.SourceURL),
			Message:    htmlsanitize.String(sourceError.Message),
		})
	}
	return out
}

func sanitizePluginStorePlatforms(platforms []pluginstore.Platform) []pluginStorePlatform {
	if len(platforms) == 0 {
		return nil
	}
	out := make([]pluginStorePlatform, 0, len(platforms))
	for _, platform := range platforms {
		out = append(out, pluginStorePlatform{
			GOOS:   htmlsanitize.String(platform.GOOS),
			GOARCH: htmlsanitize.String(platform.GOARCH),
		})
	}
	return out
}

func pluginAuthConfigured(source pluginstore.Source, plugin pluginstore.Plugin, storeAuth []pluginstore.AuthConfig) bool {
	return pluginstore.PluginAuthConfigured(source, plugin, storeAuth)
}

// latestPluginVersions resolves the latest release version of each registry
// plugin concurrently, returning results positionally aligned with plugins.
// Unresolved entries are left empty so callers can fall back gracefully.
func (h *Handler) latestPluginVersions(ctx context.Context, client pluginstore.Client, plugins []pluginstore.Plugin) []string {
	versions := make([]string, len(plugins))
	var wg sync.WaitGroup
	for index := range plugins {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			versions[index] = h.latestPluginVersion(ctx, client, plugins[index])
		}(index)
	}
	wg.Wait()
	return versions
}

// latestPluginVersion returns the plugin's latest release version, caching
// lookups per repository so repeated listings do not exhaust the GitHub API
// rate limit. Failed lookups are cached for a shorter interval and reported
// as an empty version.
func (h *Handler) latestPluginVersion(ctx context.Context, client pluginstore.Client, plugin pluginstore.Plugin) string {
	if pluginstore.PluginInstallType(plugin) != pluginstore.InstallTypeGitHubRelease {
		return ""
	}
	repository := strings.TrimSpace(plugin.Repository)
	if repository == "" {
		return ""
	}
	now := time.Now()
	h.pluginReleaseCacheMu.Lock()
	entry, found := h.pluginReleaseCache[repository]
	h.pluginReleaseCacheMu.Unlock()
	if found && now.Before(entry.expiresAt) {
		return entry.version
	}

	version := ""
	ttl := pluginReleaseFailureCacheTTL
	release, errRelease := client.FetchLatestRelease(ctx, plugin)
	if errRelease != nil {
		log.WithError(errRelease).WithField("plugin_id", plugin.ID).Warn("pluginstore: failed to fetch latest release")
	} else if latestVersion, errVersion := pluginstore.ReleaseVersion(release); errVersion != nil {
		log.WithError(errVersion).WithField("plugin_id", plugin.ID).Warn("pluginstore: invalid latest release tag")
	} else {
		version = latestVersion
		ttl = pluginReleaseCacheTTL
	}

	h.pluginReleaseCacheMu.Lock()
	if h.pluginReleaseCache == nil {
		h.pluginReleaseCache = make(map[string]pluginReleaseCacheEntry)
	}
	h.pluginReleaseCache[repository] = pluginReleaseCacheEntry{version: version, expiresAt: now.Add(ttl)}
	h.pluginReleaseCacheMu.Unlock()
	return version
}

func pluginLocalStatuses(pluginsEnabled bool, pluginsDir string, configs map[string]config.PluginInstanceConfig, host *pluginhost.Host) (map[string]pluginLocalStatus, error) {
	statuses := map[string]pluginLocalStatus{}
	files, errDiscover := pluginhost.DiscoverPluginFiles(pluginsDir, pluginStoreDesiredVersions(configs))
	if errDiscover != nil {
		return nil, errDiscover
	}
	for _, file := range files {
		status := statuses[file.ID]
		status.Installed = true
		status.Path = file.Path
		if strings.TrimSpace(file.Version) != "" {
			status.InstalledVersion = strings.TrimSpace(file.Version)
		}
		status.Enabled = true
		statuses[file.ID] = status
	}
	for id, item := range configs {
		status := statuses[id]
		status.Configured = true
		status.Enabled = pluginInstanceEnabled(item)
		statuses[id] = status
	}
	if host != nil {
		for _, info := range host.RegisteredPlugins() {
			status := statuses[info.ID]
			status.Installed = true
			status.Registered = true
			status.InstalledVersion = strings.TrimSpace(info.Metadata.Version)
			if _, configured := configs[info.ID]; !configured && !status.Enabled {
				status.Enabled = false
			}
			statuses[info.ID] = status
		}
	}
	for id, status := range statuses {
		status.EffectiveEnabled = pluginsEnabled && status.Enabled && status.Registered
		statuses[id] = status
	}
	return statuses, nil
}

func pluginStoreDesiredVersions(configs map[string]config.PluginInstanceConfig) map[string]string {
	if len(configs) == 0 {
		return nil
	}
	out := make(map[string]string, len(configs))
	for id, item := range configs {
		id = strings.TrimSpace(id)
		version := pluginStoreDesiredVersion(item)
		if id == "" || version == "" {
			continue
		}
		out[id] = version
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func pluginStoreDesiredVersion(item config.PluginInstanceConfig) string {
	storeNode := pluginStoreConfigNode(item)
	if storeNode == nil {
		return ""
	}
	if version := pluginStoreNormalizeDesiredVersion(pluginStoreYAMLScalar(yamlMappingValue(storeNode, "version"))); version != "" {
		return version
	}
	return pluginStoreNormalizeDesiredVersion(pluginStoreYAMLScalar(yamlMappingValue(storeNode, "release-tag")))
}

func pluginStoreConfigNode(item config.PluginInstanceConfig) *yaml.Node {
	if item.Raw.Kind != yaml.MappingNode {
		return nil
	}
	return yamlMappingValue(&item.Raw, "store")
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

func pluginStoreYAMLScalar(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

func pluginStoreNormalizeDesiredVersion(version string) string {
	version = strings.TrimSpace(version)
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') {
		version = version[1:]
	}
	if version == "" || version[0] < '0' || version[0] > '9' {
		return ""
	}
	return version
}

func pluginBusy(host *pluginhost.Host, id string) bool {
	if host == nil {
		return false
	}
	return host.PluginBusy(id)
}
