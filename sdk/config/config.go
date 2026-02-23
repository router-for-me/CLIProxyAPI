// Package config provides the public SDK configuration API.
//
// It re-exports the server configuration types and helpers so external projects can
// embed CLIProxyAPI without importing internal packages.
package config

import pkgconfig "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"

type SDKConfig = pkgconfig.SDKConfig

type Config = pkgconfig.Config

type StreamingConfig = pkgconfig.StreamingConfig
type TLSConfig = pkgconfig.TLSConfig
type RemoteManagement = pkgconfig.RemoteManagement
type AmpCode = pkgconfig.AmpCode
type OAuthModelAlias = pkgconfig.OAuthModelAlias
type PayloadConfig = pkgconfig.PayloadConfig
type PayloadRule = pkgconfig.PayloadRule
type PayloadFilterRule = pkgconfig.PayloadFilterRule
type PayloadModelRule = pkgconfig.PayloadModelRule

type GeminiKey = pkgconfig.GeminiKey
type CodexKey = pkgconfig.CodexKey
type ClaudeKey = pkgconfig.ClaudeKey
type VertexCompatKey = pkgconfig.VertexCompatKey
type VertexCompatModel = pkgconfig.VertexCompatModel
type OpenAICompatibility = pkgconfig.OpenAICompatibility
type OpenAICompatibilityAPIKey = pkgconfig.OpenAICompatibilityAPIKey
type OpenAICompatibilityModel = pkgconfig.OpenAICompatibilityModel

type TLS = pkgconfig.TLSConfig

const (
	DefaultPanelGitHubRepository = pkgconfig.DefaultPanelGitHubRepository
)

func LoadConfig(configFile string) (*Config, error) { return pkgconfig.LoadConfig(configFile) }

func LoadConfigOptional(configFile string, optional bool) (*Config, error) {
	return pkgconfig.LoadConfigOptional(configFile, optional)
}

func SaveConfigPreserveComments(configFile string, cfg *Config) error {
	return pkgconfig.SaveConfigPreserveComments(configFile, cfg)
}

func SaveConfigPreserveCommentsUpdateNestedScalar(configFile string, path []string, value string) error {
	return pkgconfig.SaveConfigPreserveCommentsUpdateNestedScalar(configFile, path, value)
}

func NormalizeCommentIndentation(data []byte) []byte {
	return pkgconfig.NormalizeCommentIndentation(data)
}
