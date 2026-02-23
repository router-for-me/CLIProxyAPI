package responses

import (
<<<<<<< HEAD:pkg/llmproxy/translator/claude/openai/responses/init.go
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/translator"
=======
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
>>>>>>> archive/pr-234-head-20260223:internal/translator/claude/openai/responses/init.go
)

func init() {
	translator.Register(
		constant.OpenaiResponse,
		constant.Claude,
		ConvertOpenAIResponsesRequestToClaude,
		interfaces.TranslateResponse{
			Stream:    ConvertClaudeResponseToOpenAIResponses,
			NonStream: ConvertClaudeResponseToOpenAIResponsesNonStream,
		},
	)
}
