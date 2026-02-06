package config

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// antigravityModelConversionTable maps old built-in aliases to actual model names
// for the antigravity channel during migration.
var antigravityModelConversionTable = map[string]string{
	"gemini-2.5-computer-use-preview-10-2025": "rev19-uic3-1p",
	"gemini-3-pro-image-preview":              "gemini-3-pro-image",
	"gemini-3-pro-preview":                    "gemini-3-pro-high",
	"gemini-3-flash-preview":                  "gemini-3-flash",
	"gemini-claude-sonnet-4-5":                "claude-sonnet-4-5",
	"gemini-claude-sonnet-4-5-thinking":       "claude-sonnet-4-5-thinking",
	"gemini-claude-opus-4-5-thinking":         "claude-opus-4-5-thinking",
	"gemini-claude-opus-4-6-thinking":         "claude-opus-4-6-thinking",
}

// defaultAntigravityAliases returns the default oauth-model-alias configuration
// for the antigravity channel when neither field exists.
func defaultAntigravityAliases() []OAuthModelAlias {
	return []OAuthModelAlias{
		{Name: "rev19-uic3-1p", Alias: "gemini-2.5-computer-use-preview-10-2025"},
		{Name: "gemini-3-pro-image", Alias: "gemini-3-pro-image-preview"},
		{Name: "gemini-3-pro-high", Alias: "gemini-3-pro-preview"},
		{Name: "gemini-3-flash", Alias: "gemini-3-flash-preview"},
		{Name: "claude-sonnet-4-5", Alias: "gemini-claude-sonnet-4-5"},
		{Name: "claude-sonnet-4-5-thinking", Alias: "gemini-claude-sonnet-4-5-thinking"},
		{Name: "claude-opus-4-5-thinking", Alias: "gemini-claude-opus-4-5-thinking"},
		{Name: "claude-opus-4-6-thinking", Alias: "gemini-claude-opus-4-6-thinking"},
	}
}

// MigrateOAuthModelAlias checks for and performs migration from oauth-model-mappings
// to oauth-model-alias at startup. Returns true if migration was performed.
//
// Migration flow:
// 1. Check if oauth-model-alias exists -> skip migration
// 2. Check if oauth-model-mappings exists -> convert and migrate
//   - For antigravity channel, convert old built-in aliases to actual model names
//
// 3. Neither exists -> add default antigravity config
func MigrateOAuthModelAlias(configFile string) (bool, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}

	// Parse YAML into node tree to preserve structure
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return false, nil
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return false, nil
	}
	rootMap := root.Content[0]
	if rootMap == nil || rootMap.Kind != yaml.MappingNode {
		return false, nil
	}

	// Check if oauth-model-alias already exists
	if findMapKeyIndex(rootMap, "oauth-model-alias") >= 0 {
		return false, nil
	}

	// Check if oauth-model-mappings exists
	oldIdx := findMapKeyIndex(rootMap, "oauth-model-mappings")
	if oldIdx >= 0 {
		// Migrate from old field
		return migrateFromOldField(configFile, &root, rootMap, oldIdx)
	}

	// Neither field exists - add default antigravity config
	return addDefaultAntigravityConfig(configFile, &root, rootMap)
}

// migrateFromOldField converts oauth-model-mappings to oauth-model-alias
func migrateFromOldField(configFile string, root *yaml.Node, rootMap *yaml.Node, oldIdx int) (bool, error) {
	if oldIdx+1 >= len(rootMap.Content) {
		return false, nil
	}
	oldValue := rootMap.Content[oldIdx+1]
	if oldValue == nil || oldValue.Kind != yaml.MappingNode {
		return false, nil
	}

	// Parse the old aliases
	oldAliases := parseOldAliasNode(oldValue)
	if len(oldAliases) == 0 {
		// Remove the old field and write
		removeMapKeyByIndex(rootMap, oldIdx)
		return writeYAMLNode(configFile, root)
	}

	// Convert model names for antigravity channel
	newAliases := make(map[string][]OAuthModelAlias, len(oldAliases))
	for channel, entries := range oldAliases {
		converted := make([]OAuthModelAlias, 0, len(entries))
		for _, entry := range entries {
			newEntry := OAuthModelAlias{
				Name:  entry.Name,
				Alias: entry.Alias,
				Fork:  entry.Fork,
			}
			// Convert model names for antigravity channel
			if strings.EqualFold(channel, "antigravity") {
				if actual, ok := antigravityModelConversionTable[entry.Name]; ok {
					newEntry.Name = actual
				}
			}
			converted = append(converted, newEntry)
		}
		newAliases[channel] = converted
	}

	// For antigravity channel, supplement missing default aliases
	if antigravityEntries, exists := newAliases["antigravity"]; exists {
		// Build a set of already configured model names (upstream names)
		configuredModels := make(map[string]bool, len(antigravityEntries))
		for _, entry := range antigravityEntries {
			configuredModels[entry.Name] = true
		}

		// Add missing default aliases
		for _, defaultAlias := range defaultAntigravityAliases() {
			if !configuredModels[defaultAlias.Name] {
				antigravityEntries = append(antigravityEntries, defaultAlias)
			}
		}
		newAliases["antigravity"] = antigravityEntries
	}

	// Build new node
	newNode := buildOAuthModelAliasNode(newAliases)

	// Replace old key with new key and value
	rootMap.Content[oldIdx].Value = "oauth-model-alias"
	rootMap.Content[oldIdx+1] = newNode

	return writeYAMLNode(configFile, root)
}

