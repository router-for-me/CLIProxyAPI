package opencode

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
)

// registerRoutes wires the OpenCode route namespace into the Gin engine.
//
// It exposes two surfaces, both backed by the existing SDK handlers:
//
//   - Merged routes under /opencode/v1 (and /opencode/v1beta for Gemini), where the
//     inference backend is resolved from the request model. A single OpenCode custom
//     provider baseURL of http://host/opencode/v1 serves OpenAI Chat Completions,
//     OpenAI Responses, and Anthropic Messages.
//   - Provider-scoped routes under /opencode/provider/:provider for explicit protocol
//     selection, mirroring the Amp module's /api/provider/:provider aliases.
//
// All POST inference routes are wrapped with the model-mapping layer; model-listing
// routes are not (they have no body to rewrite).
func (m *OpenCodeModule) registerRoutes(engine *gin.Engine, baseHandler *handlers.BaseAPIHandler, auth gin.HandlerFunc) {
	openaiHandlers := openai.NewOpenAIAPIHandler(baseHandler)
	openaiResponsesHandlers := openai.NewOpenAIResponsesAPIHandler(baseHandler)
	claudeHandlers := claude.NewClaudeCodeAPIHandler(baseHandler)
	geminiHandlers := gemini.NewGeminiAPIHandler(baseHandler)

	mapper := NewMappingHandler(m.modelMapper, m.forceModelMappings)

	// modelsHandler dispatches the model-listing endpoint to the right family.
	// For provider-scoped routes it keys off the :provider path parameter; for
	// merged routes (empty provider) it defaults to the OpenAI-compatible listing.
	modelsHandler := func(c *gin.Context) {
		switch strings.ToLower(c.Param("provider")) {
		case "anthropic", "claude":
			claudeHandlers.ClaudeModels(c)
		case "google", "gemini":
			geminiHandlers.GeminiModels(c)
		default:
			openaiHandlers.OpenAIModels(c)
		}
	}

	// attach registers the full protocol surface onto the given route group.
	attach := func(g *gin.RouterGroup) {
		g.GET("/models", modelsHandler)

		v1 := g.Group("/v1")
		{
			v1.GET("/models", modelsHandler)
			// OpenAI Chat Completions (@ai-sdk/openai-compatible).
			v1.POST("/chat/completions", mapper.Wrap(openaiHandlers.ChatCompletions))
			v1.POST("/completions", mapper.Wrap(openaiHandlers.Completions))
			// OpenAI Responses (@ai-sdk/openai).
			v1.POST("/responses", mapper.Wrap(openaiResponsesHandlers.Responses))
			// Anthropic Messages (@ai-sdk/anthropic).
			v1.POST("/messages", mapper.Wrap(claudeHandlers.ClaudeMessages))
			v1.POST("/messages/count_tokens", mapper.Wrap(claudeHandlers.ClaudeCountTokens))
		}

		// Gemini native API for parity with the merged /v1beta endpoints.
		v1beta := g.Group("/v1beta")
		{
			v1beta.GET("/models", geminiHandlers.GeminiModels)
			v1beta.POST("/models/*action", mapper.Wrap(geminiHandlers.GeminiHandler))
			v1beta.GET("/models/*action", geminiHandlers.GeminiGetHandler)
		}
	}

	root := engine.Group("/opencode")
	if auth != nil {
		root.Use(auth)
	}

	// Merged surface: /opencode/v1/... and /opencode/v1beta/...
	attach(root)

	// Provider-scoped surface: /opencode/provider/:provider/...
	attach(root.Group("/provider/:provider"))
}
