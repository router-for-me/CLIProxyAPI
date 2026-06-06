package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type pluginListResponse struct {
	PluginsEnabled bool              `json:"plugins_enabled"`
	PluginsDir     string            `json:"plugins_dir"`
	Plugins        []pluginListEntry `json:"plugins"`
}

type pluginListEntry struct {
	ID               string                  `json:"id"`
	Path             string                  `json:"path"`
	Configured       bool                    `json:"configured"`
	Registered       bool                    `json:"registered"`
	Enabled          bool                    `json:"enabled"`
	EffectiveEnabled bool                    `json:"effective_enabled"`
	SupportsOAuth    bool                    `json:"supports_oauth"`
	Logo             string                  `json:"logo"`
	ConfigFields     []pluginConfigFieldInfo `json:"config_fields"`
	Menus            []pluginMenuInfo        `json:"menus"`
	Metadata         *pluginMetadataInfo     `json:"metadata"`
}

type pluginMetadataInfo struct {
	Name             string                  `json:"name"`
	Version          string                  `json:"version"`
	Author           string                  `json:"author"`
	GitHubRepository string                  `json:"github_repository"`
	Logo             string                  `json:"logo"`
	ConfigFields     []pluginConfigFieldInfo `json:"config_fields"`
}

type pluginConfigFieldInfo struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	EnumValues  []string `json:"enum_values"`
	Description string   `json:"description"`
}

type pluginMenuInfo struct {
	Path        string `json:"path"`
	Menu        string `json:"menu"`
	Description string `json:"description"`
}

// ListPlugins returns discovered, configured, and registered plugin entries.
func (h *Handler) ListPlugins(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusOK, pluginListResponse{
			PluginsDir: "plugins",
			Plugins:    []pluginListEntry{},
		})
		return
	}

	h.mu.Lock()
	pluginsEnabled := h.cfg.Plugins.Enabled
	pluginsDir := normalizedPluginsDir(h.cfg.Plugins.Dir)
	configs := make(map[string]config.PluginInstanceConfig, len(h.cfg.Plugins.Configs))
	for id, item := range h.cfg.Plugins.Configs {
		configs[id] = item
	}
	host := h.pluginHost
	h.mu.Unlock()

	entries := make(map[string]pluginListEntry)
	files, errDiscover := pluginhost.DiscoverPluginFiles(pluginsDir)
	if errDiscover != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_discovery_failed", "message": errDiscover.Error()})
		return
	}
	for _, file := range files {
		entries[file.ID] = pluginListEntry{
			ID:           file.ID,
			Path:         file.Path,
			Enabled:      true,
			ConfigFields: []pluginConfigFieldInfo{},
			Menus:        []pluginMenuInfo{},
		}
	}
	for id, item := range configs {
		entry := entries[id]
		entry.ID = id
		entry.Configured = true
		entry.Enabled = pluginInstanceEnabled(item)
		if entry.ConfigFields == nil {
			entry.ConfigFields = []pluginConfigFieldInfo{}
		}
		if entry.Menus == nil {
			entry.Menus = []pluginMenuInfo{}
		}
		entries[id] = entry
	}
	if host != nil {
		for _, info := range host.RegisteredPlugins() {
			entry := entries[info.ID]
			entry.ID = info.ID
			entry.Registered = true
			entry.SupportsOAuth = info.SupportsOAuth
			entry.Logo = info.Metadata.Logo
			entry.ConfigFields = pluginConfigFields(info.Metadata.ConfigFields)
			entry.Menus = pluginMenus(info.Menus)
			entry.Metadata = pluginMetadata(info.Metadata)
			_, configured := configs[info.ID]
			if !configured && !entry.Enabled {
				entry.Enabled = true
			}
			entries[info.ID] = entry
		}
	}

	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]pluginListEntry, 0, len(ids))
	for _, id := range ids {
		entry := entries[id]
		entry.EffectiveEnabled = pluginsEnabled && entry.Enabled && entry.Registered
		if entry.ConfigFields == nil {
			entry.ConfigFields = []pluginConfigFieldInfo{}
		}
		if entry.Menus == nil {
			entry.Menus = []pluginMenuInfo{}
		}
		out = append(out, entry)
	}

	c.JSON(http.StatusOK, pluginListResponse{
		PluginsEnabled: pluginsEnabled,
		PluginsDir:     pluginsDir,
		Plugins:        out,
	})
}

