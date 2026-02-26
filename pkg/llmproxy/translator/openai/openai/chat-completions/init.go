package chat_completions

import (
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/constant"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/interfaces"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/translator/translator"
)

func init() {
	translator.Register(
		constant.OpenAI,
		constant.OpenAI,
		ConvertOpenAIRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:    ConvertOpenAIResponseToOpenAI,
			NonStream: ConvertOpenAIResponseToOpenAINonStream,
		},
	)
}
