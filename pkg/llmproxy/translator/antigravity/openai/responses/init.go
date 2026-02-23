package responses

import (
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
)

func init() {
	translator.Register(
		constant.OpenaiResponse,
		constant.Antigravity,
		ConvertOpenAIResponsesRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:    ConvertAntigravityResponseToOpenAIResponses,
			NonStream: ConvertAntigravityResponseToOpenAIResponsesNonStream,
		},
	)
}
