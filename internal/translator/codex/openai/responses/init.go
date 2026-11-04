package responses

import (
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
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
