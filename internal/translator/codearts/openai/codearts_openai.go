package openai

import (
	"context"
)

// ConvertCodeArtsStreamToOpenAI passes through SSE chunks.
// The executor already converts CodeArts SSE to OpenAI SSE format.
func ConvertCodeArtsStreamToOpenAI(ctx context.Context, model string, originalRequest, translatedRequest, chunk []byte, state *any) [][]byte {
	if len(chunk) == 0 {
		return nil
	}
	return [][]byte{chunk}
}

// ConvertCodeArtsNonStreamToOpenAI passes through non-stream responses.
// The executor already builds OpenAI-format responses.
func ConvertCodeArtsNonStreamToOpenAI(ctx context.Context, model string, originalRequest, translatedRequest, response []byte, param *any) []byte {
	if len(response) == 0 {
		return nil
	}
	return response
}
