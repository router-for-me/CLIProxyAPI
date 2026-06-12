package management

import (
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/htmlsanitize"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
)

type pluginStoreListResponse struct {
	PluginsEnabled bool                   `json:"plugins_enabled"`
	PluginsDir     string                 `json:"plugins_dir"`
	Plugins        []pluginStoreListEntry `json:"plugins"`
}

type pluginStoreListEntry struct {
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

func (h *Handler) ListPluginStore(c *gin.Context) {
	pluginsEnabled, pluginsDir, proxyURL, configs, host := h.pluginStoreSnapshot()
	client := h.newPluginStoreClient(proxyURL)
	registry, errRegistry := client.FetchRegistry(c.Request.Context())
	if errRegistry != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_store_registry_failed", "message": errRegistry.Error()})
		return
	}
	statuses, errStatus := pluginLocalStatuses(pluginsEnabled, pluginsDir, configs, host)
	if errStatus != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_discovery_failed", "message": errStatus.Error()})
		return
	}

	entries := make([]pluginStoreListEntry, 0, len(registry.Plugins))
	for _, plugin := range registry.Plugins {
		status := statuses[plugin.ID]
		installedVersion := status.InstalledVersion
		entries = append(entries, pluginStoreListEntry{
			ID:               htmlsanitize.String(plugin.ID),
			Name:             htmlsanitize.String(plugin.Name),
			Description:      htmlsanitize.String(plugin.Description),
			Author:           htmlsanitize.String(plugin.Author),
			Version:          htmlsanitize.String(plugin.Version),
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
			UpdateAvailable:  pluginstore.UpdateAvailable(installedVersion, plugin.Version),
		})
	}

	c.JSON(http.StatusOK, pluginStoreListResponse{
		PluginsEnabled: pluginsEnabled,
		PluginsDir:     htmlsanitize.String(pluginsDir),
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
	pluginsEnabled, pluginsDir, proxyURL, _, host := h.pluginStoreSnapshot()
	client := h.newPluginStoreClient(proxyURL)
	registry, errRegistry := client.FetchRegistry(c.Request.Context())
	if errRegistry != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_store_registry_failed", "message": errRegistry.Error()})
		return
	}
	plugin, okPlugin := registry.PluginByID(id)
	if !okPlugin {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_not_found", "message": "plugin not found in registry"})
		return
	}

	pluginIsLoaded := func() bool { return pluginLoaded(host, id) }
	unloadedBeforeWrite := false
	result, errInstall := client.Install(c.Request.Context(), plugin, pluginstore.InstallOptions{
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
		"version":     result.Version,
		"path":        result.Path,
		"overwritten": result.Overwritten,
	}).Info("pluginstore: plugin installed")

	c.JSON(http.StatusOK, pluginInstallResponse{
		Status:          "installed",
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

func (h *Handler) pluginStoreSnapshot() (bool, string, string, map[string]config.PluginInstanceConfig, *pluginhost.Host) {
	if h == nil || h.cfg == nil {
		return false, "plugins", "", map[string]config.PluginInstanceConfig{}, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	pluginsEnabled := h.cfg.Plugins.Enabled
	pluginsDir := normalizedPluginsDir(h.cfg.Plugins.Dir)
	proxyURL := strings.TrimSpace(h.cfg.ProxyURL)
	configs := make(map[string]config.PluginInstanceConfig, len(h.cfg.Plugins.Configs))
	for id, item := range h.cfg.Plugins.Configs {
		configs[id] = item
	}
	return pluginsEnabled, pluginsDir, proxyURL, configs, h.pluginHost
}

func (h *Handler) newPluginStoreClient(proxyURL string) pluginstore.Client {
	registryURL := ""
	var httpClient pluginstore.HTTPDoer
	if h != nil {
		registryURL = strings.TrimSpace(h.pluginStoreRegistryURL)
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
