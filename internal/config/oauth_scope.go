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
	scoped := cfg.ForAPIKey()
	if scoped == nil || len(PayloadFiltersFor(source)) == 0 {
		return scoped
	}

	// Allocate independent storage so appending local rules cannot change shared capacity.
	view := *scoped
	view.Payload.Filter = make([]PayloadFilterRule, len(scoped.Payload.Filter)+len(PayloadFiltersFor(source)))
	copy(view.Payload.Filter, scoped.Payload.Filter)
	copy(view.Payload.Filter[len(scoped.Payload.Filter):], PayloadFiltersFor(source))
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
