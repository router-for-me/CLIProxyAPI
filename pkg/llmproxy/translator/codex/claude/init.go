package claude

import (
	. "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/constant"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/interfaces"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/translator"
)

func init() {
	translator.Register(
		Claude,
		Codex,
		ConvertClaudeRequestToCodex,
		interfaces.TranslateResponse{
			Stream:     ConvertCodexResponseToClaude,
			NonStream:  ConvertCodexResponseToClaudeNonStream,
			TokenCount: ClaudeTokenCount,
		},
	)
}
