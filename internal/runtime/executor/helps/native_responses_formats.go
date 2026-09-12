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
	// Plugins decide whether to translate using the actual payload. Checked
	// translation below rejects their declined routes without protocol passthrough.
	if sdktranslator.HasPluginHooks() {
		return nil
	}
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

// TranslateNativeResponsesRequest requires an actual request translation or identity route.
func TranslateNativeResponsesRequest(from sdktranslator.Format, model string, payload []byte, stream bool) ([]byte, error) {
	to := sdktranslator.FormatOpenAIResponse
	out, handled := sdktranslator.TranslateRequestChecked(from, to, model, payload, stream)
	if !handled {
		return nil, cliproxyauth.NewRequestScopedError(fmt.Sprintf("native Responses provider has no request translator from %s to %s", from, to), http.StatusBadRequest)
	}
	// An identity route does not otherwise apply the executor's stream mode.
	return SetBoolIfDifferent(out, "stream", stream), nil
}

// NativeResponsesTranslationError identifies a declined response translation route.
func NativeResponsesTranslationError(responseFormat sdktranslator.Format) error {
	return cliproxyauth.NewRequestScopedError(fmt.Sprintf("native Responses provider has no response translator from %s to %s", sdktranslator.FormatOpenAIResponse, responseFormat), http.StatusBadRequest)
}

// NativeResponsesIncompleteTranslationError rejects a translation that would
// silently report a truncated response as completed or omit its terminal event.
func NativeResponsesIncompleteTranslationError(responseFormat sdktranslator.Format) error {
	return cliproxyauth.NewRequestScopedError(fmt.Sprintf("native Responses translator did not preserve the incomplete outcome for %s", responseFormat), http.StatusBadGateway)
}
