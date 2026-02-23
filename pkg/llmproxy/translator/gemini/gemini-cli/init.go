package geminiCLI

import (
<<<<<<< HEAD:pkg/llmproxy/translator/gemini/gemini-cli/init.go
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/translator"
=======
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
>>>>>>> archive/pr-234-head-20260223:internal/translator/gemini/gemini-cli/init.go
)

func init() {
	translator.Register(
		constant.GeminiCLI,
		constant.Gemini,
		ConvertGeminiCLIRequestToGemini,
		interfaces.TranslateResponse{
			Stream:     ConvertGeminiResponseToGeminiCLI,
			NonStream:  ConvertGeminiResponseToGeminiCLINonStream,
			TokenCount: GeminiCLITokenCount,
		},
	)
}
