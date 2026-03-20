package executor

import (
	"bytes"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// translateRequestPair avoids re-running the same expensive request translation
// when req.Payload and opts.OriginalRequest carry identical JSON.
func translateRequestPair(from, to sdktranslator.Format, model string, payload, original []byte, stream bool) ([]byte, []byte) {
	if len(original) == 0 || bytes.Equal(payload, original) {
		translated := sdktranslator.TranslateRequest(from, to, model, payload, stream)
		return translated, duplicateBytes(translated)
	}

	originalTranslated := sdktranslator.TranslateRequest(from, to, model, original, stream)
	body := sdktranslator.TranslateRequest(from, to, model, payload, stream)
	return body, originalTranslated
}

func duplicateBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}
