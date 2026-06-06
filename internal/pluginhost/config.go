package pluginhost

import (
	"bytes"
	"sort"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

var defaultRuntimeConfigYAML = []byte("enabled: true\npriority: 0\n")

type runtimeConfig struct {
	Enabled bool
	Dir     string
	Items   map[string]runtimeItemConfig
}

type runtimeItemConfig struct {
	ID         string
	Enabled    bool
	Priority   int
	ConfigYAML []byte
}

func runtimeConfigFromConfig(cfg *config.Config) runtimeConfig {
	out := runtimeConfig{
		Dir:   "plugins",
		Items: make(map[string]runtimeItemConfig),
	}
	if cfg == nil {
		return out
	}

	out.Enabled = cfg.Plugins.Enabled
	out.Dir = strings.TrimSpace(cfg.Plugins.Dir)
	if out.Dir == "" {
		out.Dir = "plugins"
	}

	ids := make([]string, 0, len(cfg.Plugins.Configs))
	for id := range cfg.Plugins.Configs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		item := cfg.Plugins.Configs[id]
		enabled := true
		if item.Enabled != nil {
			enabled = *item.Enabled
		}

		out.Items[id] = runtimeItemConfig{
			ID:         id,
			Enabled:    enabled,
			Priority:   item.Priority,
			ConfigYAML: runtimeConfigYAML(item, enabled),
		}
	}
	return out
}

func defaultRuntimeItemConfig(id string) runtimeItemConfig {
	return runtimeItemConfig{
		ID:         id,
		Enabled:    true,
		Priority:   0,
		ConfigYAML: append([]byte(nil), defaultRuntimeConfigYAML...),
	}
}

func runtimeConfigYAML(item config.PluginInstanceConfig, enabled bool) []byte {
	rawNode := normalizedConfigNode(item, enabled)
	rawYAML := bytes.TrimSpace(mustMarshalYAML(rawNode))
	if len(rawYAML) == 0 {
		return append([]byte(nil), defaultRuntimeConfigYAML...)
	}
	return append(append([]byte(nil), rawYAML...), '\n')
}

func normalizedConfigNode(item config.PluginInstanceConfig, enabled bool) *yaml.Node {
	if item.Raw.Kind == 0 {
		return defaultRuntimeConfigNode(enabled, item.Priority)
	}
	node := deepCopyYAMLNode(&item.Raw)
	if node.Kind != yaml.MappingNode {
		return node
	}
	ensureMappingScalar(node, "enabled", boolYAMLValue(enabled), "!!bool")
	ensureMappingScalar(node, "priority", intYAMLValue(item.Priority), "!!int")
	return node
}

func defaultRuntimeConfigNode(enabled bool, priority int) *yaml.Node {
	return &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "enabled"},
			{Kind: yaml.ScalarNode, Tag: "!!bool", Value: boolYAMLValue(enabled)},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "priority"},
			{Kind: yaml.ScalarNode, Tag: "!!int", Value: intYAMLValue(priority)},
		},
	}
}

func ensureMappingScalar(node *yaml.Node, key, value, tag string) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i] != nil && node.Content[i].Value == key {
			return
		}
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value},
	)
}

func boolYAMLValue(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func intYAMLValue(v int) string {
	return strconv.Itoa(v)
}

func deepCopyYAMLNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	copyNode := *node
	if len(node.Content) > 0 {
		copyNode.Content = make([]*yaml.Node, 0, len(node.Content))
		for _, child := range node.Content {
			copyNode.Content = append(copyNode.Content, deepCopyYAMLNode(child))
		}
	}
	return &copyNode
}

func mustMarshalYAML(v any) []byte {
	raw, errMarshal := yaml.Marshal(v)
	if errMarshal != nil {
		return append([]byte(nil), defaultRuntimeConfigYAML...)
	}
	return raw
}
