// Package openai provides translation between constant.OpenAI Chat Completions and constant.Kiro formats.
package openai

import (
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/translator/translator"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/constant"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/interfaces"
)

func init() {
	translator.Register(
		constant.OpenAI, // source format
		constant.Kiro,   // target format
		ConvertOpenAIRequestToKiro,
		interfaces.TranslateResponse{
			Stream:    ConvertKiroStreamToOpenAI,
			NonStream: ConvertKiroNonStreamToOpenAI,
		},
	)
}
