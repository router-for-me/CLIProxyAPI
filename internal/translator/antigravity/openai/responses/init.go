package responses

import (
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Antigravity,
		ConvertOpenAIResponsesRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:    ConvertAntigravityResponseToOpenAIResponses,
			NonStream: ConvertAntigravityResponseToOpenAIResponsesNonStream,
		},
	)
	sdktranslator.RegisterRequestEnvelope(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatAntigravity,
		ConvertOpenAIResponsesRequestEnvelopeToAntigravity,
	)
}
