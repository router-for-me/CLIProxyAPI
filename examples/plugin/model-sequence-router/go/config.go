package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"gopkg.in/yaml.v3"
)

const (
	defaultSessionTTL   = time.Hour
	minSessionTTL       = time.Minute
	maxSessionTTL       = 24 * time.Hour
	maxEffectiveTargets = 65536
	defaultLogMaxSizeMB = 25
	defaultLogBackups   = 2
	maxLogSizeMB        = 1024
	maxLogBackups       = 10
)

type pluginConfig struct {
	Enabled     bool              `yaml:"enabled"`
	SessionTTL  string            `yaml:"session_ttl"`
	Diagnostics diagnosticsConfig `yaml:"diagnostics"`
	Aliases     []aliasConfig     `yaml:"aliases"`
}

type diagnosticsConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Path       string `yaml:"path"`
	MaxSizeMB  int    `yaml:"max_size_mb"`
	MaxBackups int    `yaml:"max_backups"`
}

func (c *diagnosticsConfig) UnmarshalYAML(node *yaml.Node) error {
	type plainDiagnostics diagnosticsConfig
	value := plainDiagnostics{MaxSizeMB: defaultLogMaxSizeMB, MaxBackups: defaultLogBackups}
	if errDecode := node.Decode(&value); errDecode != nil {
		return errDecode
	}
	*c = diagnosticsConfig(value)
	return nil
}

type aliasConfig struct {
	Alias       string         `yaml:"alias"`
	DisplayName string         `yaml:"display_name"`
	RandomStart bool           `yaml:"random_start"`
	Targets     []targetConfig `yaml:"targets"`
}

func (c *aliasConfig) UnmarshalYAML(node *yaml.Node) error {
	type plainAlias aliasConfig
	value := plainAlias{RandomStart: true}
	if errDecode := node.Decode(&value); errDecode != nil {
		return errDecode
	}
	*c = aliasConfig(value)
	return nil
}

type targetConfig struct {
	Provider string                                `yaml:"provider"`
	Model    string                                `yaml:"model"`
	Repeat   int                                   `yaml:"repeat"`
	Efforts  map[thinking.ThinkingLevel]effortTier `yaml:"efforts"`
}

func (c *targetConfig) UnmarshalYAML(node *yaml.Node) error {
	type plainTarget targetConfig
	value := plainTarget{Repeat: 1}
	if errDecode := node.Decode(&value); errDecode != nil {
		return errDecode
	}
	*c = targetConfig(value)
	return nil
}

type compiledConfig struct {
	Enabled     bool
	SessionTTL  time.Duration
	Diagnostics diagnosticsConfig
	Generation  uint64
	Aliases     []*compiledAlias
	ByLookup    map[string]*compiledAlias
}

type compiledAlias struct {
	Alias       string
	LookupKey   string
	DisplayName string
	RandomStart bool
	Sequence    []compiledTarget
}

type compiledTarget struct {
	Provider string
	Model    string
	Efforts  map[thinking.ThinkingLevel]effortTier
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:    true,
		SessionTTL: defaultSessionTTL.String(),
		Diagnostics: diagnosticsConfig{
			MaxSizeMB:  defaultLogMaxSizeMB,
			MaxBackups: defaultLogBackups,
		},
	}
}

