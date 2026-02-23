package geminiCLI

import (
<<<<<<< HEAD:pkg/llmproxy/translator/openai/gemini-cli/init.go
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/translator"
=======
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
>>>>>>> archive/pr-234-head-20260223:internal/translator/openai/gemini-cli/init.go
)

func init() {
	translator.Register(
		constant.GeminiCLI,
		constant.OpenAI,
		ConvertGeminiCLIRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:     ConvertOpenAIResponseToGeminiCLI,
			NonStream:  ConvertOpenAIResponseToGeminiCLINonStream,
			TokenCount: GeminiCLITokenCount,
		},
	)
}
