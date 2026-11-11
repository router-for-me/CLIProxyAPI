package claude

import (
	"context"

	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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
	sdktranslator.RegisterRequestEnvelope(sdktranslator.FormatClaude, sdktranslator.FormatCodex,
		func(ctx context.Context, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
			req.Body, req.Err = ConvertClaudeRequestToCodexContext(ctx, req.Model, req.Body, req.Stream)
			return req
		})
}
