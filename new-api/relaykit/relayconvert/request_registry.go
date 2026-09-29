package relayconvert

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"context"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	claudemessages "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/claude_messages"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/internal/convdiag"
	geminichat "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/gemini_chat"
	oaichat "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/oai_chat"
	oairesponses "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/oai_responses"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/internal/toolconv"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type RequestConverterFunc func(c context.Context, info convmeta.Meta, request any) (any, error)

type RequestConverterQuality string

const (
	RequestConverterQualityGood        RequestConverterQuality = "good"
	RequestConverterQualityFair        RequestConverterQuality = "fair"
	RequestConverterQualityDiscouraged RequestConverterQuality = "discouraged"
)

type RequestStep struct {
	Converter string
	From      types.RelayFormat
	To        types.RelayFormat
}

type RequestResult struct {
	Value       any
	From        types.RelayFormat
	To          types.RelayFormat
	Converter   string
	Quality     RequestConverterQuality
	Steps       []RequestStep
	Diagnostics []types.ConversionDiagnostic
}

type RequestConverterSpec struct {
	ID             string
	From           types.RelayFormat
	To             types.RelayFormat
	Quality        RequestConverterQuality
	Convert        RequestConverterFunc
	StepConverters []string
}

type requestConverterRoute struct {
	from types.RelayFormat
	to   types.RelayFormat
}

var (
	requestConverterMu           sync.RWMutex
	requestConverters            = make(map[string]RequestConverterSpec)
	requestConverterRoutes       = make(map[requestConverterRoute]string)
	requestConverterDirectRoutes = make(map[requestConverterRoute]string)
)

const (
	requestConverterClaudeToGemini    = "claude_messages_to_gemini_generate_content"
	requestConverterClaudeToResponses = "claude_messages_to_openai_responses"
	requestConverterGeminiToClaude    = "gemini_generate_content_to_claude_messages"
	requestConverterGeminiToResponses = "gemini_generate_content_to_openai_responses"
	requestConverterResponsesToClaude = ConverterOpenAIResponsesToClaudeMessages
)

const (
	ConverterNone                            = "none"
	ConverterClaudeMessagesToOpenAIChat      = "anthropic_messages_to_openai_chat_completions"
	ConverterOpenAIChatToClaudeMessages      = "openai_chat_completions_to_anthropic_messages"
	ConverterOpenAIChatToOpenAIResponses     = "openai_chat_completions_to_openai_responses"
	ConverterOpenAIResponsesToOpenAIChat     = "openai_responses_to_openai_chat_completions"
	ConverterOpenAIResponsesToClaudeMessages = "openai_responses_to_claude_messages"
	ConverterOpenAIResponsesToGemini         = "openai_responses_to_gemini_generate_content"
	ConverterGeminiContentToOpenAIChat       = "gemini_generate_content_to_openai_chat_completions"
	ConverterOpenAIChatToGeminiContent       = "openai_chat_completions_to_gemini_generate_content"
)

func registerBuiltinRequestConverter(spec RequestConverterSpec) {
	spec.ID = strings.TrimSpace(spec.ID)
	if spec.ID == "" {
		panic("request converter ID is required")
	}
	if spec.From == "" || spec.To == "" {
		panic(fmt.Sprintf("request converter %q must declare from and to formats", spec.ID))
	}
	if spec.Quality == "" {
		panic(fmt.Sprintf("request converter %q must declare quality", spec.ID))
	}
	if spec.Convert == nil && len(spec.StepConverters) == 0 {
		panic(fmt.Sprintf("request converter %q must declare convert or step converters", spec.ID))
	}
	if spec.Convert != nil && len(spec.StepConverters) > 0 {
		panic(fmt.Sprintf("request converter %q cannot declare convert and step converters together", spec.ID))
	}
	if _, exists := requestConverters[spec.ID]; exists {
		panic(fmt.Sprintf("request converter %q is already registered", spec.ID))
	}
	route := requestConverterRoute{from: spec.From, to: spec.To}
	if existingID, exists := requestConverterRoutes[route]; exists {
		panic(fmt.Sprintf("request converter route from %s to %s is already registered by %q", spec.From, spec.To, existingID))
	}

	if len(spec.StepConverters) > 0 {
		stepConverters := make([]string, 0, len(spec.StepConverters))
		current := spec.From
		for _, converterID := range spec.StepConverters {
			step, ok := requestConverters[converterID]
			if !ok {
				panic(fmt.Sprintf("request converter %q references unknown step converter %q", spec.ID, converterID))
			}
			if step.Convert == nil || len(step.StepConverters) > 0 {
				panic(fmt.Sprintf("request converter %q step %q must be a direct converter", spec.ID, converterID))
			}
			if step.From != current {
				panic(fmt.Sprintf("request converter %q step %q expects %s after %s", spec.ID, converterID, step.From, current))
			}
			stepConverters = append(stepConverters, converterID)
			current = step.To
		}
		if current != spec.To {
			panic(fmt.Sprintf("request converter %q ends at %s, expected %s", spec.ID, current, spec.To))
		}
		spec.StepConverters = stepConverters
	}

	requestConverters[spec.ID] = spec
	requestConverterRoutes[route] = spec.ID
	if len(spec.StepConverters) == 0 {
		requestConverterDirectRoutes[route] = spec.ID
	}
}

