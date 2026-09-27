package config

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/responsestools"
)

// ResponsesToolsConfig is the single authoritative source for core Responses
// client tool protocol handling. There is no second policy in models.json,
// model aliases, or plugin YAML that can override it.
type ResponsesToolsConfig struct {
	// Enabled is the emergency gate for the whole feature. It is deliberately
	// tri-state: an absent value keeps the convention defaults on, while an
	// explicit false turns every route off without deleting configuration.
	Enabled *bool `yaml:"enabled" json:"enabled"`
	// Limits bounds retained protocol state. Zero values normalize to defaults.
	Limits ResponsesToolsLimits `yaml:"limits" json:"limits"`
	// Routes overrides the convention for exact upstream routes. An empty list
	// keeps the convention defaults, which already cover every route.
	Routes []ResponsesToolsRoute `yaml:"routes" json:"routes"`
}

// FeatureEnabled reports whether core Responses tools adaptation is active.
// Convention over configuration: absent configuration means enabled.
func (c ResponsesToolsConfig) FeatureEnabled() bool {
	return c.Enabled == nil || *c.Enabled
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
// are required; wildcards and regex matching are not supported.
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
// to reference budgets and strategy fields default to inherit/preserve, which
// resolve to the convention policy for the actual route.
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
// unknown enums, illegal combinations, and conflicting overlaps.
func (cfg *Config) ValidateResponsesToolsConfig() error {
	if cfg == nil {
		return nil
	}
	if _, err := cfg.ResponsesTools.Compile(); err != nil {
		return err
	}
	return nil
}

// Compile translates the config snapshot into the immutable core policy.
func (c ResponsesToolsConfig) Compile() (responsestools.Policy, error) {
	policy := responsestools.Policy{
		Enabled: c.FeatureEnabled(),
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
