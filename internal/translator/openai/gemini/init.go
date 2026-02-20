package gemini

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		constant.Gemini,
		constant.OpenAI,
		ConvertGeminiRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:     ConvertOpenAIResponseToGemini,
			NonStream:  ConvertOpenAIResponseToGeminiNonStream,
			TokenCount: GeminiTokenCount,
		},
	)
}