func LookupRequestConverter(converter string) (RequestConverterSpec, bool) {
	requestConverterMu.RLock()
	defer requestConverterMu.RUnlock()

	spec, ok := requestConverters[strings.TrimSpace(converter)]
	if !ok {
		return RequestConverterSpec{}, false
	}
	return cloneRequestConverterSpec(spec), true
}

func ConvertRequest(c context.Context, info convmeta.Meta, target types.RelayFormat, request any) (*RequestResult, error) {
	from, err := inferRequestRelayFormat(request)
	if err != nil {
		return nil, err
	}
	if target == "" {
		return nil, errors.New("target relay format is required")
	}
	if from == target {
		return &RequestResult{
			Value: request,
			From:  from,
			To:    target,
		}, nil
	}

	spec, ok := lookupRequestRoute(from, target)
	if !ok {
		return nil, fmt.Errorf("request converter from %s to %s is not registered", from, target)
	}
	return executeRequestSpec(c, info, from, target, request, spec)
}

func ConvertRequestVia(c context.Context, info convmeta.Meta, request any, path ...types.RelayFormat) (*RequestResult, error) {
	from, err := inferRequestRelayFormat(request)
	if err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, errors.New("request conversion path is required")
	}

	targets := make([]types.RelayFormat, 0, len(path))
	for _, format := range path {
		if format == "" {
			return nil, errors.New("request conversion path contains empty relay format")
		}
		targets = append(targets, format)
	}
	if targets[0] == from {
		targets = targets[1:]
	}
	if len(targets) == 0 {
		return &RequestResult{
			Value: request,
			From:  from,
			To:    from,
		}, nil
	}

	steps := make([]RequestConverterSpec, 0, len(targets))
	current := from
	for _, target := range targets {
		spec, ok := lookupRequestDirectRoute(current, target)
		if !ok {
			return nil, fmt.Errorf("request converter from %s to %s is not registered", current, target)
		}
		steps = append(steps, spec)
		current = target
	}
	return executeRequestSteps(c, info, from, targets[len(targets)-1], request, "", "", steps)
}

func ConvertRequestByID(c context.Context, info convmeta.Meta, converter string, request any) (*RequestResult, error) {
	from, err := inferRequestRelayFormat(request)
	if err != nil {
		return nil, err
	}

	spec, ok := LookupRequestConverter(converter)
	if !ok {
		return nil, fmt.Errorf("request converter %q is not registered", strings.TrimSpace(converter))
	}
	if spec.From != "" && spec.From != from {
		return nil, fmt.Errorf("request converter %q expects %s request, got %s", spec.ID, spec.From, from)
	}
	return executeRequestSpec(c, info, from, spec.To, request, spec)
}

func executeRequestSpec(c context.Context, info convmeta.Meta, from types.RelayFormat, target types.RelayFormat, request any, spec RequestConverterSpec) (*RequestResult, error) {
	steps, err := expandRequestConverterSteps(spec)
	if err != nil {
		return nil, err
	}
	return executeRequestSteps(c, info, from, target, request, spec.ID, spec.Quality, steps)
}

func executeRequestSteps(c context.Context, info convmeta.Meta, from types.RelayFormat, target types.RelayFormat, request any, converter string, quality RequestConverterQuality, specs []RequestConverterSpec) (*RequestResult, error) {
	c, diagnosticCollector := convdiag.WithCollector(c)
	current, tools, err := toolconv.ExtractRequest(from, request)
	if err != nil {
		return nil, err
	}
	steps := make([]RequestStep, 0, len(specs))
	for _, spec := range specs {
		current, err = prepareRequestForStep(current, spec, target)
		if err != nil {
			return nil, err
		}

		var step RequestStep
		current, step, err = executeRequestStep(c, info, spec, current)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}

	current, toolDiagnostics, err := toolconv.AttachRequest(target, current, tools, convmeta.OptionsOf(info))
	diagnostics := append(diagnosticCollector.Diagnostics(), toolDiagnostics...)
	for i := range diagnostics {
		if diagnostics[i].From == "" {
			diagnostics[i].From = from
		}
		if diagnostics[i].To == "" {
			diagnostics[i].To = target
		}
	}
	if err != nil {
		return &RequestResult{
			Value:       current,
			From:        from,
			To:          target,
			Quality:     quality,
			Steps:       steps,
			Diagnostics: diagnostics,
		}, err
	}
	if info != nil {
		for _, step := range steps {
			info.AppendRequestConversion(step.To)
		}
	}

	converters := make([]string, 0, len(steps))
	for _, step := range steps {
		converters = append(converters, step.Converter)
	}
	if converter == "" {
		converter = strings.Join(converters, ",")
	}
	return &RequestResult{
		Value:       current,
		From:        from,
		To:          target,
		Converter:   converter,
		Quality:     quality,
		Steps:       steps,
		Diagnostics: diagnostics,
	}, nil
}

