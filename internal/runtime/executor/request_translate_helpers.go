package executor

import (
	"bytes"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// translateRequestPair translates both runtime and original payloads while avoiding
// duplicate work when they are identical.
func translateRequestPair(
	from sdktranslator.Format,
	to sdktranslator.Format,
	baseModel string,
	payload []byte,
	original []byte,
	stream bool,
) (originalPayload []byte, originalTranslated []byte, translated []byte) {
	originalPayload = payload
	if len(original) > 0 {
		originalPayload = original
	}

	translated = sdktranslator.TranslateRequest(from, to, baseModel, payload, stream)
	if bytes.Equal(originalPayload, payload) {
		return originalPayload, bytes.Clone(translated), translated
	}

	originalTranslated = sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, stream)
	return originalPayload, originalTranslated, translated
}
