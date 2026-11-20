package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// Management clients render plugins.configs.<id> as read from the v8 config
// API, so keys the config omits look blank or switched off even when the
// plugin applies a different default. The two helpers below fill those keys
// from the defaults the registered plugin publishes (pluginapi.ConfigField
// Default) in the GET response only; the config file is not changed.

// pluginConfigFieldDefaults returns the published field defaults of the
// registered plugin when c reads plugins/configs/<id> through ConfigV8, or nil.
// It runs before ConfigV8 takes h.mu because reading plugin metadata may call
// into plugins.
func (h *Handler) pluginConfigFieldDefaults(c *gin.Context) map[string]any {
	if h == nil || c == nil || c.Request == nil || c.Request.Method != http.MethodGet {
		return nil
	}
	parts := strings.Split(strings.Trim(c.Param("path"), "/"), "/")
	if len(parts) != 3 || parts[0] != "plugins" || parts[1] != "configs" || parts[2] == "" {
		return nil
	}
	host := h.pluginHostSnapshot()
	if host == nil {
		return nil
	}
	for _, info := range host.RegisteredPlugins() {
		if info.ID != parts[2] {
			continue
		}
		defaults := make(map[string]any, len(info.Metadata.ConfigFields))
		for _, field := range info.Metadata.ConfigFields {
			if field.Name != "" && field.Default != nil {
				defaults[field.Name] = field.Default
			}
		}
		return defaults
	}
	return nil
}

// withPluginConfigDefaults adds each default whose key value omits and returns
// the result. A missing plugin entry is returned as its defaults.
func withPluginConfigDefaults(value *yaml.Node, defaults map[string]any) *yaml.Node {
	if len(defaults) == 0 {
		return value
	}
	if value == nil {
		value = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if value.Kind != yaml.MappingNode {
		return value
	}
	names := make([]string, 0, len(defaults))
	for name := range defaults {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if configV8Node(value, []string{name}) != nil {
			continue
		}
		var node yaml.Node
		if err := node.Encode(defaults[name]); err != nil {
			continue
		}
		value.Content = append(value.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, &node)
	}
	return value
}
