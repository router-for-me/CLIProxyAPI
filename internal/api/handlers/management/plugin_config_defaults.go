package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// Management clients render plugins.configs.<id> as read from the v8 config
// API and write the edited object back whole. Keys the file omits therefore
// look blank or switched off, and are saved that way, even when the effective
// config differs: the bundled guard's entry is synthesized in memory, so its
// enabled flag is not on disk, and the plugin applies its own defaults to
// omitted fields. The helpers below fill omitted keys in the GET response of
// plugins/configs/<id> from the effective enabled/priority and from the
// defaults the registered plugin publishes (pluginapi.ConfigField Default).
// The config file itself is not changed by a read.

// pluginConfigReadDefaults returns the values to show for omitted keys when c
// reads plugins/configs/<id> through ConfigV8, or nil. It runs before ConfigV8
// takes h.mu for the request because reading plugin metadata may call into
// plugins.
func (h *Handler) pluginConfigReadDefaults(c *gin.Context) map[string]any {
	if h == nil || c == nil || c.Request == nil || c.Request.Method != http.MethodGet {
		return nil
	}
	parts := strings.Split(strings.Trim(c.Param("path"), "/"), "/")
	if len(parts) != 3 || parts[0] != "plugins" || parts[1] != "configs" || parts[2] == "" {
		return nil
	}
	id := parts[2]
	defaults := map[string]any{}
	h.mu.Lock()
	if h.cfg != nil {
		if item, ok := h.cfg.Plugins.Configs[id]; ok {
			defaults["enabled"] = pluginInstanceEnabled(item)
			defaults["priority"] = item.Priority
		}
	}
	host := h.pluginHost
	h.mu.Unlock()
	if host != nil {
		for _, info := range host.RegisteredPlugins() {
			if info.ID != id {
				continue
			}
			for _, field := range info.Metadata.ConfigFields {
				if field.Name != "" && field.Default != nil {
					if _, reserved := defaults[field.Name]; !reserved {
						defaults[field.Name] = field.Default
					}
				}
			}
			break
		}
	}
	if len(defaults) == 0 {
		return nil
	}
	return defaults
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
