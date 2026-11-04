package claude

import (
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
)

func init() {
	translator.Register(
		Claude,
		OpenAI,
		ConvertClaudeRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:     ConvertOpenAIResponseToClaude,
			NonStream:  ConvertOpenAIResponseToClaudeNonStream,
			TokenCount: ClaudeTokenCount,
		},
	)
}
