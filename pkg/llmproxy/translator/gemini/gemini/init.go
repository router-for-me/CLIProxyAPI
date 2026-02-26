package gemini

import (
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/translator/translator"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/constant"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/interfaces"
)

// Register a no-op response translator and a request normalizer for constant.Gemini→constant.Gemini.
// The request converter ensures missing or invalid roles are normalized to valid values.
func init() {
	translator.Register(
		constant.Gemini,
		constant.Gemini,
		ConvertGeminiRequestToGemini,
		interfaces.TranslateResponse{
			Stream:     PassthroughGeminiResponseStream,
			NonStream:  PassthroughGeminiResponseNonStream,
			TokenCount: GeminiTokenCount,
		},
	)
}
