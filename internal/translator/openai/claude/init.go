package claude

import (
	"context"

	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func init() {
	sdktranslator.RegisterRequestEnvelope(sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, func(ctx context.Context, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
		inline, _ := ctx.Value(common.InlineChatToolMediaKey{}).(bool)
		req.Body, req.Err = convertClaudeRequestToOpenAI(req.Model, req.Body, req.Stream, false, inline)
		return req
	})
	translator.Register(
		Claude,
		OpenAI,
		nil,
		interfaces.TranslateResponse{
			Stream:     ConvertOpenAIResponseToClaude,
			NonStream:  ConvertOpenAIResponseToClaudeNonStream,
			TokenCount: ClaudeTokenCount,
		},
	)
}
