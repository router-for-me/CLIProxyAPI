// Package claude provides translation between Kiro and Claude formats.
package claude

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		constant.Claude,
		constant.Kiro,
		ConvertClaudeRequestToKiro,
		interfaces.TranslateResponse{
			Stream:    ConvertKiroStreamToClaude,
			NonStream: ConvertKiroNonStreamToClaude,
		},
	)
}
