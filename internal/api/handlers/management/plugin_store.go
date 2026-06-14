package management

import (
	"context"
	"errors"
	"fmt"
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
	StoreID          string   `json:"store_id"`
	SourceID         string   `json:"source_id"`
	SourceName       string   `json:"source_name"`
	SourceURL        string   `json:"source_url"`
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Author           string   `json:"author"`
	Version          string   `json:"version"`
	Repository       string   `json:"repository"`
	Logo             string   `json:"logo,omitempty"`
	Homepage         string   `json:"homepage,omitempty"`
	License          string   `json:"license,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Installed        bool     `json:"installed"`
	InstalledVersion string   `json:"installed_version"`
	Path             string   `json:"path"`
	Configured       bool     `json:"configured"`
	Registered       bool     `json:"registered"`
	Enabled          bool     `json:"enabled"`
	EffectiveEnabled bool     `json:"effective_enabled"`
	UpdateAvailable  bool     `json:"update_available"`
}

type pluginInstallResponse struct {
	Status          string `json:"status"`
	SourceID        string `json:"source_id"`
	SourceName      string `json:"source_name"`
	SourceURL       string `json:"source_url"`
	ID              string `json:"id"`
	Version         string `json:"version"`
	Path            string `json:"path"`
	PluginsEnabled  bool   `json:"plugins_enabled"`
	RestartRequired bool   `json:"restart_required"`
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
	pluginsEnabled, pluginsDir, proxyURL, sourceConfigs, configs, host := h.pluginStoreSnapshot()
	sources, errSources := h.pluginStoreSources(sourceConfigs)
	if errSources != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_store_source_invalid", "message": errSources.Error()})
		return
	}
	plugins, sourceErrors := h.fetchSourcedPlugins(c.Request.Context(), proxyURL, sources)
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
	client := h.newPluginStoreClient(proxyURL, "")
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
	installCtx := c.Request.Context()
	pluginsEnabled, pluginsDir, proxyURL, sourceConfigs, _, host := h.pluginStoreSnapshot()
	sources, errSources := h.pluginStoreSources(sourceConfigs)
	if errSources != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_store_source_invalid", "message": errSources.Error()})
		return
	}
	source, plugin, client, okPlugin := h.findPluginStoreInstallTarget(installCtx, proxyURL, sources, id, c.Query("source"), c)
	if !okPlugin {
		return
	}

	pluginIsLoaded := func() bool { return pluginLoaded(host, id) }
	unloadedBeforeWrite := false
	result, errInstall := client.Install(installCtx, plugin, pluginstore.InstallOptions{
		PluginsDir:   pluginsDir,
		GOOS:         goos,
		GOARCH:       goarch,
		PluginLoaded: pluginIsLoaded,
		BeforeWrite: func() error {
			if !pluginIsLoaded() {
				return nil
			}
			if host == nil {
				return pluginstore.ErrLoadedPluginLocked
			}
			log.WithFields(log.Fields{
				"plugin_id": id,
				"version":   plugin.Version,
			}).Info("pluginstore: unloading loaded plugin before install")
			if !host.UnloadPlugin(id) && pluginIsLoaded() {
				return pluginstore.ErrLoadedPluginLocked
			}
			unloadedBeforeWrite = true
			return nil
		},
	})
	if errInstall != nil {
		if unloadedBeforeWrite {
			h.mu.Lock()
			reloadCfg := h.cfg
			h.mu.Unlock()
			h.reloadConfigAfterManagementSave(c.Request.Context(), reloadCfg)
		}
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
	if errEnable := h.enablePluginConfigLocked(id); errEnable != nil {
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
	reloadCfg := h.cfg
	h.mu.Unlock()

	h.reloadConfigAfterManagementSave(c.Request.Context(), reloadCfg)
	log.WithFields(log.Fields{
		"plugin_id":   result.ID,
		"source_id":   source.ID,
		"version":     result.Version,
		"path":        result.Path,
		"overwritten": result.Overwritten,
	}).Info("pluginstore: plugin installed")

	c.JSON(http.StatusOK, pluginInstallResponse{
		Status:          "installed",
		SourceID:        htmlsanitize.String(source.ID),
		SourceName:      htmlsanitize.String(source.Name),
		SourceURL:       htmlsanitize.String(source.URL),
		ID:              htmlsanitize.String(result.ID),
		Version:         htmlsanitize.String(result.Version),
		Path:            htmlsanitize.String(result.Path),
		PluginsEnabled:  pluginsEnabled,
		RestartRequired: restartRequired,
	})
}

// enablePluginConfigLocked sets plugins.configs.<id>.enabled to true while preserving
// the rest of the plugin's raw configuration. Callers must hold h.mu.
func (h *Handler) enablePluginConfigLocked(id string) error {
	ensurePluginConfigMap(h.cfg)
	node := pluginConfigNode(h.cfg.Plugins.Configs[id])
	setYAMLMappingValue(node, "enabled", boolYAMLNode(true))
	updated, errConfig := pluginInstanceConfigFromNode(node)
	if errConfig != nil {
		return fmt.Errorf("decode plugin config: %w", errConfig)
	}
	h.cfg.Plugins.Configs[id] = updated
	return nil
}

func (h *Handler) pluginStoreSnapshot() (bool, string, string, []string, map[string]config.PluginInstanceConfig, *pluginhost.Host) {
	if h == nil || h.cfg == nil {
		return false, "plugins", "", nil, map[string]config.PluginInstanceConfig{}, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	pluginsEnabled := h.cfg.Plugins.Enabled
	pluginsDir := normalizedPluginsDir(h.cfg.Plugins.Dir)
	proxyURL := strings.TrimSpace(h.cfg.ProxyURL)
	sourceConfigs := append([]string(nil), h.cfg.Plugins.StoreSources...)
	configs := make(map[string]config.PluginInstanceConfig, len(h.cfg.Plugins.Configs))
	for id, item := range h.cfg.Plugins.Configs {
		configs[id] = item
	}
	return pluginsEnabled, pluginsDir, proxyURL, sourceConfigs, configs, h.pluginHost
}

func (h *Handler) pluginStoreSources(sourceConfigs []string) ([]pluginstore.Source, error) {
	if h != nil && strings.TrimSpace(h.pluginStoreRegistryURL) != "" {
		source := pluginstore.DefaultSource()
		source.URL = strings.TrimSpace(h.pluginStoreRegistryURL)
		return []pluginstore.Source{source}, nil
	}
	return pluginstore.NormalizeSources(sourceConfigs)
}

func (h *Handler) newPluginStoreClient(proxyURL string, registryURL string) pluginstore.Client {
	registryURL = strings.TrimSpace(registryURL)
	var httpClient pluginstore.HTTPDoer
	if h != nil {
		httpClient = h.pluginStoreHTTPClient
	}
	if registryURL == "" {
		registryURL = pluginstore.DefaultRegistryURL
	}
	if httpClient != nil {
		return pluginstore.Client{HTTPClient: httpClient, RegistryURL: registryURL}
	}
	client := &http.Client{}
	if strings.TrimSpace(proxyURL) != "" {
		util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: strings.TrimSpace(proxyURL)}, client)
	}
	return pluginstore.Client{HTTPClient: client, RegistryURL: registryURL}
}

func (h *Handler) fetchSourcedPlugins(ctx context.Context, proxyURL string, sources []pluginstore.Source) ([]sourcedPlugin, []pluginStoreSourceErr) {
	plugins := make([]sourcedPlugin, 0)
	sourceErrors := make([]pluginStoreSourceErr, 0)
	for _, source := range sources {
		client := h.newPluginStoreClient(proxyURL, source.URL)
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

func (h *Handler) findPluginStoreInstallTarget(ctx context.Context, proxyURL string, sources []pluginstore.Source, id string, requestedSourceID string, c *gin.Context) (pluginstore.Source, pluginstore.Plugin, pluginstore.Client, bool) {
	requestedSourceID = strings.TrimSpace(requestedSourceID)
	if requestedSourceID != "" {
		for _, source := range sources {
			if source.ID != requestedSourceID {
				continue
			}
			client := h.newPluginStoreClient(proxyURL, source.URL)
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

	plugins, sourceErrors := h.fetchSourcedPlugins(ctx, proxyURL, sources)
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
	return match.source, match.plugin, h.newPluginStoreClient(proxyURL, match.source.URL), true
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
	files, errDiscover := pluginhost.DiscoverPluginFiles(pluginsDir)
	if errDiscover != nil {
		return nil, errDiscover
	}
	for _, file := range files {
		status := statuses[file.ID]
		status.Installed = true
		status.Path = file.Path
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
				status.Enabled = true
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

func pluginLoaded(host *pluginhost.Host, id string) bool {
	if host == nil {
		return false
	}
	return host.PluginLoaded(id)
}
