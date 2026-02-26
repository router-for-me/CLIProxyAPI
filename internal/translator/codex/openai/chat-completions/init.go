package chat_completions

import (
	. "github.com/kooshapari/cliproxyapi-plusplus/v6/internal/constant"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/internal/interfaces"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Codex,
		ConvertOpenAIRequestToCodex,
		interfaces.TranslateResponse{
			Stream:    ConvertCodexResponseToOpenAI,
			NonStream: ConvertCodexResponseToOpenAINonStream,
		},
	)
}