// addDefaultAntigravityConfig adds the default antigravity configuration
func addDefaultAntigravityConfig(configFile string, root *yaml.Node, rootMap *yaml.Node) (bool, error) {
	defaults := map[string][]OAuthModelAlias{
		"antigravity": defaultAntigravityAliases(),
	}
	newNode := buildOAuthModelAliasNode(defaults)

	// Add new key-value pair
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "oauth-model-alias"}
	rootMap.Content = append(rootMap.Content, keyNode, newNode)

	return writeYAMLNode(configFile, root)
}

// parseOldAliasNode parses the old oauth-model-mappings node structure
func parseOldAliasNode(node *yaml.Node) map[string][]OAuthModelAlias {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	result := make(map[string][]OAuthModelAlias)
	for i := 0; i+1 < len(node.Content); i += 2 {
		channelNode := node.Content[i]
		entriesNode := node.Content[i+1]
		if channelNode == nil || entriesNode == nil {
			continue
		}
		channel := strings.ToLower(strings.TrimSpace(channelNode.Value))
		if channel == "" || entriesNode.Kind != yaml.SequenceNode {
			continue
		}
		entries := make([]OAuthModelAlias, 0, len(entriesNode.Content))
		for _, entryNode := range entriesNode.Content {
			if entryNode == nil || entryNode.Kind != yaml.MappingNode {
				continue
			}
			entry := parseAliasEntry(entryNode)
			if entry.Name != "" && entry.Alias != "" {
				entries = append(entries, entry)
			}
		}
		if len(entries) > 0 {
			result[channel] = entries
		}
	}
	return result
}

// parseAliasEntry parses a single alias entry node
func parseAliasEntry(node *yaml.Node) OAuthModelAlias {
	var entry OAuthModelAlias
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		if keyNode == nil || valNode == nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(keyNode.Value)) {
		case "name":
			entry.Name = strings.TrimSpace(valNode.Value)
		case "alias":
			entry.Alias = strings.TrimSpace(valNode.Value)
		case "fork":
			entry.Fork = strings.ToLower(strings.TrimSpace(valNode.Value)) == "true"
		}
	}
	return entry
}

// buildOAuthModelAliasNode creates a YAML node for oauth-model-alias
func buildOAuthModelAliasNode(aliases map[string][]OAuthModelAlias) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for channel, entries := range aliases {
		channelNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: channel}
		entriesNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, entry := range entries {
			entryNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			entryNode.Content = append(entryNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: entry.Name},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "alias"},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: entry.Alias},
			)
			if entry.Fork {
				entryNode.Content = append(entryNode.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "fork"},
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
				)
			}
			entriesNode.Content = append(entriesNode.Content, entryNode)
		}
		node.Content = append(node.Content, channelNode, entriesNode)
	}
	return node
}

// removeMapKeyByIndex removes a key-value pair from a mapping node by index
func removeMapKeyByIndex(mapNode *yaml.Node, keyIdx int) {
	if mapNode == nil || mapNode.Kind != yaml.MappingNode {
		return
	}
	if keyIdx < 0 || keyIdx+1 >= len(mapNode.Content) {
		return
	}
	mapNode.Content = append(mapNode.Content[:keyIdx], mapNode.Content[keyIdx+2:]...)
}

// writeYAMLNode writes the YAML node tree back to file
func writeYAMLNode(configFile string, root *yaml.Node) (bool, error) {
	f, err := os.Create(configFile)
	if err != nil {
		return false, err
	}
	defer f.Close()

	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return false, err
	}
	if err := enc.Close(); err != nil {
		return false, err
	}
	return true, nil
}
