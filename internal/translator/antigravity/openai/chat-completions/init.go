package chat_completions

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		constant.OpenAI,
		constant.Antigravity,
		ConvertOpenAIRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:    ConvertAntigravityResponseToOpenAI,
			NonStream: ConvertAntigravityResponseToOpenAINonStream,
		},
	)
}
