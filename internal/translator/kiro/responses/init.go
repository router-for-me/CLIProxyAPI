// Package responses provides translation between OpenAI Responses and Kiro formats.
package responses

import (
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Kiro,
		ConvertOpenAIResponsesRequestToKiro,
		interfaces.TranslateResponse{
			Stream:    ConvertKiroStreamToOpenAIResponses,
			NonStream: ConvertKiroNonStreamToOpenAIResponses,
		},
	)
}
