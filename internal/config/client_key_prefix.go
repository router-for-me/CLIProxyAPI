package config

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// ClientAPIKeyPrefix defines the standardized prefix for client-facing API keys.
const ClientAPIKeyPrefix = "cpa-"

// EnsureClientAPIKeyPrefix ensures that a single API key string carries the standardized prefix.
func EnsureClientAPIKeyPrefix(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, ClientAPIKeyPrefix) {
		return trimmed
	}
	return ClientAPIKeyPrefix + trimmed
}

// SanitizeClientAPIKeys normalizes configured client API keys in Config by ensuring each key
// has the standardized "cpa-" prefix and dropping duplicates.
func (c *Config) SanitizeClientAPIKeys() {
	if c == nil || len(c.APIKeys) == 0 {
		return
	}
	normalized := make([]string, 0, len(c.APIKeys))
	seen := make(map[string]struct{}, len(c.APIKeys))
	for _, key := range c.APIKeys {
		norm := EnsureClientAPIKeyPrefix(key)
		if norm == "" {
			continue
		}
		if _, exists := seen[norm]; exists {
			continue
		}
		seen[norm] = struct{}{}
		normalized = append(normalized, norm)
	}
	c.APIKeys = normalized
}

// NormalizeClientAPIKeysNode normalizes a YAML sequence node containing client API keys,
// ensuring every entry has the "cpa-" prefix and removing duplicates while preserving comments.
func NormalizeClientAPIKeysNode(node *yaml.Node) bool {
	if node == nil || node.Kind != yaml.SequenceNode {
		return false
	}
	changed := false
	filtered := make([]*yaml.Node, 0, len(node.Content))
	seen := make(map[string]struct{}, len(node.Content))

	for _, item := range node.Content {
		if item.Kind == yaml.ScalarNode {
			trimmed := strings.TrimSpace(item.Value)
			if trimmed != "" {
				if !strings.HasPrefix(trimmed, ClientAPIKeyPrefix) {
					item.Value = ClientAPIKeyPrefix + trimmed
					changed = true
				}
				if _, exists := seen[item.Value]; exists {
					changed = true
					continue
				}
				seen[item.Value] = struct{}{}
			}
		}
		filtered = append(filtered, item)
	}

	if changed {
		node.Content = filtered
	}
	return changed
}

// NormalizeClientAPIKeysYAML parses data as YAML, normalizes client API keys
// in "access.api-keys" (v8) or sequence "api-keys" (legacy) to carry the "cpa-" prefix,
// and returns the updated document. Upstream provider key mappings are untouched.
func NormalizeClientAPIKeysYAML(data []byte) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, err
	}
	if len(doc.Content) == 0 {
		return data, false, nil
	}
	root := doc.Content[0]
	if root == nil || root.Kind != yaml.MappingNode {
		return data, false, nil
	}

	// Legacy-only documents stay legacy on disk until explicitly migrated.
	// We only rewrite client keys on disk if the document already uses the v8 layout.
	if !IsV8ConfigLayout(root) {
		return data, false, nil
	}

	changed := false

	// v8 layout: access.api-keys
	if accessNode := yamlPath(root, "access.api-keys"); accessNode != nil && accessNode.Kind == yaml.SequenceNode {
		if NormalizeClientAPIKeysNode(accessNode) {
			changed = true
		}
	}

	if !changed {
		return data, false, nil
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
