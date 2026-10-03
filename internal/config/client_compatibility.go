package config

import "encoding/json"

// CodexMultiAgentV2Enabled keeps historical programmatic callers working while
// parsed YAML and JSON use only the canonical client setting.
func (cfg *Config) CodexMultiAgentV2Enabled() bool {
	return cfg != nil && (cfg.SDKConfig.CodexMultiAgentV2Enabled() || cfg.Codex.OptimizeMultiAgentV2)
}

// MarshalJSON projects historical programmatic fields into the canonical client layout.
func (cfg Config) MarshalJSON() ([]byte, error) {
	cfg.Client.Codex.OptimizeMultiAgentV2 = cfg.CodexMultiAgentV2Enabled()
	return json.Marshal(legacyConfig(cfg))
}
