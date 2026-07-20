// Package constant defines provider name constants used throughout the CLI Proxy API.
// These constants identify different AI service providers and their variants,
// ensuring consistent naming across the application.
package constant

const (
	// Gemini represents the Google Gemini provider identifier.
	Gemini = "gemini"

	// GeminiInteractions represents the native Google Interactions API provider identifier.
	GeminiInteractions = "gemini-interactions"

	// Codex represents the OpenAI Codex provider identifier.
	Codex = "codex"

	// Claude represents the Anthropic Claude provider identifier.
	Claude = "claude"

	// OpenAI represents the OpenAI provider identifier.
	OpenAI = "openai"

	// OpenaiResponse represents the OpenAI response format identifier.
	OpenaiResponse = "openai-response"

	// Antigravity represents the Antigravity response format identifier.
	Antigravity = "antigravity"

	// Interactions represents the Google Interactions API format identifier.
	Interactions = "interactions"

	// ClaudeResponsesBridgeAlt identifies Claude /messages requests that must use
	// the Codex Responses API while preserving a Claude-compatible response.
	ClaudeResponsesBridgeAlt = "claude/responses"

	// ClaudeResponsesCompactBridgeAlt identifies Claude compaction requests that
	// must use the Codex /responses/compact endpoint.
	ClaudeResponsesCompactBridgeAlt = "claude/responses/compact"

	// ClaudeResponsesCompactionField carries validated compacted Responses items
	// from the Claude handler to the Codex executor. It is never sent upstream.
	ClaudeResponsesCompactionField = "cpa_responses_compaction"
)
