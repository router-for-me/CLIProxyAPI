// Package translator provides types and functions for converting chat requests and responses between different schemas.
package translator

import "context"

// RequestTransform is a function type that converts a request payload from a source schema to a target schema.
// It takes the model name, the raw JSON payload of the request, and a boolean indicating if the request is for a streaming response.
// It returns the converted request payload as a byte slice.
type RequestTransform func(model string, rawJSON []byte, stream bool) []byte

// ResponseStreamTransform is a function type that converts a streaming response from a source schema to a target schema.
// It takes a context, the model name, the raw JSON of the original and converted requests, the raw JSON of the current response chunk, and an optional parameter.
// It returns a slice of strings, where each string is a chunk of the converted streaming response.
type ResponseStreamTransform func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string

// ResponseNonStreamTransform is a function type that converts a non-streaming response from a source schema to a target schema.
// It takes a context, the model name, the raw JSON of the original and converted requests, the raw JSON of the response, and an optional parameter.
// It returns the converted response as a single string.
type ResponseNonStreamTransform func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) string

// ResponseTokenCountTransform is a function type that transforms a token count from a source format to a target format.
// It takes a context and the token count as an int64, and returns the transformed token count as a string.
type ResponseTokenCountTransform func(ctx context.Context, count int64) string

// ResponseTransform is a struct that groups together the functions for transforming streaming and non-streaming responses,
// as well as token counts.
type ResponseTransform struct {
	// Stream is the function for transforming streaming responses.
	Stream ResponseStreamTransform
	// NonStream is the function for transforming non-streaming responses.
	NonStream ResponseNonStreamTransform
	// TokenCount is the function for transforming token counts.
	TokenCount ResponseTokenCountTransform
}
