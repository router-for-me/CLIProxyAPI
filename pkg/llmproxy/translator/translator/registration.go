package translator

import (
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/constant"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/interfaces"

	// Antigravity translator providers
	antigravityclaude "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/antigravity/claude"
	antigravitygemini "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/antigravity/gemini"
	antigravityopenai "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/antigravity/openai/chat-completions"
	antigravityopenairesponses "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/antigravity/openai/responses"

	// Claude translator providers
	claudegemini "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/claude/gemini"
	claudegeminicli "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/claude/gemini-cli"
	claudeopenai "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/claude/openai/chat-completions"
	claudeopenairesponses "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/claude/openai/responses"

	// Codex translator providers
	codexclaude "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/codex/claude"
	codexgemini "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/codex/gemini"
	codexgeminicli "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/codex/gemini-cli"
	codexopenai "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/codex/openai/chat-completions"
	codexopenairesponses "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/codex/openai/responses"

	// Gemini translator providers
	geminiclaude "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini/claude"
	geminigemini "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini/gemini"
	geminigeminicli "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini/gemini-cli"
	geminiopenai "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini/openai/chat-completions"
	geminiopenairesponses "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini/openai/responses"

	// Gemini CLI translator providers
	geminicliiclaude "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini-cli/claude"
	geminiigemini "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini-cli/gemini"
	geminicliiopenai "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini-cli/openai/chat-completions"
	geminicliiopenairesponses "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini-cli/openai/responses"

	// Kiro translator providers
	kiroclaude "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/kiro/claude"
	kiroopenai "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/kiro/openai"

	// OpenAI translator providers
	openai_claude "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/openai/claude"
	openaigemini "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/openai/gemini"
	openaigeminicli "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/openai/gemini-cli"
	openaiopenai "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/openai/openai/chat-completions"
	openairesponses "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/openai/openai/responses"
)

