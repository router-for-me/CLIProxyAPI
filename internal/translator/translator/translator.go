// Package translator provides request and response translation functionality
// between different AI API formats. It acts as a wrapper around the SDK translator
// registry, providing convenient functions for translating requests and responses
// between OpenAI, Claude, Gemini, and other API formats.
package translator

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
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
	return registry.TranslateRequest(sdktranslator.FromString(from), sdktranslator.FromString(to), modelName, rawJSON, stream)
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
