// Package openai provides translation between OpenAI Chat Completions and Kiro formats.
package openai

import (
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/translator"
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
