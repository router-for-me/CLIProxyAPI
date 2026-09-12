package pluginhost

import (
	"bytes"
	"sort"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

var defaultRuntimeConfigYAML = []byte("enabled: false\npriority: 0\n")

type runtimeConfig struct {
	Enabled bool
	Dir     string
	Items   map[string]runtimeItemConfig
}

type runtimeItemConfig struct {
	ID         string
	Enabled    bool
	Priority   int
	Version    string
	ConfigYAML []byte
}

func runtimeConfigFromConfig(cfg *config.Config) (runtimeConfig, error) {
	out := runtimeConfig{
		Dir:   "plugins",
		Items: make(map[string]runtimeItemConfig),
	}
	if cfg == nil {
		return out, nil
	}

	out.Enabled = cfg.Plugins.Enabled
	if !out.Enabled {
		return out, nil
	}
	pluginsDir, errResolvePluginsDir := config.ResolvePluginsDir(cfg.Plugins.Dir)
	if errResolvePluginsDir != nil {
		return runtimeConfig{}, errResolvePluginsDir
	}
	out.Dir = pluginsDir

	ids := make([]string, 0, len(cfg.Plugins.Configs))
	for id := range cfg.Plugins.Configs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		item := cfg.Plugins.Configs[id]
		enabled := false
		if item.Enabled != nil {
			enabled = *item.Enabled
		}

		out.Items[id] = runtimeItemConfig{
			ID:         id,
			Enabled:    enabled,
			Priority:   item.Priority,
			Version:    pluginConfigDesiredVersion(item),
			ConfigYAML: runtimeConfigYAML(item, enabled),
		}
	}
	return out, nil
}

func defaultRuntimeItemConfig(id string) runtimeItemConfig {
	return runtimeItemConfig{
		ID:         id,
		Enabled:    false,
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

func desiredPluginVersions(items map[string]runtimeItemConfig) map[string]string {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]string, len(items))
	for id, item := range items {
		id = strings.TrimSpace(id)
		version := strings.TrimSpace(item.Version)
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

func pluginConfigDesiredVersion(item config.PluginInstanceConfig) string {
	storeNode := yamlMappingValue(&item.Raw, "store")
	if storeNode == nil {
		return ""
	}
	if version := normalizePluginDesiredVersion(yamlScalarString(yamlMappingValue(storeNode, "version"))); version != "" {
		return version
	}
	return normalizePluginDesiredVersion(yamlScalarString(yamlMappingValue(storeNode, "release-tag")))
}

func normalizePluginDesiredVersion(version string) string {
	version = strings.TrimSpace(version)
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') {
		version = version[1:]
	}
	if !validPluginVersion(version) {
		return ""
	}
	return version
}

func yamlScalarString(node *yaml.Node) string {
	if node == nil || node.Kind == 0 {
		return ""
	}
	if node.Kind == yaml.ScalarNode {
		return strings.TrimSpace(node.Value)
	}
	var value string
	if errDecode := node.Decode(&value); errDecode != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index] != nil && node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
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
