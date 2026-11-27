package usage

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the usage block of plugins.configs.prox.
type Config struct {
	Retention time.Duration    `yaml:"retention" json:"retention"`
	Pricing   map[string]Price `yaml:"pricing" json:"pricing,omitempty"`
}

// Default keeps 90 days of usage.
func Default() Config { return Config{Retention: 90 * 24 * time.Hour} }

// Decode reads the usage block from the prox settings YAML.
func Decode(raw []byte) (Config, error) {
	var wrapper struct {
		Usage yaml.Node `yaml:"usage"`
	}
	cfg := Default()
	if len(strings.TrimSpace(string(raw))) != 0 {
		if err := yaml.Unmarshal(raw, &wrapper); err != nil {
			return Config{}, fmt.Errorf("usage: invalid YAML")
		}
		if wrapper.Usage.Kind != 0 {
			if err := wrapper.Usage.Decode(&cfg); err != nil {
				return Config{}, fmt.Errorf("usage: %w", err)
			}
		}
	}
	if cfg.Retention < 24*time.Hour {
		return Config{}, fmt.Errorf("usage: retention must be at least 24h")
	}
	for k, p := range cfg.Pricing {
		if p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheWrite < 0 {
			return Config{}, fmt.Errorf("usage: negative price for %q", k)
		}
	}
	return cfg, nil
}
