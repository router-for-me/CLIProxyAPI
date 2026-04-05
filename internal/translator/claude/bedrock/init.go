package bedrock

import (
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		Claude,
		BedrockConverse,
		ConvertClaudeRequestToBedrock,
		interfaces.TranslateResponse{
			Stream:     ConvertBedrockStreamResponseToClaude,
			NonStream:  ConvertBedrockResponseToClaude,
			TokenCount: ClaudeTokenCount,
		},
	)
}