func decodeAndCompileConfig(raw []byte, generation uint64) (*compiledConfig, error) {
	cfg := defaultPluginConfig()
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return nil, fmt.Errorf("decode plugin config: %w", errUnmarshal)
		}
	}
	ttlText := strings.TrimSpace(cfg.SessionTTL)
	if ttlText == "" {
		ttlText = defaultSessionTTL.String()
	}
	ttl, errTTL := time.ParseDuration(ttlText)
	if errTTL != nil {
		return nil, fmt.Errorf("invalid session_ttl %q: %w", ttlText, errTTL)
	}
	if ttl < minSessionTTL || ttl > maxSessionTTL {
		return nil, fmt.Errorf("session_ttl must be between %s and %s", minSessionTTL, maxSessionTTL)
	}
	if cfg.Enabled && len(cfg.Aliases) == 0 {
		return nil, fmt.Errorf("enabled configuration requires at least one alias")
	}
	diagnostics := cfg.Diagnostics
	diagnostics.Path = strings.TrimSpace(diagnostics.Path)
	if diagnostics.Enabled {
		if diagnostics.Path == "" {
			return nil, fmt.Errorf("diagnostics.path is required when diagnostics are enabled")
		}
		if !filepath.IsAbs(diagnostics.Path) {
			return nil, fmt.Errorf("diagnostics.path must be absolute")
		}
		if diagnostics.MaxSizeMB < 1 || diagnostics.MaxSizeMB > maxLogSizeMB {
			return nil, fmt.Errorf("diagnostics.max_size_mb must be between 1 and %d", maxLogSizeMB)
		}
		if diagnostics.MaxBackups < 0 || diagnostics.MaxBackups > maxLogBackups {
			return nil, fmt.Errorf("diagnostics.max_backups must be between 0 and %d", maxLogBackups)
		}
		diagnostics.Path = filepath.Clean(diagnostics.Path)
	}

	compiled := &compiledConfig{
		Enabled:     cfg.Enabled,
		SessionTTL:  ttl,
		Diagnostics: diagnostics,
		Generation:  generation,
		Aliases:     make([]*compiledAlias, 0, len(cfg.Aliases)),
		ByLookup:    make(map[string]*compiledAlias, len(cfg.Aliases)),
	}
	for aliasIndex, rawAlias := range cfg.Aliases {
		aliasName := strings.TrimSpace(rawAlias.Alias)
		if aliasName == "" {
			return nil, fmt.Errorf("aliases[%d].alias must not be blank", aliasIndex)
		}
		aliasBase, _, _ := parseSupportedEffortSuffix(aliasName)
		lookupKey := normalizedAliasKey(aliasBase)
		if _, exists := compiled.ByLookup[lookupKey]; exists {
			return nil, fmt.Errorf("duplicate alias %q", aliasName)
		}
		if len(rawAlias.Targets) == 0 {
			return nil, fmt.Errorf("alias %q requires at least one target", aliasName)
		}
		displayName := strings.TrimSpace(rawAlias.DisplayName)
		if displayName == "" {
			displayName = aliasName
		}
		alias := &compiledAlias{
			Alias:       aliasName,
			LookupKey:   lookupKey,
			DisplayName: displayName,
			RandomStart: rawAlias.RandomStart,
		}
		effectiveLength := 0
		for targetIndex, rawTarget := range rawAlias.Targets {
			provider := strings.ToLower(strings.TrimSpace(rawTarget.Provider))
			model := strings.TrimSpace(rawTarget.Model)
			if provider == "" {
				return nil, fmt.Errorf("alias %q target[%d].provider must not be blank", aliasName, targetIndex)
			}
			if model == "" {
				return nil, fmt.Errorf("alias %q target[%d].model must not be blank", aliasName, targetIndex)
			}
			repeat := rawTarget.Repeat
			if repeat < 1 {
				return nil, fmt.Errorf("alias %q target[%d].repeat must be at least 1", aliasName, targetIndex)
			}
			if repeat > maxEffectiveTargets-effectiveLength {
				return nil, fmt.Errorf("alias %q effective sequence exceeds %d positions", aliasName, maxEffectiveTargets)
			}
			if errEfforts := validateEfforts(model, rawTarget.Efforts); errEfforts != nil {
				return nil, fmt.Errorf("alias %q target[%d]: %w", aliasName, targetIndex, errEfforts)
			}
			for range repeat {
				alias.Sequence = append(alias.Sequence, compiledTarget{Provider: provider, Model: model, Efforts: rawTarget.Efforts})
			}
			effectiveLength += repeat
		}
		compiled.Aliases = append(compiled.Aliases, alias)
		compiled.ByLookup[lookupKey] = alias
	}
	return compiled, nil
}

func normalizedAliasKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func parseSupportedEffortSuffix(model string) (base string, rawSuffix string, ok bool) {
	parsed := thinking.ParseSuffix(model)
	if !parsed.HasSuffix {
		return model, "", false
	}
	if _, valid := thinking.ParseNumericSuffix(parsed.RawSuffix); valid {
		return parsed.ModelName, parsed.RawSuffix, true
	}
	if _, valid := thinking.ParseLevelSuffix(parsed.RawSuffix); valid {
		return parsed.ModelName, parsed.RawSuffix, true
	}
	if _, valid := thinking.ParseSpecialSuffix(parsed.RawSuffix); valid {
		return parsed.ModelName, parsed.RawSuffix, true
	}
	return model, "", false
}
