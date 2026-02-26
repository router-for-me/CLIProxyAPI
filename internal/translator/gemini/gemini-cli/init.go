package geminiCLI

import (
	. "github.com/kooshapari/cliproxyapi-plusplus/v6/internal/constant"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/internal/interfaces"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		GeminiCLI,
		Gemini,
		ConvertGeminiCLIRequestToGemini,
		interfaces.TranslateResponse{
			Stream:     ConvertGeminiResponseToGeminiCLI,
			NonStream:  ConvertGeminiResponseToGeminiCLINonStream,
			TokenCount: GeminiCLITokenCount,
		},
	)
}
