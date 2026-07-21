package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIKeyEntry is a client proxy API key with optional model allowlist.
//
// When AllowedModels is nil, the key is unrestricted.
// When AllowedModels is non-nil (including empty), only listed models are allowed;
// an empty list denies every model.
type APIKeyEntry struct {
	APIKey        string    `yaml:"api-key" json:"api-key"`
	AllowedModels *[]string `yaml:"allowed-models,omitempty" json:"allowed-models,omitempty"`
}

// APIKeyList is a mixed list of plain key strings and key objects with model allowlists.
type APIKeyList []APIKeyEntry

// KeyValues returns the configured client API key strings (order preserved, blanks skipped).
func (l APIKeyList) KeyValues() []string {
	if len(l) == 0 {
		return nil
	}
	out := make([]string, 0, len(l))
	for i := range l {
		key := strings.TrimSpace(l[i].APIKey)
		if key == "" {
			continue
		}
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// HasRestrictions reports whether any entry defines an allowlist.
func (l APIKeyList) HasRestrictions() bool {
	for i := range l {
		if l[i].AllowedModels != nil {
			return true
		}
	}
	return false
}

// Lookup returns the last entry matching key (last-write-wins for duplicates).
func (l APIKeyList) Lookup(key string) (APIKeyEntry, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return APIKeyEntry{}, false
	}
	var found APIKeyEntry
	ok := false
	for i := range l {
		if strings.TrimSpace(l[i].APIKey) == key {
			found = l[i]
			ok = true
		}
	}
	return found, ok
}

// AllowsModel reports whether clientKey may use modelName.
// Unknown or empty client keys are unrestricted (no client identity / open proxy).
// Matching is case-insensitive. Patterns ending with '*' are prefix wildcards.
func (l APIKeyList) AllowsModel(clientKey, modelName string) bool {
	clientKey = strings.TrimSpace(clientKey)
	if clientKey == "" {
		return true
	}
	entry, ok := l.Lookup(clientKey)
	if !ok {
		return true
	}
	if entry.AllowedModels == nil {
		return true
	}
	return modelMatchesAllowlist(modelName, *entry.AllowedModels)
}

// FilterModelIDs keeps model IDs allowed for clientKey.
func (l APIKeyList) FilterModelIDs(clientKey string, modelIDs []string) []string {
	if len(modelIDs) == 0 {
		return modelIDs
	}
	if !l.HasRestrictions() {
		return modelIDs
	}
	clientKey = strings.TrimSpace(clientKey)
	if clientKey == "" {
		return modelIDs
	}
	entry, ok := l.Lookup(clientKey)
	if !ok || entry.AllowedModels == nil {
		return modelIDs
	}
	out := make([]string, 0, len(modelIDs))
	for _, id := range modelIDs {
		if modelMatchesAllowlist(id, *entry.AllowedModels) {
			out = append(out, id)
		}
	}
	return out
}

// FilterModelMaps filters model maps using idField ("id" or "name").
// Gemini "name" values may include a "models/" prefix; both forms are checked.
func (l APIKeyList) FilterModelMaps(clientKey string, models []map[string]any, idField string) []map[string]any {
	if len(models) == 0 {
		return models
	}
	if !l.HasRestrictions() {
		return models
	}
	clientKey = strings.TrimSpace(clientKey)
	if clientKey == "" {
		return models
	}
	entry, ok := l.Lookup(clientKey)
	if !ok || entry.AllowedModels == nil {
		return models
	}
	if idField == "" {
		idField = "id"
	}
	out := make([]map[string]any, 0, len(models))
	for _, model := range models {
		raw, _ := model[idField].(string)
		if modelIDAllowed(raw, *entry.AllowedModels) {
			out = append(out, model)
		}
	}
	return out
}

func modelIDAllowed(rawID string, allowlist []string) bool {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return false
	}
	if modelMatchesAllowlist(rawID, allowlist) {
		return true
	}
	// Gemini list entries often use "models/<id>".
	if strings.HasPrefix(strings.ToLower(rawID), "models/") {
		return modelMatchesAllowlist(rawID[len("models/"):], allowlist)
	}
	return false
}

func modelMatchesAllowlist(model string, allowlist []string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	modelLower := strings.ToLower(model)
	for _, pattern := range allowlist {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		patternLower := strings.ToLower(pattern)
		if strings.HasSuffix(patternLower, "*") {
			prefix := strings.TrimSuffix(patternLower, "*")
			if prefix == "" || strings.HasPrefix(modelLower, prefix) {
				return true
			}
			continue
		}
		if modelLower == patternLower {
			return true
		}
	}
	return false
}