func expandRequestConverterSteps(spec RequestConverterSpec) ([]RequestConverterSpec, error) {
	if len(spec.StepConverters) == 0 {
		if spec.Convert == nil {
			return nil, fmt.Errorf("request converter %q has no registered implementation", spec.ID)
		}
		return []RequestConverterSpec{spec}, nil
	}
	if spec.Convert != nil {
		return nil, fmt.Errorf("request converter %q cannot mix direct and step conversion", spec.ID)
	}

	steps := make([]RequestConverterSpec, 0, len(spec.StepConverters))
	current := spec.From
	for _, converterID := range spec.StepConverters {
		step, ok := LookupRequestConverter(converterID)
		if !ok {
			return nil, fmt.Errorf("request converter %q references missing step converter %q", spec.ID, converterID)
		}
		if step.Convert == nil || len(step.StepConverters) > 0 {
			return nil, fmt.Errorf("request converter %q step %q is not a direct converter", spec.ID, converterID)
		}
		if step.From != current {
			return nil, fmt.Errorf("request converter %q step %q expects %s request, got %s", spec.ID, converterID, step.From, current)
		}
		steps = append(steps, step)
		current = step.To
	}
	if current != spec.To {
		return nil, fmt.Errorf("request converter %q ends at %s, expected %s", spec.ID, current, spec.To)
	}
	return steps, nil
}

func executeRequestStep(c context.Context, info convmeta.Meta, spec RequestConverterSpec, request any) (any, RequestStep, error) {
	if spec.Convert == nil {
		return nil, RequestStep{}, fmt.Errorf("request converter %q has no registered implementation", spec.ID)
	}

	value, err := spec.Convert(c, info, request)
	if err != nil {
		return nil, RequestStep{}, err
	}
	return value, RequestStep{
		Converter: spec.ID,
		From:      spec.From,
		To:        spec.To,
	}, nil
}

func prepareRequestForStep(request any, spec RequestConverterSpec, finalTarget types.RelayFormat) (any, error) {
	if spec.From != types.RelayFormatOpenAIResponses || finalTarget != types.RelayFormatGemini {
		return request, nil
	}

	responsesRequest, ok := request.(*dto.OpenAIResponsesRequest)
	if !ok {
		if value, ok := request.(dto.OpenAIResponsesRequest); ok {
			responsesRequest = &value
		}
	}
	if responsesRequest == nil {
		return nil, fmt.Errorf("expected OpenAI responses request, got %T", request)
	}

	prepared, err := oairesponses.PrepareOpenAIResponsesRequest(*responsesRequest)
	if err != nil {
		return nil, err
	}
	return &prepared, nil
}

func lookupRequestRoute(from types.RelayFormat, to types.RelayFormat) (RequestConverterSpec, bool) {
	requestConverterMu.RLock()
	defer requestConverterMu.RUnlock()

	converterID, ok := requestConverterRoutes[requestConverterRoute{from: from, to: to}]
	if !ok {
		return RequestConverterSpec{}, false
	}
	spec, ok := requestConverters[converterID]
	return cloneRequestConverterSpec(spec), ok
}

func lookupRequestDirectRoute(from types.RelayFormat, to types.RelayFormat) (RequestConverterSpec, bool) {
	requestConverterMu.RLock()
	defer requestConverterMu.RUnlock()

	converterID, ok := requestConverterDirectRoutes[requestConverterRoute{from: from, to: to}]
	if !ok {
		return RequestConverterSpec{}, false
	}
	spec, ok := requestConverters[converterID]
	return cloneRequestConverterSpec(spec), ok
}

func cloneRequestConverterSpec(spec RequestConverterSpec) RequestConverterSpec {
	if len(spec.StepConverters) > 0 {
		spec.StepConverters = append([]string{}, spec.StepConverters...)
	}
	return spec
}

func inferRequestRelayFormat(request any) (types.RelayFormat, error) {
	if isNilRequest(request) {
		return "", errors.New("request is nil")
	}
	format, ok := convmeta.GuessRelayFormatFromRequest(request)
	if !ok {
		return "", fmt.Errorf("unsupported request type %T", request)
	}
	return format, nil
}

