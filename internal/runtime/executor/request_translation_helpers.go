package executor

import (
	"bytes"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// translateRequestPair avoids re-running the same expensive request translation
// when req.Payload and opts.OriginalRequest carry identical JSON.
func translateRequestPair(from, to sdktranslator.Format, model string, payload, original []byte, stream bool) ([]byte, []byte) {
	return translateRequestPairSelective(from, to, model, payload, original, stream, true)
}

func translateRequestPairSelective(from, to sdktranslator.Format, model string, payload, original []byte, stream bool, needOriginal bool) ([]byte, []byte) {
	translated := sdktranslator.TranslateRequest(from, to, model, payload, stream)
	if !needOriginal {
		return translated, nil
	}
	if len(original) == 0 || bytes.Equal(payload, original) {
		return translated, duplicateBytes(translated)
	}

	originalTranslated := sdktranslator.TranslateRequest(from, to, model, original, stream)
	return translated, originalTranslated
}

func duplicateBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}
