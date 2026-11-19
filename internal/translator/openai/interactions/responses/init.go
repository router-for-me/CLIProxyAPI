package responses

import (
	"context"

	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type geminiThoughtReplayKey struct{}

// WithGeminiThoughtReplay allows Responses reasoning replay on a native Gemini
// Interactions request. Other providers use the same translation format.
func WithGeminiThoughtReplay(ctx context.Context) context.Context {
	return context.WithValue(ctx, geminiThoughtReplayKey{}, true)
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
			replayGeminiThought, _ := ctx.Value(geminiThoughtReplayKey{}).(bool)
			req.Body, req.Err = convertOpenAIResponsesRequestToInteractions(req.Model, req.Body, req.Stream, replayGeminiThought)
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