// PatchPluginEnabled updates plugins.configs.<id>.enabled without touching plugins.enabled.
func (h *Handler) PatchPluginEnabled(c *gin.Context) {
	id, okID := pluginIDFromRequest(c)
	if !okID {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil || body.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": "enabled is required"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	ensurePluginConfigMap(h.cfg)
	item := h.cfg.Plugins.Configs[id]
	node := pluginConfigNode(item)
	setYAMLMappingValue(node, "enabled", boolYAMLNode(*body.Enabled))
	updated, errConfig := pluginInstanceConfigFromNode(node)
	if errConfig != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": errConfig.Error()})
		return
	}
	h.cfg.Plugins.Configs[id] = updated
	h.persistLocked(c)
}

// PutPluginConfig replaces plugins.configs.<id> with the request object.
func (h *Handler) PutPluginConfig(c *gin.Context) {
	id, okID := pluginIDFromRequest(c)
	if !okID {
		return
	}
	body, okBody := readPluginConfigObject(c)
	if !okBody {
		return
	}
	node, errNode := yamlNodeFromJSONObject(body)
	if errNode != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": errNode.Error()})
		return
	}
	updated, errConfig := pluginInstanceConfigFromNode(node)
	if errConfig != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": errConfig.Error()})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	ensurePluginConfigMap(h.cfg)
	h.cfg.Plugins.Configs[id] = updated
	h.persistLocked(c)
}

// PatchPluginConfig shallow-merges plugins.configs.<id> with the request object.
func (h *Handler) PatchPluginConfig(c *gin.Context) {
	id, okID := pluginIDFromRequest(c)
	if !okID {
		return
	}
	body, okBody := readPluginConfigObject(c)
	if !okBody {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	ensurePluginConfigMap(h.cfg)
	node := pluginConfigNode(h.cfg.Plugins.Configs[id])
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := body[key]
		if value == nil {
			deleteYAMLMappingKey(node, key)
			continue
		}
		valueNode, errNode := yamlNodeFromJSONValue(value)
		if errNode != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": errNode.Error()})
			return
		}
		setYAMLMappingValue(node, key, valueNode)
	}
	updated, errConfig := pluginInstanceConfigFromNode(node)
	if errConfig != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": errConfig.Error()})
		return
	}
	h.cfg.Plugins.Configs[id] = updated
	h.persistLocked(c)
}

func normalizedPluginsDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "plugins"
	}
	return dir
}

func pluginInstanceEnabled(item config.PluginInstanceConfig) bool {
	if item.Enabled == nil {
		return true
	}
	return *item.Enabled
}

func pluginConfigFields(fields []pluginapi.ConfigField) []pluginConfigFieldInfo {
	out := make([]pluginConfigFieldInfo, 0, len(fields))
	for _, field := range fields {
		enumValues := append([]string{}, field.EnumValues...)
		out = append(out, pluginConfigFieldInfo{
			Name:        field.Name,
			Type:        string(field.Type),
			EnumValues:  enumValues,
			Description: field.Description,
		})
	}
	return out
}

func pluginMenus(menus []pluginhost.RegisteredPluginMenu) []pluginMenuInfo {
	out := make([]pluginMenuInfo, 0, len(menus))
	for _, menu := range menus {
		out = append(out, pluginMenuInfo{
			Path:        menu.Path,
			Menu:        menu.Menu,
			Description: menu.Description,
		})
	}
	return out
}

