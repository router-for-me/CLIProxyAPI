package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultCommandAuthTimeoutMS         = 5000
	DefaultCommandAuthRefreshIntervalMS = 300000
)

// CommandAuthConfig configures a command that returns a bearer token on stdout.
type CommandAuthConfig struct {
	Command           string   `yaml:"command" json:"command"`
	Args              []string `yaml:"args,omitempty" json:"args,omitempty"`
	TimeoutMS         int      `yaml:"timeout-ms,omitempty" json:"timeout-ms,omitempty"`
	RefreshIntervalMS int      `yaml:"refresh-interval-ms,omitempty" json:"refresh-interval-ms,omitempty"`
	identity          string
}

func (c *CommandAuthConfig) UnmarshalYAML(value *yaml.Node) error {
	if c == nil {
		return nil
	}
	type raw CommandAuthConfig
	var out raw
	if value != nil {
		if err := value.Decode(&out); err != nil {
			return err
		}
		if node := yamlMappingValue(value, "timeout_ms"); node != nil && out.TimeoutMS == 0 {
			if err := node.Decode(&out.TimeoutMS); err != nil {
				return fmt.Errorf("parse timeout_ms: %w", err)
			}
		}
		if node := yamlMappingValue(value, "refresh_interval_ms"); node != nil && out.RefreshIntervalMS == 0 {
			if err := node.Decode(&out.RefreshIntervalMS); err != nil {
				return fmt.Errorf("parse refresh_interval_ms: %w", err)
			}
		}
	}
	*c = CommandAuthConfig(out)
	return nil
}

func (c *CommandAuthConfig) UnmarshalJSON(data []byte) error {
	if c == nil {
		return nil
	}
	type raw CommandAuthConfig
	var out raw
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	var aliases struct {
		TimeoutMS         int `json:"timeout_ms"`
		RefreshIntervalMS int `json:"refresh_interval_ms"`
	}
	if err := json.Unmarshal(data, &aliases); err != nil {
		return err
	}
	if out.TimeoutMS == 0 && aliases.TimeoutMS != 0 {
		out.TimeoutMS = aliases.TimeoutMS
	}
	if out.RefreshIntervalMS == 0 && aliases.RefreshIntervalMS != 0 {
		out.RefreshIntervalMS = aliases.RefreshIntervalMS
	}
	*c = CommandAuthConfig(out)
	return nil
}

func (k *CodexKey) UnmarshalYAML(value *yaml.Node) error {
	if k == nil {
		return nil
	}
	type raw CodexKey
	var out raw
	if value != nil {
		if err := value.Decode(&out); err != nil {
			return err
		}
		decodeYAMLStringAlias(value, "api_key", &out.APIKey)
		decodeYAMLStringAlias(value, "base_url", &out.BaseURL)
		decodeYAMLStringAlias(value, "proxy_url", &out.ProxyURL)
		decodeYAMLStringSliceAlias(value, "excluded_models", &out.ExcludedModels)
		decodeYAMLBoolAlias(value, "disable_cooling", &out.DisableCooling)
	}
	*k = CodexKey(out)
	return nil
}

func (k *CodexKey) UnmarshalJSON(data []byte) error {
	if k == nil {
		return nil
	}
	type raw CodexKey
	var out raw
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	var aliases struct {
		APIKey         string   `json:"api_key"`
		BaseURL        string   `json:"base_url"`
		ProxyURL       string   `json:"proxy_url"`
		ExcludedModels []string `json:"excluded_models"`
		DisableCooling bool     `json:"disable_cooling"`
	}
	if err := json.Unmarshal(data, &aliases); err != nil {
		return err
	}
	if strings.TrimSpace(out.APIKey) == "" {
		out.APIKey = aliases.APIKey
	}
	if strings.TrimSpace(out.BaseURL) == "" {
		out.BaseURL = aliases.BaseURL
	}
	if strings.TrimSpace(out.ProxyURL) == "" {
		out.ProxyURL = aliases.ProxyURL
	}
	if len(out.ExcludedModels) == 0 {
		out.ExcludedModels = aliases.ExcludedModels
	}
	if !out.DisableCooling {
		out.DisableCooling = aliases.DisableCooling
	}
	*k = CodexKey(out)
	return nil
}

func (c *OpenAICompatibility) UnmarshalYAML(value *yaml.Node) error {
	if c == nil {
		return nil
	}
	type raw OpenAICompatibility
	var out raw
	if value != nil {
		if err := value.Decode(&out); err != nil {
			return err
		}
		decodeYAMLStringAlias(value, "base_url", &out.BaseURL)
		decodeYAMLStringAlias(value, "proxy_url", &out.ProxyURL)
		decodeYAMLBoolAlias(value, "disable_cooling", &out.DisableCooling)
		if node := yamlMappingValue(value, "api_key_entries"); node != nil && len(out.APIKeyEntries) == 0 {
			if err := node.Decode(&out.APIKeyEntries); err != nil {
				return fmt.Errorf("parse api_key_entries: %w", err)
			}
		}
	}
	*c = OpenAICompatibility(out)
	return nil
}

