package config

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/responsestools"
)

// ResponsesToolsConfig is the single authoritative source for core Responses
// client tool protocol handling. There is no second policy in models.json,
// model aliases, or plugin YAML that can override it.
type ResponsesToolsConfig struct {
	// Enabled toggles core Responses tools adaptation. Default false preserves
	// legacy behavior on every route until operators opt in per route.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Limits bounds retained protocol state. Zero values normalize to defaults.
	Limits ResponsesToolsLimits `yaml:"limits" json:"limits"`
	// Routes binds handling strategies to exact upstream routes. Empty routes
	// with enabled:false is equivalent to the feature being off.
	Routes []ResponsesToolsRoute `yaml:"routes" json:"routes"`
}

// ResponsesToolsLimits mirrors responsestools.Limits in kebab-case YAML.
type ResponsesToolsLimits struct {
	MaxActiveToolBytes      int `yaml:"max-active-tool-bytes" json:"max-active-tool-bytes"`
	MaxActiveAttempts       int `yaml:"max-active-attempts" json:"max-active-attempts"`
	MaxStateBytes           int `yaml:"max-state-bytes" json:"max-state-bytes"`
	MaxAttemptBytes         int `yaml:"max-attempt-bytes" json:"max-attempt-bytes"`
	MaxSchemaExpansionBytes int `yaml:"max-schema-expansion-bytes" json:"max-schema-expansion-bytes"`
	MaxSchemaExpansionNodes int `yaml:"max-schema-expansion-nodes" json:"max-schema-expansion-nodes"`
	MaxDepth                int `yaml:"max-depth" json:"max-depth"`
}

// ResponsesToolsRoute binds strategies to one exact upstream route.
type ResponsesToolsRoute struct {
	Match         ResponsesToolsMatch  `yaml:"match" json:"match"`
	ClientSearch  string               `yaml:"client-search" json:"client-search"`
	CustomTools   string               `yaml:"custom-tools" json:"custom-tools"`
	CustomGrammar string               `yaml:"custom-grammar" json:"custom-grammar"`
	Schema        ResponsesToolsSchema `yaml:"schema" json:"schema"`
}

// ResponsesToolsMatch identifies one exact upstream route. All match fields
// are required; the first release supports no wildcards or regex matching.
type ResponsesToolsMatch struct {
	Provider       string `yaml:"provider" json:"provider"`
	AuthKind       string `yaml:"auth-kind" json:"auth-kind"`
	UpstreamModel  string `yaml:"upstream-model" json:"upstream-model"`
	UpstreamFormat string `yaml:"upstream-format" json:"upstream-format"`
	BaseURL        string `yaml:"base-url" json:"base-url"`
}

// ResponsesToolsSchema tunes strict-schema handling for one route.
type ResponsesToolsSchema struct {
	CompleteSearchRequired bool   `yaml:"complete-search-required" json:"complete-search-required"`
	LocalRefs              string `yaml:"local-refs" json:"local-refs"`
}

// NormalizeResponsesToolsConfig applies documented defaults: limits fall back
// to reference budgets, strategy fields default to inherit/preserve, and an
// empty route list with enabled:false stays fully off.
func (cfg *Config) NormalizeResponsesToolsConfig() {
	if cfg == nil {
		return
	}
	defaults := responsestools.DefaultLimits()
	limits := &cfg.ResponsesTools.Limits
	if limits.MaxActiveToolBytes <= 0 {
		limits.MaxActiveToolBytes = defaults.MaxActiveToolBytes
	}
	if limits.MaxActiveAttempts <= 0 {
		limits.MaxActiveAttempts = defaults.MaxActiveAttempts
	}
	if limits.MaxStateBytes <= 0 {
		limits.MaxStateBytes = defaults.MaxStateBytes
	}
	if limits.MaxAttemptBytes <= 0 {
		limits.MaxAttemptBytes = defaults.MaxAttemptBytes
	}
	if limits.MaxSchemaExpansionBytes <= 0 {
		limits.MaxSchemaExpansionBytes = defaults.MaxSchemaExpansionBytes
	}
	if limits.MaxSchemaExpansionNodes <= 0 {
		limits.MaxSchemaExpansionNodes = defaults.MaxSchemaExpansionNodes
	}
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = defaults.MaxDepth
	}
	for index := range cfg.ResponsesTools.Routes {
		route := &cfg.ResponsesTools.Routes[index]
		if strings.TrimSpace(route.ClientSearch) == "" {
			route.ClientSearch = string(responsestools.ClientSearchInherit)
		}
		if strings.TrimSpace(route.CustomTools) == "" {
			route.CustomTools = string(responsestools.CustomToolsInherit)
		}
		if strings.TrimSpace(route.CustomGrammar) == "" {
			route.CustomGrammar = string(responsestools.CustomGrammarReject)
		}
		if strings.TrimSpace(route.Schema.LocalRefs) == "" {
			route.Schema.LocalRefs = string(responsestools.LocalRefsPreserve)
		}
	}
}

// ValidateResponsesToolsConfig compiles the configured policy and rejects
// unknown enums, illegal combinations, conflicting overlaps, and simultaneous
// use with the legacy shim plugin.
func (cfg *Config) ValidateResponsesToolsConfig() error {
	if cfg == nil {
		return nil
	}
	policy, err := cfg.ResponsesTools.Compile()
	if err != nil {
		return err
	}
	if !cfg.ResponsesTools.Enabled || len(policy.Routes) == 0 {
		return nil
	}
	if cfg.Plugins.Enabled {
		for id, instance := range cfg.Plugins.Configs {
			if id != "codex-tool-search-shim" {
				continue
			}
			enabled := instance.Enabled != nil && *instance.Enabled
			if enabled {
				return fmt.Errorf("responses-tools core and the codex-tool-search-shim plugin must not be enabled together; disable one of them")
			}
		}
	}
	return nil
}

// Compile translates the config snapshot into the immutable core policy.
func (c ResponsesToolsConfig) Compile() (responsestools.Policy, error) {
	policy := responsestools.Policy{
		Enabled: c.Enabled,
		Limits: responsestools.Limits{
			MaxActiveToolBytes:      c.Limits.MaxActiveToolBytes,
			MaxActiveAttempts:       c.Limits.MaxActiveAttempts,
			MaxStateBytes:           c.Limits.MaxStateBytes,
			MaxAttemptBytes:         c.Limits.MaxAttemptBytes,
			MaxSchemaExpansionBytes: c.Limits.MaxSchemaExpansionBytes,
			MaxSchemaExpansionNodes: c.Limits.MaxSchemaExpansionNodes,
			MaxDepth:                c.Limits.MaxDepth,
		},
	}
	for _, route := range c.Routes {
		policy.Routes = append(policy.Routes, responsestools.RoutePolicy{
			Match: responsestools.RouteMatch{
				Provider:       route.Match.Provider,
				AuthKind:       route.Match.AuthKind,
				UpstreamModel:  route.Match.UpstreamModel,
				UpstreamFormat: route.Match.UpstreamFormat,
				BaseURL:        route.Match.BaseURL,
			},
			ClientSearch:  responsestools.ClientSearchMode(route.ClientSearch),
			CustomTools:   responsestools.CustomToolsMode(route.CustomTools),
			CustomGrammar: responsestools.CustomGrammarMode(route.CustomGrammar),
			Schema: responsestools.SchemaPolicy{
				CompleteSearchRequired: route.Schema.CompleteSearchRequired,
				LocalRefs:              responsestools.LocalRefsMode(route.Schema.LocalRefs),
			},
		})
	}
	return responsestools.CompilePolicy(policy)
}
