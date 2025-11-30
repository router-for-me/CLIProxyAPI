// Package translator provides request and response translation functionality
// between different AI API formats. It acts as a wrapper around the SDK translator
// registry, providing convenient functions for translating requests and responses
// between OpenAI, Claude, Gemini, and other API formats.
//
// This package serves as a bridge between the old SDK translator and the new
// unified translator. When UseCanonicalTranslator flag is enabled in config,
// requests are routed through the new translator system via translator_wrapper functions.
package translator

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// registry holds the default translator registry instance.
var registry = sdktranslator.Default()

// Register registers a new translator for converting between two API formats.
//
// Parameters:
//   - from: The source API format identifier
//   - to: The target API format identifier
//   - request: The request translation function
//   - response: The response translation function
func Register(from, to string, request interfaces.TranslateRequestFunc, response interfaces.TranslateResponse) {
	registry.Register(sdktranslator.FromString(from), sdktranslator.FromString(to), request, response)
}

// Request translates a request from one API format to another.
// If UseCanonicalTranslator flag is enabled in config, routes through new translator system.
//
// Parameters:
//   - from: The source API format identifier
//   - to: The target API format identifier
//   - modelName: The model name for the request
//   - rawJSON: The raw JSON request data
//   - stream: Whether this is a streaming request
//
// Returns:
//   - []byte: The translated request JSON
func Request(from, to, modelName string, rawJSON []byte, stream bool) []byte {
	return RequestWithConfig(nil, from, to, modelName, rawJSON, stream, nil)
}

// RequestWithConfig translates a request with config and metadata support.
// If cfg.UseCanonicalTranslator is true, uses new translator for supported routes.
//
// Parameters:
//   - cfg: Configuration containing UseCanonicalTranslator flag
//   - from: The source API format identifier
//   - to: The target API format identifier
//   - modelName: The model name for the request
//   - rawJSON: The raw JSON request data
//   - stream: Whether this is a streaming request
//   - metadata: Additional context (e.g., thinking overrides)
//
// Returns:
//   - []byte: The translated request JSON
func RequestWithConfig(cfg *config.Config, from, to, modelName string, rawJSON []byte, stream bool, metadata map[string]any) []byte {
	// Check if new translator is enabled and route is supported
	if cfg != nil && cfg.UseCanonicalTranslator {
		fromFmt := sdktranslator.FromString(from)
		toStr := to
		
		// Route through new translator for supported translations
		switch toStr {
		case "gemini-cli", "antigravity":
			if result, err := executor.TranslateToGeminiCLI(cfg, fromFmt, modelName, rawJSON, stream, metadata); err == nil {
				return result
			}
		case "gemini":
			if result, err := executor.TranslateToGemini(cfg, fromFmt, modelName, rawJSON, stream, metadata); err == nil {
				return result
			}
		}
	}
	
	// Fallback to old translator
	return registry.TranslateRequest(sdktranslator.FromString(from), sdktranslator.FromString(to), modelName, rawJSON, stream)
}

// TranslateToGeminiCLI is a convenience wrapper for translateToGeminiCLI from executor package.
// Exported for backward compatibility with existing code.
func TranslateToGeminiCLI(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	return executor.TranslateToGeminiCLI(cfg, from, model, payload, streaming, metadata)
}

// TranslateToGemini is a convenience wrapper for translateToGemini from executor package.
// Exported for backward compatibility with existing code.
func TranslateToGemini(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	return executor.TranslateToGemini(cfg, from, model, payload, streaming, metadata)
}

// NeedConvert checks if a response translation is needed between two API formats.
//
// Parameters:
//   - from: The source API format identifier
//   - to: The target API format identifier
//
// Returns:
//   - bool: True if response translation is needed, false otherwise
func NeedConvert(from, to string) bool {
	return registry.HasResponseTransformer(sdktranslator.FromString(from), sdktranslator.FromString(to))
}

// Response translates a streaming response from one API format to another.
//
// Parameters:
//   - from: The source API format identifier
//   - to: The target API format identifier
//   - ctx: The context for the translation
//   - modelName: The model name for the response
//   - originalRequestRawJSON: The original request JSON
//   - requestRawJSON: The translated request JSON
//   - rawJSON: The raw response JSON
//   - param: Additional parameters for translation
//
// Returns:
//   - []string: The translated response lines
func Response(from, to string, ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	return registry.TranslateStream(ctx, sdktranslator.FromString(from), sdktranslator.FromString(to), modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// ResponseNonStream translates a non-streaming response from one API format to another.
//
// Parameters:
//   - from: The source API format identifier
//   - to: The target API format identifier
//   - ctx: The context for the translation
//   - modelName: The model name for the response
//   - originalRequestRawJSON: The original request JSON
//   - requestRawJSON: The translated request JSON
//   - rawJSON: The raw response JSON
//   - param: Additional parameters for translation
//
// Returns:
//   - string: The translated response JSON
func ResponseNonStream(from, to string, ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) string {
	return registry.TranslateNonStream(ctx, sdktranslator.FromString(from), sdktranslator.FromString(to), modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}