func pluginMetadata(meta pluginapi.Metadata) *pluginMetadataInfo {
	return &pluginMetadataInfo{
		Name:             meta.Name,
		Version:          meta.Version,
		Author:           meta.Author,
		GitHubRepository: meta.GitHubRepository,
		Logo:             meta.Logo,
		ConfigFields:     pluginConfigFields(meta.ConfigFields),
	}
}

func pluginIDFromRequest(c *gin.Context) (string, bool) {
	id := strings.TrimSpace(c.Param("id"))
	if !pluginhost.ValidatePluginID(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_plugin_id", "message": "invalid plugin id"})
		return "", false
	}
	return id, true
}

func readPluginConfigObject(c *gin.Context) (map[string]any, bool) {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.UseNumber()
	var body map[string]any
	if errDecode := decoder.Decode(&body); errDecode != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": errDecode.Error()})
		return nil, false
	}
	if body == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": "body must be a JSON object"})
		return nil, false
	}
	return body, true
}

func ensurePluginConfigMap(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.NormalizePluginsConfig()
}

func pluginConfigNode(item config.PluginInstanceConfig) *yaml.Node {
	if item.Raw.Kind == yaml.MappingNode {
		return cloneYAMLNode(&item.Raw)
	}
	node := emptyYAMLMappingNode()
	if item.Enabled != nil {
		setYAMLMappingValue(node, "enabled", boolYAMLNode(*item.Enabled))
	}
	if item.Priority != 0 {
		setYAMLMappingValue(node, "priority", intYAMLNode(item.Priority))
	}
	return node
}

func pluginInstanceConfigFromNode(node *yaml.Node) (config.PluginInstanceConfig, error) {
	if node == nil {
		node = emptyYAMLMappingNode()
	}
	var item config.PluginInstanceConfig
	if errDecode := node.Decode(&item); errDecode != nil {
		return config.PluginInstanceConfig{}, errDecode
	}
	return item, nil
}

func yamlNodeFromJSONObject(body map[string]any) (*yaml.Node, error) {
	node := emptyYAMLMappingNode()
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		valueNode, errNode := yamlNodeFromJSONValue(body[key])
		if errNode != nil {
			return nil, fmt.Errorf("%s: %w", key, errNode)
		}
		setYAMLMappingValue(node, key, valueNode)
	}
	return node, nil
}

func yamlNodeFromJSONValue(value any) (*yaml.Node, error) {
	switch typed := value.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: typed}, nil
	case bool:
		return boolYAMLNode(typed), nil
	case json.Number:
		if _, errInt64 := typed.Int64(); errInt64 == nil {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: typed.String()}, nil
		}
		if _, errFloat64 := typed.Float64(); errFloat64 == nil {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: typed.String()}, nil
		}
		return nil, fmt.Errorf("invalid number %q", typed.String())
	case float64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(typed, 'f', -1, 64)}, nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range typed {
			child, errChild := yamlNodeFromJSONValue(item)
			if errChild != nil {
				return nil, errChild
			}
			node.Content = append(node.Content, child)
		}
		return node, nil
	case map[string]any:
		return yamlNodeFromJSONObject(typed)
	default:
		return nil, fmt.Errorf("unsupported value type %T", value)
	}
}

func emptyYAMLMappingNode() *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
}

func boolYAMLNode(value bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(value)}
}

func intYAMLNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}
}

func setYAMLMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	if mapping.Kind != yaml.MappingNode {
		*mapping = *emptyYAMLMappingNode()
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index] != nil && mapping.Content[index].Value == key {
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func deleteYAMLMappingKey(mapping *yaml.Node, key string) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index] != nil && mapping.Content[index].Value == key {
			mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
			return
		}
	}
}

func cloneYAMLNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	out := *node
	if len(node.Content) > 0 {
		out.Content = make([]*yaml.Node, 0, len(node.Content))
		for _, child := range node.Content {
			out.Content = append(out.Content, cloneYAMLNode(child))
		}
	}
	return &out
}
