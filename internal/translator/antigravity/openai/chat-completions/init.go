package chat_completions

import (
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Antigravity,
		nil,
		interfaces.TranslateResponse{
			Stream:    ConvertAntigravityResponseToOpenAI,
			NonStream: ConvertAntigravityResponseToOpenAINonStream,
		},
	)
	sdktranslator.RegisterRequestEnvelope(
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatAntigravity,
		ConvertOpenAIRequestEnvelopeToAntigravity,
	)
}
