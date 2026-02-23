// Package openai provides translation between constant.OpenAI Chat Completions and constant.Kiro formats.
package openai

import (
<<<<<<< HEAD:pkg/llmproxy/translator/kiro/openai/init.go
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/translator"
=======
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
>>>>>>> archive/pr-234-head-20260223:internal/translator/kiro/openai/init.go
)

func init() {
	translator.Register(
		constant.OpenAI, // source format
		constant.Kiro,   // target format
		ConvertOpenAIRequestToKiro,
		interfaces.TranslateResponse{
			Stream:    ConvertKiroStreamToOpenAI,
			NonStream: ConvertKiroNonStreamToOpenAI,
		},
	)
}
