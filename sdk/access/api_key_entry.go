package access

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIKeyEntry is a client-facing API key with an optional display name.
// A plain string is accepted and re-emitted as a plain string, so existing
// configurations keep working and round-trip unchanged.
type APIKeyEntry struct {
	// Key is the raw credential value clients send.
	Key string `yaml:"key" json:"key"`

	// Name optionally labels the key. When set it replaces the raw key as the
	// authenticated principal in usage records and request logs.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
}

// namedAPIKeyEntry is the mapping representation used when a name is present.
type namedAPIKeyEntry struct {
	Key  string `yaml:"key" json:"key"`
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
}

// UnmarshalYAML accepts either a plain scalar key or a mapping with key and name.
func (e *APIKeyEntry) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var key string
		if err := value.Decode(&key); err != nil {
			return err
		}
		e.Key = key
		e.Name = ""
		return nil
	case yaml.MappingNode:
		var raw namedAPIKeyEntry
		if err := value.Decode(&raw); err != nil {
			return err
		}
		e.Key = raw.Key
		e.Name = raw.Name
		return nil
	default:
		return fmt.Errorf("api key entry must be a string or a mapping")
	}
}

// MarshalYAML emits a plain scalar when no name is set to stay byte-for-byte
// compatible with plain string entries.
func (e APIKeyEntry) MarshalYAML() (any, error) {
	if strings.TrimSpace(e.Name) == "" {
		return e.Key, nil
	}
	return namedAPIKeyEntry{Key: e.Key, Name: e.Name}, nil
}

// UnmarshalJSON accepts either a JSON string or an object with key and name.
func (e *APIKeyEntry) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		var key string
		if err := json.Unmarshal(trimmed, &key); err != nil {
			return err
		}
		e.Key = key
		e.Name = ""
		return nil
	}
	var raw namedAPIKeyEntry
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return err
	}
	e.Key = raw.Key
	e.Name = raw.Name
	return nil
}

// MarshalJSON emits a plain string when no name is set.
func (e APIKeyEntry) MarshalJSON() ([]byte, error) {
	if strings.TrimSpace(e.Name) == "" {
		return json.Marshal(e.Key)
	}
	return json.Marshal(namedAPIKeyEntry{Key: e.Key, Name: e.Name})
}

// APIKeysFromStrings converts raw key strings into unnamed entries.
func APIKeysFromStrings(keys []string) []APIKeyEntry {
	if len(keys) == 0 {
		return nil
	}
	entries := make([]APIKeyEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, APIKeyEntry{Key: key})
	}
	return entries
}

// APIKeyValues returns the raw key values of the supplied entries.
func APIKeyValues(entries []APIKeyEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	return keys
}
