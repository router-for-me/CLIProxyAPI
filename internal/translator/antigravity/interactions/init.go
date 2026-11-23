package interactions

import (
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
)

func init() {
	translator.Register(
		Interactions,
		Antigravity,
		ConvertInteractionsRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:    ConvertAntigravityResponseToInteractions,
			NonStream: ConvertAntigravityResponseToInteractionsNonStream,
		},
	)
}
