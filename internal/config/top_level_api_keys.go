package config

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type NamedTopLevelAPIKey struct {
	Name   string
	APIKey string
}

func ParseTopLevelAPIKeysYAML(data []byte) ([]NamedTopLevelAPIKey, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}

	apiKeysNode := topLevelAPIKeysNode(&root)
	if apiKeysNode == nil {
		return nil, nil
	}
	if apiKeysNode.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("api-keys must be a YAML sequence")
	}

	entries := make([]NamedTopLevelAPIKey, 0, len(apiKeysNode.Content))
	for index, item := range apiKeysNode.Content {
		entry, ok, err := parseNamedTopLevelAPIKeyNode(item)
		if err != nil {
			return nil, fmt.Errorf("api-keys[%d]: %w", index, err)
		}
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}

	if len(entries) == 0 {
		return nil, nil
	}
	return entries, nil
}

func NormalizeTopLevelAPIKeysYAML(data []byte) ([]byte, []NamedTopLevelAPIKey, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, nil, err
	}

	apiKeysNode := topLevelAPIKeysNode(&root)
	if apiKeysNode == nil {
		return data, nil, nil
	}
	if apiKeysNode.Kind != yaml.SequenceNode {
		return nil, nil, fmt.Errorf("api-keys must be a YAML sequence")
	}

	entries := make([]NamedTopLevelAPIKey, 0, len(apiKeysNode.Content))
	normalizedItems := make([]*yaml.Node, 0, len(apiKeysNode.Content))
	for index, item := range apiKeysNode.Content {
		entry, ok, err := parseNamedTopLevelAPIKeyNode(item)
		if err != nil {
			return nil, nil, fmt.Errorf("api-keys[%d]: %w", index, err)
		}
		if !ok {
			continue
		}
		entries = append(entries, entry)
		normalizedItems = append(normalizedItems, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: entry.APIKey})
	}
	apiKeysNode.Content = normalizedItems

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		_ = encoder.Close()
		return nil, nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, nil, err
	}

	return buf.Bytes(), entries, nil
}

func BuildTopLevelAPIKeysJSONValue(entries []NamedTopLevelAPIKey) []any {
	if len(entries) == 0 {
		return nil
	}
	result := make([]any, 0, len(entries))
	for _, entry := range entries {
		apiKey := strings.TrimSpace(entry.APIKey)
		if apiKey == "" {
			continue
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			result = append(result, apiKey)
			continue
		}
		result = append(result, map[string]string{
			"name":    name,
			"api-key": apiKey,
		})
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func topLevelAPIKeysNode(root *yaml.Node) *yaml.Node {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	mapping := root.Content[0]
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key != nil && key.Value == "api-keys" {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func parseNamedTopLevelAPIKeyNode(node *yaml.Node) (NamedTopLevelAPIKey, bool, error) {
	if node == nil {
		return NamedTopLevelAPIKey{}, false, nil
	}

	switch node.Kind {
	case yaml.ScalarNode:
		apiKey := strings.TrimSpace(node.Value)
		if apiKey == "" {
			return NamedTopLevelAPIKey{}, false, nil
		}
		return NamedTopLevelAPIKey{APIKey: apiKey}, true, nil
	case yaml.MappingNode:
		var raw struct {
			Name   string `yaml:"name"`
			APIKey string `yaml:"api-key"`
			Key    string `yaml:"key"`
		}
		if err := node.Decode(&raw); err != nil {
			return NamedTopLevelAPIKey{}, false, err
		}
		apiKey := strings.TrimSpace(raw.APIKey)
		if apiKey == "" {
			apiKey = strings.TrimSpace(raw.Key)
		}
		if apiKey == "" {
			return NamedTopLevelAPIKey{}, false, fmt.Errorf("api-key is required")
		}
		return NamedTopLevelAPIKey{
			Name:   strings.TrimSpace(raw.Name),
			APIKey: apiKey,
		}, true, nil
	default:
		return NamedTopLevelAPIKey{}, false, fmt.Errorf("unsupported YAML node kind %d", node.Kind)
	}
}
