package responses

import (
	"context"

	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func init() {
	sdktranslator.RegisterRequestEnvelope(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatInteractions, func(ctx context.Context, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
		replay, _ := ctx.Value(common.DevinReasoningReplayKey{}).(bool)
		req.Body, req.Err = convertOpenAIResponsesRequestToInteractions(req.Model, req.Body, req.Stream, replay)
		return req
	})
	translator.Register(
		OpenaiResponse,
		Interactions,
		nil,
		interfaces.TranslateResponse{
			Stream:    ConvertInteractionsResponseToOpenAIResponses,
			NonStream: ConvertInteractionsResponseToOpenAIResponsesNonStream,
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