// init registers all translator conversion functions with the translator registry.
// This centralized registration ensures all providers are properly initialized
// when the translator package is imported.
func init() {
	// Antigravity -> Claude
	Register(
		constant.Claude,
		constant.Antigravity,
		antigravityclaude.ConvertClaudeRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:     antigravityclaude.ConvertAntigravityResponseToClaude,
			NonStream:  antigravityclaude.ConvertAntigravityResponseToClaudeNonStream,
			TokenCount: antigravityclaude.ClaudeTokenCount,
		},
	)

	// Antigravity -> Gemini
	Register(
		constant.Gemini,
		constant.Antigravity,
		antigravitygemini.ConvertGeminiRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:     antigravitygemini.ConvertAntigravityResponseToGemini,
			NonStream:  antigravitygemini.ConvertAntigravityResponseToGeminiNonStream,
			TokenCount: antigravitygemini.GeminiTokenCount,
		},
	)

	// Antigravity -> OpenAI
	Register(
		constant.OpenAI,
		constant.Antigravity,
		antigravityopenai.ConvertOpenAIRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:    antigravityopenai.ConvertAntigravityResponseToOpenAI,
			NonStream: antigravityopenai.ConvertAntigravityResponseToOpenAINonStream,
		},
	)

	// Antigravity -> OpenAI Responses
	Register(
		constant.OpenaiResponse,
		constant.Antigravity,
		antigravityopenairesponses.ConvertOpenAIResponsesRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:    antigravityopenairesponses.ConvertAntigravityResponseToOpenAIResponses,
			NonStream: antigravityopenairesponses.ConvertAntigravityResponseToOpenAIResponsesNonStream,
		},
	)

	// Claude -> Gemini
	Register(
		constant.Gemini,
		constant.Claude,
		claudegemini.ConvertGeminiRequestToClaude,
		interfaces.TranslateResponse{
			Stream:     claudegemini.ConvertClaudeResponseToGemini,
			NonStream:  claudegemini.ConvertClaudeResponseToGeminiNonStream,
			TokenCount: claudegemini.GeminiTokenCount,
		},
	)

	// Claude -> Gemini CLI
	Register(
		constant.GeminiCLI,
		constant.Claude,
		claudegeminicli.ConvertGeminiCLIRequestToClaude,
		interfaces.TranslateResponse{
			Stream:     claudegeminicli.ConvertClaudeResponseToGeminiCLI,
			NonStream:  claudegeminicli.ConvertClaudeResponseToGeminiCLINonStream,
			TokenCount: claudegeminicli.GeminiCLITokenCount,
		},
	)

	// Claude -> OpenAI
	Register(
		constant.OpenAI,
		constant.Claude,
		claudeopenai.ConvertOpenAIRequestToClaude,
		interfaces.TranslateResponse{
			Stream:    claudeopenai.ConvertClaudeResponseToOpenAI,
			NonStream: claudeopenai.ConvertClaudeResponseToOpenAINonStream,
		},
	)

	// Claude -> OpenAI Responses
	Register(
		constant.OpenaiResponse,
		constant.Claude,
		claudeopenairesponses.ConvertOpenAIResponsesRequestToClaude,
		interfaces.TranslateResponse{
			Stream:    claudeopenairesponses.ConvertClaudeResponseToOpenAIResponses,
			NonStream: claudeopenairesponses.ConvertClaudeResponseToOpenAIResponsesNonStream,
		},
	)

	// Codex -> Claude
	Register(
		constant.Claude,
		constant.Codex,
		codexclaude.ConvertClaudeRequestToCodex,
		interfaces.TranslateResponse{
			Stream:     ToResponseStreamTransform(codexclaude.ConvertCodexResponseToClaude),
			NonStream:  ToResponseNonStreamTransformFromBytes(codexclaude.ConvertCodexResponseToClaudeNonStream),
			TokenCount: ToResponseTokenCountTransform(codexclaude.ClaudeTokenCount),
		},
	)

	// Codex -> Gemini
	Register(
		constant.Gemini,
		constant.Codex,
		codexgemini.ConvertGeminiRequestToCodex,
		interfaces.TranslateResponse{
			Stream:     codexgemini.ConvertCodexResponseToGemini,
			NonStream:  codexgemini.ConvertCodexResponseToGeminiNonStream,
			TokenCount: codexgemini.GeminiTokenCount,
		},
	)

	// Codex -> Gemini CLI
	Register(
		constant.GeminiCLI,
		constant.Codex,
		codexgeminicli.ConvertGeminiCLIRequestToCodex,
		interfaces.TranslateResponse{
			Stream:     ToResponseStreamTransform(codexgeminicli.ConvertCodexResponseToGeminiCLI),
			NonStream:  ToResponseNonStreamTransform(codexgeminicli.ConvertCodexResponseToGeminiCLINonStream),
			TokenCount: ToResponseTokenCountTransformFromBytes(codexgeminicli.GeminiCLITokenCount),
		},
	)

	// Codex -> OpenAI
	Register(
		constant.OpenAI,
		constant.Codex,
		codexopenai.ConvertOpenAIRequestToCodex,
		interfaces.TranslateResponse{
			Stream:    codexopenai.ConvertCodexResponseToOpenAI,
			NonStream: codexopenai.ConvertCodexResponseToOpenAINonStream,
		},
	)

	// Codex -> OpenAI Responses
	Register(
		constant.OpenaiResponse,
		constant.Codex,
		codexopenairesponses.ConvertOpenAIResponsesRequestToCodex,
		interfaces.TranslateResponse{
			Stream:    ToResponseStreamTransform(codexopenairesponses.ConvertCodexResponseToOpenAIResponses),
			NonStream: ToResponseNonStreamTransform(codexopenairesponses.ConvertCodexResponseToOpenAIResponsesNonStream),
		},
	)

	// Gemini -> Claude
	Register(
		constant.Claude,
		constant.Gemini,
		geminiclaude.ConvertClaudeRequestToGemini,
		interfaces.TranslateResponse{
			Stream:     geminiclaude.ConvertGeminiResponseToClaude,
			NonStream:  geminiclaude.ConvertGeminiResponseToClaudeNonStream,
			TokenCount: geminiclaude.ClaudeTokenCount,
		},
	)

	// Gemini -> Gemini (passthrough)
	Register(
		constant.Gemini,
		constant.Gemini,
		geminigemini.ConvertGeminiRequestToGemini,
		interfaces.TranslateResponse{
			Stream:     geminigemini.PassthroughGeminiResponseStream,
			NonStream:  geminigemini.PassthroughGeminiResponseNonStream,
			TokenCount: geminigemini.GeminiTokenCount,
		},
	)

	// Gemini -> Gemini CLI
	Register(
		constant.GeminiCLI,
		constant.Gemini,
		geminigeminicli.ConvertGeminiCLIRequestToGemini,
		interfaces.TranslateResponse{
			Stream:     geminigeminicli.ConvertGeminiResponseToGeminiCLI,
			NonStream:  geminigeminicli.ConvertGeminiResponseToGeminiCLINonStream,
			TokenCount: geminigeminicli.GeminiCLITokenCount,
		},
	)

	// Gemini -> OpenAI
	Register(
		constant.OpenAI,
		constant.Gemini,
		geminiopenai.ConvertOpenAIRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    geminiopenai.ConvertGeminiResponseToOpenAI,
			NonStream: geminiopenai.ConvertGeminiResponseToOpenAINonStream,
		},
	)

	// Gemini -> OpenAI Responses
	Register(
		constant.OpenaiResponse,
		constant.Gemini,
		geminiopenairesponses.ConvertOpenAIResponsesRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    ToResponseStreamTransform(geminiopenairesponses.ConvertGeminiResponseToOpenAIResponses),
			NonStream: ToResponseNonStreamTransformFromBytes(geminiopenairesponses.ConvertGeminiResponseToOpenAIResponsesNonStream),
		},
	)

	// Gemini CLI -> Claude
	Register(
		constant.Claude,
		constant.GeminiCLI,
		geminicliiclaude.ConvertClaudeRequestToCLI,
		interfaces.TranslateResponse{
			Stream:     geminicliiclaude.ConvertGeminiCLIResponseToClaude,
			NonStream:  geminicliiclaude.ConvertGeminiCLIResponseToClaudeNonStream,
			TokenCount: geminicliiclaude.ClaudeTokenCount,
		},
	)

	// Gemini CLI -> Gemini
	Register(
		constant.Gemini,
		constant.GeminiCLI,
		geminiigemini.ConvertGeminiRequestToGeminiCLI,
		interfaces.TranslateResponse{
			Stream:     geminiigemini.ConvertGeminiCliResponseToGemini,
			NonStream:  geminiigemini.ConvertGeminiCliResponseToGeminiNonStream,
			TokenCount: geminiigemini.GeminiTokenCount,
		},
	)

	// Gemini CLI -> OpenAI
	Register(
		constant.OpenAI,
		constant.GeminiCLI,
		geminicliiopenai.ConvertOpenAIRequestToGeminiCLI,
		interfaces.TranslateResponse{
			Stream:    geminicliiopenai.ConvertCliResponseToOpenAI,
			NonStream: geminicliiopenai.ConvertCliResponseToOpenAINonStream,
		},
	)

	// Gemini CLI -> OpenAI Responses
	Register(
		constant.OpenaiResponse,
		constant.GeminiCLI,
		geminicliiopenairesponses.ConvertOpenAIResponsesRequestToGeminiCLI,
		interfaces.TranslateResponse{
			Stream:    geminicliiopenairesponses.ConvertGeminiCLIResponseToOpenAIResponses,
			NonStream: geminicliiopenairesponses.ConvertGeminiCLIResponseToOpenAIResponsesNonStream,
		},
	)

	// Kiro -> Claude
	Register(
		constant.Claude,
		constant.Kiro,
		kiroclaude.ConvertClaudeRequestToKiro,
		interfaces.TranslateResponse{
			Stream:    kiroclaude.ConvertKiroStreamToClaude,
			NonStream: kiroclaude.ConvertKiroNonStreamToClaude,
		},
	)

	// Kiro -> OpenAI
	Register(
		constant.OpenAI,
		constant.Kiro,
		kiroopenai.ConvertOpenAIRequestToKiro,
		interfaces.TranslateResponse{
			Stream:    kiroopenai.ConvertKiroStreamToOpenAI,
			NonStream: kiroopenai.ConvertKiroNonStreamToOpenAI,
		},
	)

	// OpenAI -> Claude
	Register(
		constant.Claude,
		constant.OpenAI,
		openai_claude.ConvertClaudeRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:     ToResponseStreamTransform(openai_claude.ConvertOpenAIResponseToClaude),
			NonStream:  ToResponseNonStreamTransform(openai_claude.ConvertOpenAIResponseToClaudeNonStream),
			TokenCount: ToResponseTokenCountTransform(openai_claude.ClaudeTokenCount),
		},
	)

	// OpenAI -> Gemini
	Register(
		constant.Gemini,
		constant.OpenAI,
		openaigemini.ConvertGeminiRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:     openaigemini.ConvertOpenAIResponseToGemini,
			NonStream:  openaigemini.ConvertOpenAIResponseToGeminiNonStream,
			TokenCount: openaigemini.GeminiTokenCount,
		},
	)

	// OpenAI -> Gemini CLI
	Register(
		constant.GeminiCLI,
		constant.OpenAI,
		openaigeminicli.ConvertGeminiCLIRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:     openaigeminicli.ConvertOpenAIResponseToGeminiCLI,
			NonStream:  openaigeminicli.ConvertOpenAIResponseToGeminiCLINonStream,
			TokenCount: openaigeminicli.GeminiCLITokenCount,
		},
	)

	// OpenAI -> OpenAI (passthrough)
	Register(
		constant.OpenAI,
		constant.OpenAI,
		openaiopenai.ConvertOpenAIRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:    ToResponseStreamTransform(openaiopenai.ConvertOpenAIResponseToOpenAI),
			NonStream: ToResponseNonStreamTransform(openaiopenai.ConvertOpenAIResponseToOpenAINonStream),
		},
	)

	// OpenAI -> OpenAI Responses
	Register(
		constant.OpenaiResponse,
		constant.OpenAI,
		openairesponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions,
		interfaces.TranslateResponse{
			Stream:    ToResponseStreamTransform(openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses),
			NonStream: ToResponseNonStreamTransform(openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream),
		},
	)
}
