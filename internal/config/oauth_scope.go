package config

import (
	"reflect"

	"gopkg.in/yaml.v3"
)

// ForAPIKey returns a request-local configuration view without v8 OAuth-only
// overrides. Legacy global settings and explicit API-key settings keep their
// existing semantics. The shared configuration is never modified.
func (cfg *Config) ForAPIKey() *Config {
	if cfg == nil || len(cfg.OAuthOnlyFields) == 0 {
		return cfg
	}
	filtered := *cfg
	value := reflect.ValueOf(&filtered).Elem()
	for path, scoped := range cfg.OAuthOnlyFields {
		if indexes, ok := v8FieldIndexes[path]; ok && scoped {
			value.FieldByIndex(indexes).SetZero()
		}
	}
	if cfg.OAuthOnlyFields["codex.optimize-multi-agent-v2"] {
		filtered.CodexOptimizeMultiAgentV2 = false
	}
	if cfg.OAuthOnlyFields["codex.orphan-delegation-compatibility"] {
		filtered.CodexOrphanDelegationCompatibility = false
	}
	if cfg.OAuthOnlyFields["codex.response-steering"] {
		filtered.CodexResponseSteering = false
	}
	filtered.OAuthOnlyFields = nil
	return &filtered
}

// MarshalYAML preserves OAuth scope across watcher/server YAML snapshots. A
// legacy-only Config still produces its original layout. Persistence explicitly
// marshals legacyConfig before reconciling the on-disk layout.
func (cfg Config) MarshalYAML() (any, error) {
	var root yaml.Node
	if err := root.Encode(legacyConfig(cfg)); err != nil {
		return nil, err
	}
	value := reflect.ValueOf(cfg)
	for _, path := range v8Paths {
		if !cfg.OAuthOnlyFields[path.old] {
			continue
		}
		field := yamlPath(&root, path.old)
		if field == nil {
			field = new(yaml.Node)
			if err := field.Encode(value.FieldByIndex(v8FieldIndexes[path.old]).Interface()); err != nil {
				return nil, err
			}
		}
		copy := deepCopyNode(field)
		deleteYAMLPath(&root, path.old)
		setYAMLPath(&root, path.current, copy)
	}
	return &root, nil
}
