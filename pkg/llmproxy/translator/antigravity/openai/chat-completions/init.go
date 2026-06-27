package chat_completions

import (
	. "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/constant"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/interfaces"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Antigravity,
		ConvertOpenAIRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:    ConvertAntigravityResponseToOpenAI,
			NonStream: ConvertAntigravityResponseToOpenAINonStream,
		},
	)
}
