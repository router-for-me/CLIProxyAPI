package helps

import (
	"bytes"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// TranslateRequestWithOriginal translates the request payload for upstream use and
// only translates the original payload when payload rules need the untranslated
// source shape for default-field detection.
func TranslateRequestWithOriginal(cfg *config.Config, from, to sdktranslator.Format, model string, payload, original []byte, stream bool) ([]byte, []byte) {
	translated := sdktranslator.TranslateRequest(from, to, model, payload, stream)
	if !payloadRulesConfigured(cfg) {
		return translated, nil
	}
	if len(original) == 0 || bytes.Equal(payload, original) {
		return translated, bytes.Clone(translated)
	}
	return translated, sdktranslator.TranslateRequest(from, to, model, original, stream)
}
