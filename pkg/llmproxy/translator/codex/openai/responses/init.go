package responses

import (
	. "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/constant"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/interfaces"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Codex,
		ConvertOpenAIResponsesRequestToCodex,
		interfaces.TranslateResponse{
			Stream:    ConvertCodexResponseToOpenAIResponses,
			NonStream: ConvertCodexResponseToOpenAIResponsesNonStream,
		},
	)
}
