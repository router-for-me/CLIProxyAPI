package geminiCLI

import (
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
)

func init() {
	translator.Register(
		constant.GeminiCLI,
		constant.OpenAI,
		ConvertGeminiCLIRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:     ConvertOpenAIResponseToGeminiCLI,
			NonStream:  ConvertOpenAIResponseToGeminiCLINonStream,
			TokenCount: GeminiCLITokenCount,
		},
	)
}
