package config

import (
	"reflect"
	"strings"

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
	for _, path := range v8Paths {
		if !strings.HasPrefix(path.current, "oauth.providers.") || !cfg.OAuthOnlyFields[path.old] {
			continue
		}
		value.FieldByIndex(v8FieldIndexes[path.old]).SetZero()
	}
	filtered.OAuthOnlyFields = nil
	return &filtered
}

// ForConfiguredKey combines static-key scoping with the selected owner's filters.
func (cfg *Config) ForConfiguredKey(source PayloadFilterSource) *Config {
	return cfg.ForAPIKey().WithKeyPayloadFilters(source)
}

// WithKeyPayloadFilters adds a selected key's filters without changing OAuth scope.
func (cfg *Config) WithKeyPayloadFilters(source PayloadFilterSource) *Config {
	filters := PayloadFiltersFor(source)
	if cfg == nil || len(filters) == 0 {
		return cfg
	}

	// Allocate independent storage so appending local rules cannot change shared capacity.
	view := *cfg
	view.Payload.Filter = make([]PayloadFilterRule, len(cfg.Payload.Filter)+len(filters))
	copy(view.Payload.Filter, cfg.Payload.Filter)
	copy(view.Payload.Filter[len(cfg.Payload.Filter):], filters)
	return &view
}

// MarshalYAML preserves OAuth scope across watcher/server YAML snapshots. A
// legacy-only Config still produces its original layout. Persistence explicitly
// marshals legacyConfig before reconciling the on-disk layout.
func (cfg Config) MarshalYAML() (any, error) {
	var root yaml.Node
	if err := root.Encode(legacyConfig(cfg)); err != nil {
		return nil, err
	}
	if cfg.ServerID != "" {
		setConfigScalar(&root, serverIDPath, cfg.ServerID)
	}
	value := reflect.ValueOf(cfg)
	for _, path := range v8Paths {
		if !strings.HasPrefix(path.current, "oauth.providers.") || !cfg.OAuthOnlyFields[path.old] {
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
