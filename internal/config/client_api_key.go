package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ClientAPIKey is a client credential used to access this proxy (config api-keys).
type ClientAPIKey struct {
	Key          string            `yaml:"key" json:"key"`
	ModelAliases []OAuthModelAlias `yaml:"model-aliases,omitempty" json:"model-aliases,omitempty"`
}

// ClientAPIKeys is the api-keys section: each entry may be a plain key string or a ClientAPIKey object.
type ClientAPIKeys []ClientAPIKey

// UnmarshalYAML accepts a sequence of strings or mapping objects with key and model-aliases.
func (keys *ClientAPIKeys) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		*keys = nil
		return nil
	}
	switch value.Kind {
	case yaml.SequenceNode:
		out := make(ClientAPIKeys, 0, len(value.Content))
		for _, item := range value.Content {
			if item == nil {
				continue
			}
			switch item.Kind {
			case yaml.ScalarNode:
				key := strings.TrimSpace(item.Value)
				if key == "" {
					continue
				}
				out = append(out, ClientAPIKey{Key: key})
			case yaml.MappingNode:
				var entry ClientAPIKey
				if err := item.Decode(&entry); err != nil {
					return err
				}
				entry.Key = strings.TrimSpace(entry.Key)
				if entry.Key == "" {
					continue
				}
				out = append(out, entry)
			default:
				return fmt.Errorf("api-keys: unsupported entry kind %v", item.Kind)
			}
		}
		*keys = out
		return nil
	default:
		return fmt.Errorf("api-keys: expected sequence, got %v", value.Kind)
	}
}

// MarshalYAML writes a compact list (plain strings when no aliases).
func (keys ClientAPIKeys) MarshalYAML() (interface{}, error) {
	if len(keys) == 0 {
		return []string{}, nil
	}
	out := make([]interface{}, 0, len(keys))
	for _, entry := range keys {
		if len(entry.ModelAliases) == 0 {
			out = append(out, entry.Key)
			continue
		}
		out = append(out, ClientAPIKey{
			Key:          entry.Key,
			ModelAliases: entry.ModelAliases,
		})
	}
	return out, nil
}

// APIKeyStrings returns trimmed client key values for authentication.
func (keys ClientAPIKeys) APIKeyStrings() []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, entry := range keys {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SanitizeClientAPIKeys normalizes client keys and per-key model aliases.
func (cfg *Config) SanitizeClientAPIKeys() {
	if cfg == nil {
		return
	}
	if len(cfg.ClientAPIKeys) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(cfg.ClientAPIKeys))
	clean := make(ClientAPIKeys, 0, len(cfg.ClientAPIKeys))
	for _, entry := range cfg.ClientAPIKeys {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		keyLower := strings.ToLower(key)
		if _, exists := seen[keyLower]; exists {
			continue
		}
		seen[keyLower] = struct{}{}
		aliases := sanitizeOAuthModelAliasList(entry.ModelAliases)
		clean = append(clean, ClientAPIKey{Key: key, ModelAliases: aliases})
	}
	cfg.ClientAPIKeys = clean
}

func sanitizeOAuthModelAliasList(aliases []OAuthModelAlias) []OAuthModelAlias {
	if len(aliases) == 0 {
		return nil
	}
	cfg := Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"client": aliases,
		},
	}
	cfg.SanitizeOAuthModelAlias()
	out := cfg.OAuthModelAlias["client"]
	if len(out) == 0 {
		return nil
	}
	return append([]OAuthModelAlias(nil), out...)
}

// ModelAliasesFor returns model aliases configured for the given client API key.
func (keys ClientAPIKeys) ModelAliasesFor(clientKey string) []OAuthModelAlias {
	clientKey = strings.TrimSpace(clientKey)
	if clientKey == "" {
		return nil
	}
	for _, entry := range keys {
		if entry.Key != clientKey {
			continue
		}
		if len(entry.ModelAliases) == 0 {
			return nil
		}
		return append([]OAuthModelAlias(nil), entry.ModelAliases...)
	}
	return nil
}

// ClientAPIKeyModelAliases returns model aliases for the given client API key (case-sensitive match on stored key).
func (cfg *Config) ClientAPIKeyModelAliases(clientKey string) []OAuthModelAlias {
	if cfg == nil {
		return nil
	}
	return cfg.ClientAPIKeys.ModelAliasesFor(clientKey)
}
