package chat_completions

import (
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Interactions,
		ConvertOpenAIRequestToInteractions,
		interfaces.TranslateResponse{
			Stream:    ConvertInteractionsResponseToOpenAI,
			NonStream: ConvertInteractionsResponseToOpenAINonStream,
		},
	)
	translator.Register(
		Interactions,
		OpenAI,
		ConvertInteractionsRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:    ConvertOpenAIResponseToInteractions,
			NonStream: ConvertOpenAIResponseToInteractionsNonStream,
		},
	)
}