// UnmarshalYAML accepts mixed string / object entries under api-keys.
func (l *APIKeyList) UnmarshalYAML(value *yaml.Node) error {
	if l == nil {
		return fmt.Errorf("APIKeyList: nil receiver")
	}
	if value == nil || value.Kind == 0 || (value.Kind == yaml.ScalarNode && strings.TrimSpace(value.Value) == "") {
		*l = nil
		return nil
	}
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("api-keys: expected sequence, got %v", value.Kind)
	}
	out := make(APIKeyList, 0, len(value.Content))
	for _, node := range value.Content {
		entry, err := parseAPIKeyYAMLNode(node)
		if err != nil {
			return err
		}
		if strings.TrimSpace(entry.APIKey) == "" {
			continue
		}
		out = append(out, entry)
	}
	*l = out
	return nil
}

func parseAPIKeyYAMLNode(node *yaml.Node) (APIKeyEntry, error) {
	if node == nil {
		return APIKeyEntry{}, fmt.Errorf("api-keys: nil entry")
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return APIKeyEntry{APIKey: strings.TrimSpace(node.Value)}, nil
	case yaml.MappingNode:
		var raw struct {
			APIKey        string    `yaml:"api-key"`
			AllowedModels *[]string `yaml:"allowed-models"`
		}
		if err := node.Decode(&raw); err != nil {
			return APIKeyEntry{}, fmt.Errorf("api-keys: invalid object entry: %w", err)
		}
		entry := APIKeyEntry{APIKey: strings.TrimSpace(raw.APIKey)}
		if raw.AllowedModels != nil {
			models := normalizeModelList(*raw.AllowedModels)
			entry.AllowedModels = &models
		}
		return entry, nil
	default:
		return APIKeyEntry{}, fmt.Errorf("api-keys: unsupported entry kind %v", node.Kind)
	}
}

// MarshalYAML writes plain strings for unrestricted keys and objects when allowlists are set.
func (l APIKeyList) MarshalYAML() (any, error) {
	if l == nil {
		return []any{}, nil
	}
	out := make([]any, 0, len(l))
	for i := range l {
		key := strings.TrimSpace(l[i].APIKey)
		if key == "" {
			continue
		}
		if l[i].AllowedModels == nil {
			out = append(out, key)
			continue
		}
		models := normalizeModelList(*l[i].AllowedModels)
		out = append(out, map[string]any{
			"api-key":        key,
			"allowed-models": models,
		})
	}
	return out, nil
}

// UnmarshalJSON accepts mixed string / object entries.
func (l *APIKeyList) UnmarshalJSON(data []byte) error {
	if l == nil {
		return fmt.Errorf("APIKeyList: nil receiver")
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*l = nil
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("api-keys: %w", err)
	}
	out := make(APIKeyList, 0, len(raw))
	for _, item := range raw {
		entry, err := parseAPIKeyJSONItem(item)
		if err != nil {
			return err
		}
		if strings.TrimSpace(entry.APIKey) == "" {
			continue
		}
		out = append(out, entry)
	}
	*l = out
	return nil
}

func parseAPIKeyJSONItem(item json.RawMessage) (APIKeyEntry, error) {
	trimmed := strings.TrimSpace(string(item))
	if trimmed == "" || trimmed == "null" {
		return APIKeyEntry{}, nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		var key string
		if err := json.Unmarshal(item, &key); err != nil {
			return APIKeyEntry{}, fmt.Errorf("api-keys: invalid string entry: %w", err)
		}
		return APIKeyEntry{APIKey: strings.TrimSpace(key)}, nil
	}
	var raw struct {
		APIKey        string    `json:"api-key"`
		AllowedModels *[]string `json:"allowed-models"`
	}
	if err := json.Unmarshal(item, &raw); err != nil {
		return APIKeyEntry{}, fmt.Errorf("api-keys: invalid object entry: %w", err)
	}
	entry := APIKeyEntry{APIKey: strings.TrimSpace(raw.APIKey)}
	if raw.AllowedModels != nil {
		models := normalizeModelList(*raw.AllowedModels)
		entry.AllowedModels = &models
	}
	return entry, nil
}

// MarshalJSON writes plain strings for unrestricted keys and objects when allowlists are set.
func (l APIKeyList) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	out := make([]any, 0, len(l))
	for i := range l {
		key := strings.TrimSpace(l[i].APIKey)
		if key == "" {
			continue
		}
		if l[i].AllowedModels == nil {
			out = append(out, key)
			continue
		}
		models := normalizeModelList(*l[i].AllowedModels)
		out = append(out, map[string]any{
			"api-key":        key,
			"allowed-models": models,
		})
	}
	return json.Marshal(out)
}

// FromStringKeys builds an unrestricted APIKeyList from plain key strings.
func FromStringKeys(keys []string) APIKeyList {
	if len(keys) == 0 {
		return nil
	}
	out := make(APIKeyList, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out = append(out, APIKeyEntry{APIKey: key})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeModelList(models []string) []string {
	if models == nil {
		return []string{}
	}
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	return out
}
