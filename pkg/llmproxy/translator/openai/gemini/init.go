package gemini

import (
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/translator/translator"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/constant"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/interfaces"
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
