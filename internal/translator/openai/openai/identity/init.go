// Package identity registers a no-op translator for the openai-response → openai-response
// format pair. This is required by the native Responses path in OpenAICompatExecutor:
// when the upstream supports /responses natively, both from and to formats are
// "openai-response" and the translator pipeline must return the payload unchanged.
package identity

import (
	"context"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		OpenaiResponse,
		identityRequestTransform,
		interfaces.TranslateResponse{
			Stream:     identityStreamResponseTransform,
			NonStream:  identityNonStreamResponseTransform,
		},
	)
}

// identityRequestTransform returns the request payload unchanged.
func identityRequestTransform(model string, raw []byte, stream bool) []byte {
	return raw
}

// identityNonStreamResponseTransform returns the response body unchanged.
func identityNonStreamResponseTransform(_ context.Context, _ string, _, _, raw []byte, _ *any) []byte {
	return raw
}

// identityStreamResponseTransform wraps a single SSE chunk as-is.
func identityStreamResponseTransform(_ context.Context, _ string, _, _, raw []byte, _ *any) [][]byte {
	if len(raw) == 0 {
		return nil
	}
	return [][]byte{raw}
}
