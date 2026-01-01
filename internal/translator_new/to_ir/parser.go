// Package to_ir converts provider-specific API formats into unified format.
package to_ir

import (
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
)

// Format represents the source API format
type Format string

const (
	FormatOpenAI    Format = "openai"
	FormatClaude    Format = "claude"
	FormatGemini    Format = "gemini"
	FormatGeminiCLI Format = "gemini-cli"
	FormatOllama    Format = "ollama"
	FormatKiro      Format = "kiro"
)

// ParserOptions configuration for parsing
type ParserOptions struct {
	Format Format // Source format
	Model  string // Model name (optional context)
}

// ParseRequest parses a request from any supported format into UnifiedChatRequest
func ParseRequest(payload []byte, opts ParserOptions) (*ir.UnifiedChatRequest, error) {
	switch opts.Format {
	case FormatOpenAI:
		return ParseOpenAIRequest(payload)
	case FormatClaude:
		return ParseClaudeRequest(payload)
	case FormatGemini, FormatGeminiCLI:
		// Gemini formats are similar enough at the input parsing level, 
		// but ParseGeminiRequest should handle both or we check specificity
		return ParseGeminiRequest(payload)
	case FormatOllama:
		return ParseOllamaRequest(payload)
	case FormatKiro:
		return ParseKiroRequest(payload)
	default:
		// Default to OpenAI as it's the most common protocol
		return ParseOpenAIRequest(payload)
	}
}

// ParseResponse parses a response from an upstream provider into UnifiedChatResponse IR.
// Currently primarily used for parsing upstream responses that need to be converted to
// a different downstream format (e.g. Qwen/iFlow -> OpenAI -> Client).
// For now, this is minimal and delegates to specific parsers.
func ParseResponse(payload []byte, opts ParserOptions) (*ir.UnifiedChatResponse, error) {
	switch opts.Format {
	case FormatOpenAI:
		msgs, usage, err := ParseOpenAIResponse(payload)
		if err != nil {
			return nil, err
		}
		// Extract ID from payload if possible (OpenAI responses have 'id')
		var id string
		var raw map[string]interface{}
		if json.Unmarshal(payload, &raw) == nil {
			if v, ok := raw["id"].(string); ok {
				id = v
			}
		}
		return &ir.UnifiedChatResponse{
			ID:       id,
			Messages: msgs,
			Usage:    usage,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported response parsing format: %s", opts.Format)
	}
}
