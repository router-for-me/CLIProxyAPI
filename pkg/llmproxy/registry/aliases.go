// Package registry provides type aliases to the internal implementation.
// This allows both "internal/registry" and "pkg/llmproxy/registry" import paths to work seamlessly.
package registry

import internalregistry "github.com/kooshapari/cliproxyapi-plusplus/v6/internal/registry"

// Type aliases for exported types
type ModelInfo = internalregistry.ModelInfo
type ThinkingSupport = internalregistry.ThinkingSupport
type ModelRegistration = internalregistry.ModelRegistration
type ModelRegistryHook = internalregistry.ModelRegistryHook
type ModelRegistry = internalregistry.ModelRegistry
type AntigravityModelConfig = internalregistry.AntigravityModelConfig

// Function aliases for exported functions
var (
	GetGlobalRegistry                     = internalregistry.GetGlobalRegistry
	LookupModelInfo                       = internalregistry.LookupModelInfo
	GetStaticModelDefinitionsByChannel    = internalregistry.GetStaticModelDefinitionsByChannel
	LookupStaticModelInfo                 = internalregistry.LookupStaticModelInfo
	GetGitHubCopilotModels                = internalregistry.GetGitHubCopilotModels
	GetKiroModels                         = internalregistry.GetKiroModels
	GetAmazonQModels                      = internalregistry.GetAmazonQModels
	GetClaudeModels                       = internalregistry.GetClaudeModels
	GetGeminiModels                       = internalregistry.GetGeminiModels
	GetGeminiVertexModels                 = internalregistry.GetGeminiVertexModels
	GetGeminiCLIModels                    = internalregistry.GetGeminiCLIModels
	GetAIStudioModels                     = internalregistry.GetAIStudioModels
	GetOpenAIModels                       = internalregistry.GetOpenAIModels
	GetAntigravityModelConfig             = internalregistry.GetAntigravityModelConfig
	GetQwenModels                         = internalregistry.GetQwenModels
	GetIFlowModels                        = internalregistry.GetIFlowModels
	GetKimiModels                         = internalregistry.GetKimiModels
	GetCursorModels                       = internalregistry.GetCursorModels
	GetMiniMaxModels                      = internalregistry.GetMiniMaxModels
	GetRooModels                          = internalregistry.GetRooModels
	GetDeepSeekModels                     = internalregistry.GetDeepSeekModels
	GetGroqModels                         = internalregistry.GetGroqModels
	GetMistralModels                      = internalregistry.GetMistralModels
	GetSiliconFlowModels                  = internalregistry.GetSiliconFlowModels
	GetOpenRouterModels                   = internalregistry.GetOpenRouterModels
	GetTogetherModels                     = internalregistry.GetTogetherModels
	GetFireworksModels                    = internalregistry.GetFireworksModels
	GetNovitaModels                       = internalregistry.GetNovitaModels
)