func isNilRequest(request any) bool {
	if request == nil {
		return true
	}
	value := reflect.ValueOf(request)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func convertChatRequestToResponses(c context.Context, _ convmeta.Meta, request any) (any, error) {
	chatRequest, ok := request.(*dto.GeneralOpenAIRequest)
	if !ok {
		if value, ok := request.(dto.GeneralOpenAIRequest); ok {
			chatRequest = &value
		}
	}
	if chatRequest == nil {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", request)
	}
	return oaichat.ChatCompletionsRequestToResponsesRequest(c, chatRequest)
}

func convertClaudeRequestToOpenAI(c context.Context, info convmeta.Meta, request any) (any, error) {
	claudeRequest, ok := request.(*dto.ClaudeRequest)
	if !ok {
		if value, ok := request.(dto.ClaudeRequest); ok {
			claudeRequest = &value
		}
	}
	if claudeRequest == nil {
		return nil, fmt.Errorf("expected Anthropic Messages request, got %T", request)
	}
	return claudemessages.ClaudeMessagesRequestToOpenAIChat(c, *claudeRequest, info)
}

func convertClaudeRequestToOpenAIResponses(c context.Context, info convmeta.Meta, request any) (any, error) {
	claudeRequest, ok := request.(*dto.ClaudeRequest)
	if !ok {
		if value, ok := request.(dto.ClaudeRequest); ok {
			claudeRequest = &value
		}
	}
	if claudeRequest == nil {
		return nil, fmt.Errorf("expected Anthropic Messages request, got %T", request)
	}
	return claudemessages.ClaudeMessagesRequestToOpenAIResponses(c, *claudeRequest, info)
}

func convertOpenAIRequestToClaude(c context.Context, info convmeta.Meta, request any) (any, error) {
	openAIRequest, ok := request.(*dto.GeneralOpenAIRequest)
	if !ok {
		if value, ok := request.(dto.GeneralOpenAIRequest); ok {
			openAIRequest = &value
		}
	}
	if openAIRequest == nil {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", request)
	}
	return oaichat.OpenAIChatRequestToClaudeMessages(c, info, *openAIRequest)
}

func convertGeminiRequestToOpenAI(c context.Context, info convmeta.Meta, request any) (any, error) {
	geminiRequest, ok := request.(*dto.GeminiChatRequest)
	if !ok {
		if value, ok := request.(dto.GeminiChatRequest); ok {
			geminiRequest = &value
		}
	}
	if geminiRequest == nil {
		return nil, fmt.Errorf("expected Gemini generateContent request, got %T", request)
	}
	return geminichat.GeminiGenerateContentRequestToOpenAIChat(c, geminiRequest, info)
}

func convertOpenAIRequestToGemini(c context.Context, info convmeta.Meta, request any) (any, error) {
	openAIRequest, ok := request.(*dto.GeneralOpenAIRequest)
	if !ok {
		if value, ok := request.(dto.GeneralOpenAIRequest); ok {
			openAIRequest = &value
		}
	}
	if openAIRequest == nil {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", request)
	}
	return oaichat.OpenAIChatRequestToGeminiGenerateContent(c, *openAIRequest, info)
}

func convertOpenAIResponsesRequestToClaudeMessages(c context.Context, info convmeta.Meta, request any) (any, error) {
	responsesRequest, err := oairesponses.OpenAIResponsesRequestFromAny(request)
	if err != nil {
		return nil, err
	}
	return oairesponses.OpenAIResponsesRequestToClaudeMessages(c, info, responsesRequest)
}

func convertOpenAIResponsesRequestToGeminiChat(c context.Context, info convmeta.Meta, request any) (any, error) {
	responsesRequest, err := oairesponses.OpenAIResponsesRequestFromAny(request)
	if err != nil {
		return nil, err
	}

	prepared, err := oairesponses.PrepareOpenAIResponsesRequest(*responsesRequest)
	if err != nil {
		return nil, err
	}
	return oairesponses.OpenAIResponsesRequestToGeminiChat(c, &prepared, info)
}

func convertResponsesRequestToChat(c context.Context, _ convmeta.Meta, request any) (any, error) {
	responsesRequest, ok := request.(*dto.OpenAIResponsesRequest)
	if !ok {
		if value, ok := request.(dto.OpenAIResponsesRequest); ok {
			responsesRequest = &value
		}
	}
	if responsesRequest == nil {
		return nil, fmt.Errorf("expected OpenAI responses request, got %T", request)
	}
	return oairesponses.ResponsesRequestToChatCompletionsRequest(c, responsesRequest)
}
