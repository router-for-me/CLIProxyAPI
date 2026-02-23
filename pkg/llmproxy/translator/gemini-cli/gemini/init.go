package gemini

import (
<<<<<<< HEAD:pkg/llmproxy/translator/gemini-cli/gemini/init.go
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/translator"
=======
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
>>>>>>> archive/pr-234-head-20260223:internal/translator/gemini-cli/gemini/init.go
)

func init() {
	translator.Register(
		constant.Gemini,
		constant.GeminiCLI,
		ConvertGeminiRequestToGeminiCLI,
		interfaces.TranslateResponse{
			Stream:     ConvertGeminiCliResponseToGemini,
			NonStream:  ConvertGeminiCliResponseToGeminiNonStream,
			TokenCount: GeminiTokenCount,
		},
	)
}
