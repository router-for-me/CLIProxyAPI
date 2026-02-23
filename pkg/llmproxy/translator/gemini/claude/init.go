package claude

import (
<<<<<<< HEAD:pkg/llmproxy/translator/gemini/claude/init.go
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/translator"
=======
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
>>>>>>> archive/pr-234-head-20260223:internal/translator/gemini/claude/init.go
)

func init() {
	translator.Register(
		constant.Claude,
		constant.Gemini,
		ConvertClaudeRequestToGemini,
		interfaces.TranslateResponse{
			Stream:     ConvertGeminiResponseToClaude,
			NonStream:  ConvertGeminiResponseToClaudeNonStream,
			TokenCount: ClaudeTokenCount,
		},
	)
}
