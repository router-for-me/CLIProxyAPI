package helps

import (
	"fmt"
	"net/http"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// ValidateNativeResponsesFormats prevents an unregistered translation from
// silently sending or returning a payload in the wrong protocol.
func ValidateNativeResponsesFormats(from, responseFormat sdktranslator.Format, stream bool) error {
	to := sdktranslator.FormatOpenAIResponse
	if from != to && !sdktranslator.HasRequestTransformer(from, to) {
		return cliproxyauth.NewRequestScopedError(fmt.Sprintf("native Responses provider has no request translator from %s to %s", from, to), http.StatusBadRequest)
	}
	hasResponse := sdktranslator.HasNonStreamResponseTransformer(responseFormat, to)
	if stream {
		hasResponse = sdktranslator.HasStreamResponseTransformer(responseFormat, to)
	}
	if responseFormat != to && !hasResponse {
		return cliproxyauth.NewRequestScopedError(fmt.Sprintf("native Responses provider has no response translator from %s to %s", to, responseFormat), http.StatusBadRequest)
	}
	return nil
}
