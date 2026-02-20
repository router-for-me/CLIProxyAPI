package geminiCLI

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		constant.GeminiCLI,
		constant.Claude,
		ConvertGeminiCLIRequestToClaude,
		interfaces.TranslateResponse{
			Stream:     ConvertClaudeResponseToGeminiCLI,
			NonStream:  ConvertClaudeResponseToGeminiCLINonStream,
			TokenCount: GeminiCLITokenCount,
		},
	)
}
