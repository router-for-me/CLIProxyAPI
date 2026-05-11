// Package claude provides translation between Kiro and Claude formats.
package claude

import (
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		Claude,
		Kiro,
		ConvertClaudeRequestToKiro,
		interfaces.TranslateResponse{
			Stream:    ConvertKiroStreamToClaude,
			NonStream: ConvertKiroNonStreamToClaude,
		},
	)
}
