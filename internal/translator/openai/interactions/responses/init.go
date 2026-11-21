package responses

import (
	"context"

	"github.com/MoeForge/moego/ojson"
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type geminiInteractionsTargetKey struct{}

type geminiInteractionsTarget struct {
	convertToolResultPart func(ojson.Result) *ojson.Doc
}

// WithGeminiInteractionsTarget enables native Gemini thought replay and
// provider-specific tool result conversion.
func WithGeminiInteractionsTarget(ctx context.Context, convertToolResultPart func(ojson.Result) *ojson.Doc) context.Context {
	return context.WithValue(ctx, geminiInteractionsTargetKey{}, &geminiInteractionsTarget{convertToolResultPart: convertToolResultPart})
}

func init() {
	translator.Register(
		OpenaiResponse,
		Interactions,
		nil,
		interfaces.TranslateResponse{
			Stream:    ConvertInteractionsResponseToOpenAIResponses,
			NonStream: ConvertInteractionsResponseToOpenAIResponsesNonStream,
		},
	)
	sdktranslator.RegisterRequestEnvelope(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatInteractions,
		func(ctx context.Context, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
			target, _ := ctx.Value(geminiInteractionsTargetKey{}).(*geminiInteractionsTarget)
			req.Body, req.Err = convertOpenAIResponsesRequestToInteractions(req.Model, req.Body, req.Stream, target)
			return req
		},
	)
	translator.Register(
		Interactions,
		OpenaiResponse,
		ConvertInteractionsRequestToOpenAIResponses,
		interfaces.TranslateResponse{
			Stream:    ConvertOpenAIResponsesResponseToInteractions,
			NonStream: ConvertOpenAIResponsesResponseToInteractionsNonStream,
		},
	)
}
