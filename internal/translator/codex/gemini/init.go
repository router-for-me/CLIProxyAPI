package gemini

import (
	. "github.com/kooshapari/cliproxyapi-plusplus/v6/internal/constant"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/internal/interfaces"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		Gemini,
		Codex,
		ConvertGeminiRequestToCodex,
		interfaces.TranslateResponse{
			Stream:     ConvertCodexResponseToGemini,
			NonStream:  ConvertCodexResponseToGeminiNonStream,
			TokenCount: GeminiTokenCount,
		},
	)
}
