// Package config provides configuration management for the CLI Proxy API server.
// It re-exports from internal/config for unified configuration management.
package config

import internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"

// Config is an alias to internal/config.Config - the canonical configuration type.
type Config = internalconfig.Config

// SDKConfig is an alias to internal/config.SDKConfig.
type SDKConfig = internalconfig.SDKConfig

// StreamingConfig is an alias to internal/config.StreamingConfig.
type StreamingConfig = internalconfig.StreamingConfig

// TLSConfig is an alias to internal/config.TLSConfig.
type TLSConfig = internalconfig.TLSConfig

// PprofConfig is an alias to internal/config.PprofConfig.
type PprofConfig = internalconfig.PprofConfig

// RemoteManagement is an alias to internal/config.RemoteManagement.
type RemoteManagement = internalconfig.RemoteManagement

// QuotaExceeded is an alias to internal/config.QuotaExceeded.
type QuotaExceeded = internalconfig.QuotaExceeded

// RoutingConfig is an alias to internal/config.RoutingConfig.
type RoutingConfig = internalconfig.RoutingConfig

// OAuthModelAlias is an alias to internal/config.OAuthModelAlias.
type OAuthModelAlias = internalconfig.OAuthModelAlias

// AmpModelMapping is an alias to internal/config.AmpModelMapping.
type AmpModelMapping = internalconfig.AmpModelMapping

// AmpCode is an alias to internal/config.AmpCode.
type AmpCode = internalconfig.AmpCode

// AmpUpstreamAPIKeyEntry is an alias to internal/config.AmpUpstreamAPIKeyEntry.
type AmpUpstreamAPIKeyEntry = internalconfig.AmpUpstreamAPIKeyEntry

// PayloadConfig is an alias to internal/config.PayloadConfig.
type PayloadConfig = internalconfig.PayloadConfig

// PayloadRule is an alias to internal/config.PayloadRule.
type PayloadRule = internalconfig.PayloadRule

// PayloadFilterRule is an alias to internal/config.PayloadFilterRule.
type PayloadFilterRule = internalconfig.PayloadFilterRule

// PayloadModelRule is an alias to internal/config.PayloadModelRule.
type PayloadModelRule = internalconfig.PayloadModelRule

// CloakConfig is an alias to internal/config.CloakConfig.
type CloakConfig = internalconfig.CloakConfig

// ClaudeHeaderDefaults is an alias to internal/config.ClaudeHeaderDefaults.
type ClaudeHeaderDefaults = internalconfig.ClaudeHeaderDefaults

// ClaudeKey is an alias to internal/config.ClaudeKey.
type ClaudeKey = internalconfig.ClaudeKey

// ClaudeModel is an alias to internal/config.ClaudeModel.
type ClaudeModel = internalconfig.ClaudeModel

// CodexKey is an alias to internal/config.CodexKey.
type CodexKey = internalconfig.CodexKey

// CodexModel is an alias to internal/config.CodexModel.
type CodexModel = internalconfig.CodexModel

// GeminiKey is an alias to internal/config.GeminiKey.
type GeminiKey = internalconfig.GeminiKey

// GeminiModel is an alias to internal/config.GeminiModel.
type GeminiModel = internalconfig.GeminiModel

// KiroKey is an alias to internal/config.KiroKey.
type KiroKey = internalconfig.KiroKey

// CursorKey is an alias to internal/config.CursorKey.
type CursorKey = internalconfig.CursorKey

// OAICompatProviderConfig is an alias to internal/config.OAICompatProviderConfig.
type OAICompatProviderConfig = internalconfig.OAICompatProviderConfig

// ProviderSpec is an alias to internal/config.ProviderSpec.
type ProviderSpec = internalconfig.ProviderSpec

// VertexCompatKey is an alias to internal/config.VertexCompatKey.
type VertexCompatKey = internalconfig.VertexCompatKey

// VertexCompatModel is an alias to internal/config.VertexCompatModel.
type VertexCompatModel = internalconfig.VertexCompatModel

// OpenAICompatibility is an alias to internal/config.OpenAICompatibility.
type OpenAICompatibility = internalconfig.OpenAICompatibility

// OpenAICompatibilityAPIKey is an alias to internal/config.OpenAICompatibilityAPIKey.
type OpenAICompatibilityAPIKey = internalconfig.OpenAICompatibilityAPIKey

// OpenAICompatibilityModel is an alias to internal/config.OpenAICompatibilityModel.
type OpenAICompatibilityModel = internalconfig.OpenAICompatibilityModel

// MiniMaxKey is an alias to internal/config.MiniMaxKey.
type MiniMaxKey = internalconfig.MiniMaxKey

// DeepSeekKey is an alias to internal/config.DeepSeekKey.
type DeepSeekKey = internalconfig.DeepSeekKey

// GeneratedConfig is an alias to internal/config.GeneratedConfig.
type GeneratedConfig = internalconfig.GeneratedConfig

// Constants
const (
	DefaultPanelGitHubRepository = internalconfig.DefaultPanelGitHubRepository
	DefaultPprofAddr             = internalconfig.DefaultPprofAddr
)

// Function aliases
var (
	LoadConfig                                  = internalconfig.LoadConfig
	LoadConfigOptional                          = internalconfig.LoadConfigOptional
	SaveConfigPreserveComments                  = internalconfig.SaveConfigPreserveComments
	SaveConfigPreserveCommentsUpdateNestedScalar = internalconfig.SaveConfigPreserveCommentsUpdateNestedScalar
	NormalizeCommentIndentation                 = internalconfig.NormalizeCommentIndentation
	NormalizeHeaders                            = internalconfig.NormalizeHeaders
	NormalizeExcludedModels                     = internalconfig.NormalizeExcludedModels
	NormalizeOAuthExcludedModels                = internalconfig.NormalizeOAuthExcludedModels
)

// Helper functions
var (
	GetDedicatedProviders = internalconfig.GetDedicatedProviders
	GetPremadeProviders   = internalconfig.GetPremadeProviders
	GetProviderByName     = internalconfig.GetProviderByName
)

// GroqKey is an alias to internal/config.GroqKey.
type GroqKey = internalconfig.GroqKey

// MistralKey is an alias to internal/config.MistralKey.
type MistralKey = internalconfig.MistralKey

// SiliconFlowKey is an alias to internal/config.SiliconFlowKey.
type SiliconFlowKey = internalconfig.SiliconFlowKey

// OpenRouterKey is an alias to internal/config.OpenRouterKey.
type OpenRouterKey = internalconfig.OpenRouterKey

// TogetherKey is an alias to internal/config.TogetherKey.
type TogetherKey = internalconfig.TogetherKey

// FireworksKey is an alias to internal/config.FireworksKey.
type FireworksKey = internalconfig.FireworksKey

// NovitaKey is an alias to internal/config.NovitaKey.
type NovitaKey = internalconfig.NovitaKey
