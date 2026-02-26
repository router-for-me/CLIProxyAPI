// Package config provides the public SDK configuration API.
//
// It re-exports the server configuration types from pkg/llmproxy/config
// so external projects can embed CLIProxyAPI without importing internal packages.
package config

import llmproxyconfig "github.com/kooshapari/cliproxyapi-plusplus/v6/internal/config"

type SDKConfig = llmproxyconfig.SDKConfig
type Config = llmproxyconfig.Config
type StreamingConfig = llmproxyconfig.StreamingConfig
type TLSConfig = llmproxyconfig.TLSConfig
type PprofConfig = llmproxyconfig.PprofConfig
type RemoteManagement = llmproxyconfig.RemoteManagement
type QuotaExceeded = llmproxyconfig.QuotaExceeded
type RoutingConfig = llmproxyconfig.RoutingConfig
type OAuthModelAlias = llmproxyconfig.OAuthModelAlias
type AmpModelMapping = llmproxyconfig.AmpModelMapping
type AmpCode = llmproxyconfig.AmpCode
type AmpUpstreamAPIKeyEntry = llmproxyconfig.AmpUpstreamAPIKeyEntry
type PayloadConfig = llmproxyconfig.PayloadConfig
type PayloadRule = llmproxyconfig.PayloadRule
type PayloadFilterRule = llmproxyconfig.PayloadFilterRule
type PayloadModelRule = llmproxyconfig.PayloadModelRule
type CloakConfig = llmproxyconfig.CloakConfig
type ClaudeKey = llmproxyconfig.ClaudeKey
type ClaudeModel = llmproxyconfig.ClaudeModel
type CodexKey = llmproxyconfig.CodexKey
type CodexModel = llmproxyconfig.CodexModel
type GeminiKey = llmproxyconfig.GeminiKey
type GeminiModel = llmproxyconfig.GeminiModel
type KiroKey = llmproxyconfig.KiroKey
type CursorKey = llmproxyconfig.CursorKey
type OAICompatProviderConfig = llmproxyconfig.OAICompatProviderConfig
type ProviderSpec = llmproxyconfig.ProviderSpec
type VertexCompatKey = llmproxyconfig.VertexCompatKey
type VertexCompatModel = llmproxyconfig.VertexCompatModel
type OpenAICompatibility = llmproxyconfig.OpenAICompatibility
type OpenAICompatibilityAPIKey = llmproxyconfig.OpenAICompatibilityAPIKey
type OpenAICompatibilityModel = llmproxyconfig.OpenAICompatibilityModel
type MiniMaxKey = llmproxyconfig.MiniMaxKey
type DeepSeekKey = llmproxyconfig.DeepSeekKey

type TLS = llmproxyconfig.TLSConfig

const DefaultPanelGitHubRepository = llmproxyconfig.DefaultPanelGitHubRepository

func LoadConfig(configFile string) (*Config, error) { return llmproxyconfig.LoadConfig(configFile) }

func LoadConfigOptional(configFile string, optional bool) (*Config, error) {
	return llmproxyconfig.LoadConfigOptional(configFile, optional)
}

func SaveConfigPreserveComments(configFile string, cfg *Config) error {
	return llmproxyconfig.SaveConfigPreserveComments(configFile, cfg)
}

func SaveConfigPreserveCommentsUpdateNestedScalar(configFile string, path []string, value string) error {
	return llmproxyconfig.SaveConfigPreserveCommentsUpdateNestedScalar(configFile, path, value)
}

func NormalizeCommentIndentation(data []byte) []byte {
	return llmproxyconfig.NormalizeCommentIndentation(data)
}

func NormalizeHeaders(headers map[string]string) map[string]string {
	return llmproxyconfig.NormalizeHeaders(headers)
}

func NormalizeExcludedModels(models []string) []string {
	return llmproxyconfig.NormalizeExcludedModels(models)
}

func NormalizeOAuthExcludedModels(entries map[string][]string) map[string][]string {
	return llmproxyconfig.NormalizeOAuthExcludedModels(entries)
}
