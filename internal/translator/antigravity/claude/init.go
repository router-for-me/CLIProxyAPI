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
		Antigravity,
		nil,
		interfaces.TranslateResponse{
			Stream:     ConvertAntigravityResponseToClaude,
			NonStream:  ConvertAntigravityResponseToClaudeNonStream,
			TokenCount: ClaudeTokenCount,
		},
	)
	sdktranslator.RegisterRequestEnvelope(sdktranslator.FormatClaude, sdktranslator.FormatAntigravity, convertClaudeRequestEnvelopeToAntigravity)
}

func convertClaudeRequestEnvelopeToAntigravity(_ context.Context, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
	var webSearch *bool
	if req.ModelInfo != nil && req.ModelInfo.NativeCapabilities != nil {
		webSearch = req.ModelInfo.NativeCapabilities.WebSearch
	}
	req.Body, req.Err = convertClaudeRequestToAntigravityWithWebSearch(req.Model, req.Body, req.Stream, webSearch)
	return req
}