func (c *OpenAICompatibility) UnmarshalJSON(data []byte) error {
	if c == nil {
		return nil
	}
	type raw OpenAICompatibility
	var out raw
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	var aliases struct {
		BaseURL        string                      `json:"base_url"`
		ProxyURL       string                      `json:"proxy_url"`
		APIKeyEntries  []OpenAICompatibilityAPIKey `json:"api_key_entries"`
		DisableCooling bool                        `json:"disable_cooling"`
	}
	if err := json.Unmarshal(data, &aliases); err != nil {
		return err
	}
	if strings.TrimSpace(out.BaseURL) == "" {
		out.BaseURL = aliases.BaseURL
	}
	if strings.TrimSpace(out.ProxyURL) == "" {
		out.ProxyURL = aliases.ProxyURL
	}
	if len(out.APIKeyEntries) == 0 {
		out.APIKeyEntries = aliases.APIKeyEntries
	}
	if !out.DisableCooling {
		out.DisableCooling = aliases.DisableCooling
	}
	*c = OpenAICompatibility(out)
	return nil
}

func normalizeCommandAuth(auth *CommandAuthConfig) {
	if auth == nil {
		return
	}
	auth.Command = strings.TrimSpace(auth.Command)
	if auth.TimeoutMS == 0 {
		auth.TimeoutMS = DefaultCommandAuthTimeoutMS
	}
	if auth.RefreshIntervalMS == 0 {
		auth.RefreshIntervalMS = DefaultCommandAuthRefreshIntervalMS
	}
	auth.identity = ""
	auth.identity = CommandAuthIdentity(auth)
}

func validateCommandAuth(section string, auth *CommandAuthConfig) error {
	if auth == nil {
		return nil
	}
	if strings.TrimSpace(auth.Command) == "" {
		return fmt.Errorf("%s auth.command is required", section)
	}
	if auth.TimeoutMS < 0 {
		return fmt.Errorf("%s auth.timeout-ms must be non-negative", section)
	}
	if auth.RefreshIntervalMS < 0 {
		return fmt.Errorf("%s auth.refresh-interval-ms must be non-negative", section)
	}
	return nil
}

// CommandAuthIdentity returns a stable non-secret identity for a command auth config.
func CommandAuthIdentity(auth *CommandAuthConfig) string {
	if auth == nil || strings.TrimSpace(auth.Command) == "" {
		return ""
	}
	if auth.identity != "" {
		return auth.identity
	}
	argsJSON, _ := json.Marshal(normalizedCommandAuthArgs(auth.Args))
	sum := sha256.Sum256([]byte(strings.TrimSpace(auth.Command) + "\x00" + string(argsJSON)))
	return hex.EncodeToString(sum[:])
}

func CommandAuthArgsJSON(auth *CommandAuthConfig) string {
	if auth == nil {
		return "[]"
	}
	argsJSON, _ := json.Marshal(normalizedCommandAuthArgs(auth.Args))
	return string(argsJSON)
}

func normalizedCommandAuthArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	return args
}

// ValidateCommandAuthConfig validates dynamic provider auth configuration.
func (cfg *Config) ValidateCommandAuthConfig() error {
	if cfg == nil {
		return nil
	}
	for i := range cfg.CodexKey {
		entry := &cfg.CodexKey[i]
		section := fmt.Sprintf("codex-api-key[%d]", i)
		if err := validateCommandAuth(section, entry.Auth); err != nil {
			return err
		}
		if entry.Auth != nil && strings.TrimSpace(entry.APIKey) != "" {
			return fmt.Errorf("%s cannot set both api-key and auth", section)
		}
	}
	for i := range cfg.OpenAICompatibility {
		entry := &cfg.OpenAICompatibility[i]
		section := fmt.Sprintf("openai-compatibility[%d]", i)
		if err := validateCommandAuth(section, entry.Auth); err != nil {
			return err
		}
		if entry.Auth != nil {
			for j := range entry.APIKeyEntries {
				if strings.TrimSpace(entry.APIKeyEntries[j].APIKey) != "" {
					return fmt.Errorf("%s cannot set both api-key-entries[%d].api-key and auth", section, j)
				}
			}
		}
	}
	return nil
}

func yamlMappingValue(value *yaml.Node, key string) *yaml.Node {
	if value == nil || value.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		if value.Content[i] != nil && value.Content[i].Value == key {
			return value.Content[i+1]
		}
	}
	return nil
}

func decodeYAMLStringAlias(value *yaml.Node, key string, target *string) {
	if target == nil || strings.TrimSpace(*target) != "" {
		return
	}
	if node := yamlMappingValue(value, key); node != nil {
		_ = node.Decode(target)
	}
}

func decodeYAMLBoolAlias(value *yaml.Node, key string, target *bool) {
	if target == nil || *target {
		return
	}
	if node := yamlMappingValue(value, key); node != nil {
		_ = node.Decode(target)
	}
}

func decodeYAMLStringSliceAlias(value *yaml.Node, key string, target *[]string) {
	if target == nil || len(*target) > 0 {
		return
	}
	if node := yamlMappingValue(value, key); node != nil {
		_ = node.Decode(target)
	}
}
