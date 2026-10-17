package config

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// ProjectV8ConfigAliases moves canonical fields into a requested historical
// subtree. Moving, rather than copying, preserves PUT and DELETE semantics when
// the subtree contains both shared settings and OAuth-only fields.
func ProjectV8ConfigAliases(root *yaml.Node, path string) {
	if path == "" {
		return
	}
	paths := append(append([]configPath(nil), v8Aliases...), v8SharedStructPaths...)
	for _, alias := range paths {
		if path != alias.old && !strings.HasPrefix(alias.old, path+".") && !strings.HasPrefix(path, alias.old+".") {
			continue
		}
		value := yamlPath(root, alias.current)
		if value == nil {
			continue
		}
		// Struct aliases only represent empty containers; their populated fields
		// have individual mappings, sometimes with different nesting.
		if value.Kind == yaml.MappingNode && len(value.Content) != 0 {
			continue
		}
		copy := copyYAMLPathValue(root, alias.current)
		setYAMLPathWithComments(root, alias.old, copy)
		deleteYAMLPath(root, alias.current)
	}
}

// NormalizeV8ConfigAliases accepts historical v8 paths without discarding
// unknown fields. Callers still validate the canonical document before saving.
// Normalize request bodies before merging so an alias update can replace a
// canonical value already present in the stored configuration.
func NormalizeV8ConfigAliases(root *yaml.Node) error {
	var shape any
	if err := root.Decode(&shape); err != nil {
		return err
	}
	*root = *expandConfigAliases(root)
	// A null historical container resets its fields. Represent the reset as
	// null leaves so merging a root PATCH cannot turn it into an empty-map no-op.
	for _, container := range v8SharedStructPaths {
		value := yamlPath(root, container.old)
		if value == nil || value.Tag != "!!null" {
			continue
		}
		copy := copyYAMLPathValue(root, container.old)
		for _, alias := range v8Aliases {
			if strings.HasPrefix(alias.old, container.old+".") && yamlPath(root, alias.current) == nil {
				setYAMLPathWithComments(root, alias.current, copy)
				copy.HeadComment = ""
			}
		}
		deleteYAMLPath(root, container.old)
	}
	for _, alias := range v8Aliases {
		if yamlPath(root, alias.old) == nil {
			continue
		}
		if yamlPath(root, alias.current) == nil {
			setYAMLPathWithComments(root, alias.current, copyYAMLPathValue(root, alias.old))
		}
		deleteYAMLPath(root, alias.old)
	}
	for _, alias := range v8SharedStructPaths {
		value := yamlPath(root, alias.old)
		if value == nil || (value.Tag != "!!null" && (value.Kind != yaml.MappingNode || len(value.Content) != 0)) {
			continue
		}
		if yamlPath(root, alias.current) == nil {
			setYAMLPathWithComments(root, alias.current, copyYAMLPathValue(root, alias.old))
			value = yamlPath(root, alias.current)
			value.Kind, value.Tag, value.Value = yaml.MappingNode, "!!map", ""
		}
		deleteYAMLPath(root, alias.old)
	}
	return nil
}
