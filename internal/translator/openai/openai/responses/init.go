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
	sdktranslator.RegisterRequestEnvelope(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, func(ctx context.Context, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
		inline, _ := ctx.Value(common.InlineChatToolMediaKey{}).(bool)
		req.Body, req.Err = convertOpenAIResponsesRequestToOpenAIChatCompletions(req.Model, req.Body, req.Stream, inline)
		return req
	})
	translator.Register(
		OpenaiResponse,
		OpenAI,
		nil,
		interfaces.TranslateResponse{
			Stream:    ConvertOpenAIChatCompletionsResponseToOpenAIResponses,
			NonStream: ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream,
		},
	)
}
