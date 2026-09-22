package management

import (
	"errors"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var errAccountProtectedPluginConfigPath = errors.New("protected plugin config path")

func accountSanitizePluginConfigJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if accountPluginConfigKeyProtected(key) {
				continue
			}
			out[key] = accountSanitizePluginConfigJSON(child)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, accountSanitizePluginConfigJSON(child))
		}
		return out
	default:
		return value
	}
}

func accountPluginConfigKeyProtected(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(normalized, "billing") || strings.Contains(normalized, "quota")
}

func accountPluginConfigContainsProtectedFields(node *yaml.Node) bool {
	return len(accountPluginProtectedYAMLPaths(node)) > 0
}

func mergeAccountPluginPUTConfigWithProtectedOriginal(original, requested *yaml.Node) (*yaml.Node, error) {
	if original == nil {
		original = emptyYAMLMappingNode()
	}
	if requested == nil {
		requested = emptyYAMLMappingNode()
	}
	merged := cloneYAMLNode(requested)
	if accountPluginConfigContainsProtectedFields(original) {
		oldEnabled := accountYAMLLookupPath(original, []string{"enabled"})
		newEnabled := accountYAMLLookupPath(requested, []string{"enabled"})
		if newEnabled != nil && (oldEnabled == nil || !accountYAMLNodesSemanticallyEqual(newEnabled, oldEnabled)) {
			return nil, errAccountProtectedPluginConfigPath
		}
		if newEnabled == nil && oldEnabled != nil {
			accountYAMLSetPath(merged, []string{"enabled"}, oldEnabled)
		}
	}
	for _, path := range accountPluginProtectedYAMLPaths(requested) {
		requestedValue := accountYAMLLookupPath(requested, path)
		originalValue := accountYAMLLookupPath(original, path)
		if originalValue == nil || !accountYAMLNodesSemanticallyEqual(requestedValue, originalValue) {
			return nil, errAccountProtectedPluginConfigPath
		}
	}
	for _, path := range accountPluginProtectedYAMLPaths(original) {
		originalValue := accountYAMLLookupPath(original, path)
		requestedValue := accountYAMLLookupPath(requested, path)
		if requestedValue != nil {
			if !accountYAMLNodesSemanticallyEqual(requestedValue, originalValue) {
				return nil, errAccountProtectedPluginConfigPath
			}
			continue
		}
		if accountPluginProtectedAncestorReplaced(requested, path) {
			return nil, errAccountProtectedPluginConfigPath
		}
		accountYAMLSetPath(merged, path, originalValue)
	}
	return merged, nil
}

func rejectChangedAccountPluginProtectedYAML(updated, original *yaml.Node) error {
	if accountPluginConfigContainsProtectedFields(original) {
		oldEnabled := accountYAMLLookupPath(original, []string{"enabled"})
		newEnabled := accountYAMLLookupPath(updated, []string{"enabled"})
		if (oldEnabled == nil) != (newEnabled == nil) || (oldEnabled != nil && !accountYAMLNodesSemanticallyEqual(oldEnabled, newEnabled)) {
			return errAccountProtectedPluginConfigPath
		}
	}
	paths := accountPluginProtectedYAMLPathUnion(updated, original)
	for _, path := range paths {
		updatedValue := accountYAMLLookupPath(updated, path)
		originalValue := accountYAMLLookupPath(original, path)
		if updatedValue == nil || originalValue == nil || !accountYAMLNodesSemanticallyEqual(updatedValue, originalValue) {
			return errAccountProtectedPluginConfigPath
		}
	}
	return nil
}

func accountPluginProtectedAncestorReplaced(root *yaml.Node, path []string) bool {
	if len(path) <= 1 {
		return false
	}
	current := root
	for _, segment := range path[:len(path)-1] {
		if current == nil {
			return false
		}
		if current.Kind != yaml.MappingNode {
			return true
		}
		var child *yaml.Node
		for i := 0; i+1 < len(current.Content); i += 2 {
			key := current.Content[i]
			if key != nil && key.Value == segment {
				child = current.Content[i+1]
				break
			}
		}
		if child == nil {
			return false
		}
		if child.Kind != yaml.MappingNode {
			return true
		}
		current = child
	}
	return false
}

func accountPluginProtectedYAMLPathUnion(nodes ...*yaml.Node) [][]string {
	byKey := make(map[string][]string)
	for _, node := range nodes {
		for _, path := range accountPluginProtectedYAMLPaths(node) {
			byKey[accountPluginYAMLPathKey(path)] = path
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([][]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, byKey[key])
	}
	return out
}

func accountPluginProtectedYAMLPaths(root *yaml.Node) [][]string {
	var out [][]string
	accountPluginCollectProtectedYAMLPaths(root, nil, &out)
	return out
}

func accountPluginCollectProtectedYAMLPaths(node *yaml.Node, path []string, out *[][]string) {
	if node == nil {
		return
	}
	if node.Kind == yaml.SequenceNode {
		if len(path) > 0 && accountPluginYAMLSubtreeContainsProtectedKey(node) {
			*out = append(*out, append([]string(nil), path...))
		}
		return
	}
	if node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]
		if key == nil {
			continue
		}
		childPath := append(append([]string(nil), path...), key.Value)
		if accountPluginConfigKeyProtected(key.Value) {
			*out = append(*out, childPath)
			continue
		}
		if value != nil && value.Kind == yaml.SequenceNode && accountPluginYAMLSubtreeContainsProtectedKey(value) {
			*out = append(*out, childPath)
			continue
		}
		accountPluginCollectProtectedYAMLPaths(value, childPath, out)
	}
}

func accountPluginYAMLSubtreeContainsProtectedKey(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if key != nil && accountPluginConfigKeyProtected(key.Value) {
				return true
			}
			if accountPluginYAMLSubtreeContainsProtectedKey(node.Content[i+1]) {
				return true
			}
		}
		return false
	}
	for _, child := range node.Content {
		if accountPluginYAMLSubtreeContainsProtectedKey(child) {
			return true
		}
	}
	return false
}

func accountPluginYAMLPathKey(path []string) string {
	return strings.Join(path, "\x00")
}
