package interactions

import (
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		Interactions,
		Claude,
		ConvertInteractionsRequestToClaude,
		interfaces.TranslateResponse{
			Stream:    ConvertClaudeResponseToInteractions,
			NonStream: ConvertClaudeResponseToInteractionsNonStream,
		},
	)
}
