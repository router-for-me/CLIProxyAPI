// Package config provides the public SDK configuration API.
//
// It re-exports the server configuration types and helpers so external projects can
// embed CLIProxyAPI without importing internal packages.
package config

import llmproxyconfig "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"

type SDKConfig = llmproxyconfig.SDKConfig

type Config = llmproxyconfig.Config

type StreamingConfig = llmproxyconfig.StreamingConfig
type TLSConfig = llmproxyconfig.TLSConfig
type RemoteManagement = llmproxyconfig.RemoteManagement
type AmpCode = llmproxyconfig.AmpCode
type OAuthModelAlias = llmproxyconfig.OAuthModelAlias
type PayloadConfig = llmproxyconfig.PayloadConfig
type PayloadRule = llmproxyconfig.PayloadRule
type PayloadFilterRule = llmproxyconfig.PayloadFilterRule
type PayloadModelRule = llmproxyconfig.PayloadModelRule

type GeminiKey = llmproxyconfig.GeminiKey
type CodexKey = llmproxyconfig.CodexKey
type ClaudeKey = llmproxyconfig.ClaudeKey
type VertexCompatKey = llmproxyconfig.VertexCompatKey
type VertexCompatModel = llmproxyconfig.VertexCompatModel
type OpenAICompatibility = llmproxyconfig.OpenAICompatibility
type OpenAICompatibilityAPIKey = llmproxyconfig.OpenAICompatibilityAPIKey
type OpenAICompatibilityModel = llmproxyconfig.OpenAICompatibilityModel

type TLS = llmproxyconfig.TLSConfig

const (
	DefaultPanelGitHubRepository = llmproxyconfig.DefaultPanelGitHubRepository
)

func LoadConfig(configFile string) (*Config, error) { return llmproxyconfig.LoadConfig(configFile) }

func LoadConfigOptional(configFile string, optional bool) (*Config, error) {
	return llmproxyconfig.LoadConfigOptional(configFile, optional)
}

func ParseConfigBytes(data []byte) (*Config, error) { return llmproxyconfig.ParseConfigBytes(data) }

func SaveConfigPreserveComments(configFile string, cfg *Config) error {
	return llmproxyconfig.SaveConfigPreserveComments(configFile, cfg)
}

func SaveConfigPreserveCommentsUpdateNestedScalar(configFile string, path []string, value string) error {
	return llmproxyconfig.SaveConfigPreserveCommentsUpdateNestedScalar(configFile, path, value)
}

func NormalizeCommentIndentation(data []byte) []byte {
	return llmproxyconfig.NormalizeCommentIndentation(data)
}
