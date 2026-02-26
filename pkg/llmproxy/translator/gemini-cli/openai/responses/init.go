package responses

import (
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/translator/translator"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/constant"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/interfaces"
)

func init() {
	translator.Register(
		constant.OpenaiResponse,
		constant.GeminiCLI,
		ConvertOpenAIResponsesRequestToGeminiCLI,
		interfaces.TranslateResponse{
			Stream:    ConvertGeminiCLIResponseToOpenAIResponses,
			NonStream: ConvertGeminiCLIResponseToOpenAIResponsesNonStream,
		},
	)
}
