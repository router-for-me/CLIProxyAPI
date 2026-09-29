package relayconvert

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/internal/convdiag"
	sharedclaude "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/claude"
	sharedgemini "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/gemini"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
)

func ClaudeMessagesRequestToOpenAIChat(claudeRequest dto.ClaudeRequest, info convmeta.Meta) (*dto.GeneralOpenAIRequest, error) {
	return convertCompatRequest[dto.GeneralOpenAIRequest](context.Background(), info, types.RelayFormatOpenAI, &claudeRequest)
}

func OpenAIChatRequestToClaudeMessages(c context.Context, info convmeta.Meta, textRequest dto.GeneralOpenAIRequest) (*dto.ClaudeRequest, error) {
	return convertCompatRequest[dto.ClaudeRequest](c, info, types.RelayFormatClaude, &textRequest)
}

func GeminiGenerateContentRequestToOpenAIChat(geminiRequest *dto.GeminiChatRequest, info convmeta.Meta) (*dto.GeneralOpenAIRequest, error) {
	return convertCompatRequest[dto.GeneralOpenAIRequest](context.Background(), info, types.RelayFormatOpenAI, geminiRequest)
}

func OpenAIChatRequestToGeminiGenerateContent(c context.Context, textRequest dto.GeneralOpenAIRequest, info convmeta.Meta) (*dto.GeminiChatRequest, error) {
	return convertCompatRequest[dto.GeminiChatRequest](c, info, types.RelayFormatGemini, &textRequest)
}

func ApplyGeminiThinkingConfigChecked(geminiRequest *dto.GeminiChatRequest, info convmeta.Meta, oaiRequest ...dto.GeneralOpenAIRequest) error {
	ctx, collector := convdiag.WithCollector(context.Background())
	err := reasoning.AsClientError(sharedgemini.ApplyThinkingConfig(ctx, geminiRequest, info, oaiRequest...))
	if recorder, ok := info.(interface {
		RecordConversionDiagnostics(context.Context, []types.ConversionDiagnostic)
	}); ok {
		diagnostics := collector.Diagnostics()
		for i := range diagnostics {
			if diagnostics[i].To == "" {
				diagnostics[i].To = types.RelayFormatGemini
			}
		}
		recorder.RecordConversionDiagnostics(ctx, diagnostics)
	}
	return err
}

func ApplyClaudeThinkingModel(claudeRequest *dto.ClaudeRequest, info convmeta.Meta) error {
	ctx, collector := convdiag.WithCollector(context.Background())
	err := reasoning.AsClientError(sharedclaude.ApplyReasoning(ctx, claudeRequest, info, reasoning.Intent{}, false))
	if recorder, ok := info.(interface {
		RecordConversionDiagnostics(context.Context, []types.ConversionDiagnostic)
	}); ok {
		recorder.RecordConversionDiagnostics(ctx, collector.Diagnostics())
	}
	return err
}

func ChatCompletionsRequestToResponsesRequest(req *dto.GeneralOpenAIRequest) (*dto.OpenAIResponsesRequest, error) {
	return convertCompatRequest[dto.OpenAIResponsesRequest](context.Background(), nil, types.RelayFormatOpenAIResponses, req)
}

func ResponsesRequestToChatCompletionsRequest(req *dto.OpenAIResponsesRequest) (*dto.GeneralOpenAIRequest, error) {
	return convertCompatRequest[dto.GeneralOpenAIRequest](context.Background(), nil, types.RelayFormatOpenAI, req)
}

func OpenAIResponsesRequestToClaudeMessages(c context.Context, info convmeta.Meta, req *dto.OpenAIResponsesRequest) (*dto.ClaudeRequest, error) {
	return convertCompatRequest[dto.ClaudeRequest](c, info, types.RelayFormatClaude, req)
}

func OpenAIResponsesRequestToGeminiChat(c context.Context, req *dto.OpenAIResponsesRequest, info convmeta.Meta) (*dto.GeminiChatRequest, error) {
	return convertCompatRequest[dto.GeminiChatRequest](c, info, types.RelayFormatGemini, req)
}

func convertCompatRequest[T any](c context.Context, info convmeta.Meta, target types.RelayFormat, request any) (*T, error) {
	result, err := ConvertRequest(c, info, target, request)
	if err != nil {
		return nil, err
	}
	converted, ok := result.Value.(*T)
	if !ok {
		return nil, fmt.Errorf("expected %s request, got %T", target, result.Value)
	}
	return converted, nil
}
