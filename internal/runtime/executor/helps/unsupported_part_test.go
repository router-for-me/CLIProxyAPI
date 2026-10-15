package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// A turn that only holds a file the target cannot receive is refused with a named
// 400, through both the single and the pair translation, with and without compat.
func TestClaudeContainerUploadReturns400(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file-example"}]}]}`)
	for _, to := range []sdktranslator.Format{sdktranslator.FormatGemini, sdktranslator.FormatOpenAI, sdktranslator.FormatCodex, sdktranslator.FormatInteractions} {
		for _, compat := range []bool{false, true} {
			_, single := TranslateRequestReturningError(context.Background(), nil, nil, sdktranslator.FormatClaude, to, "m", payload, false, compat)
			_, _, pair := TranslateRequestPairReturningError(context.Background(), nil, nil, sdktranslator.FormatClaude, to, "m", payload, payload, false, compat)
			for name, err := range map[string]error{"single": single, "pair": pair} {
				if err == nil || err.Error() != "unsupported content part: container_upload" || clienterror.HTTPStatusFromError(err) != 400 {
					t.Errorf("to=%s compat=%v %s: err = %v, want a 400 naming container_upload", to, compat, name, err)
				}
			}
		}
	}
}

// TestByteWrappersKeepAJSONBodyForAnUnsendableFile guards the executors that still
// ignore the error: they must get a request body, never an empty one.
func TestByteWrappersKeepAJSONBodyForAnUnsendableFile(t *testing.T) {
	payload := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file-absent"}]}]}`)
	for _, to := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatCodex, sdktranslator.FormatInteractions, sdktranslator.FormatGemini} {
		for _, compat := range []bool{false, true} {
			original, working := TranslateRequestPairWithAPIKeyModelCompatibility(context.Background(), nil, nil, sdktranslator.FormatClaude, to, "m", payload, payload, false, compat)
			for _, body := range [][]byte{original, working} {
				if !gjson.ValidBytes(body) || len(body) < 2 {
					t.Fatalf("to=%s compat=%v: body = %q", to, compat, body)
				}
			}
			if _, _, err := TranslateRequestPairReturningError(context.Background(), nil, nil, sdktranslator.FormatClaude, to, "m", payload, payload, false, compat); err == nil {
				t.Fatalf("to=%s compat=%v: the checked variant lost the error", to, compat)
			}
		}
	}
}
