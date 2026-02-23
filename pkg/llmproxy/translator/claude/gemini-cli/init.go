package geminiCLI

import (
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
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
