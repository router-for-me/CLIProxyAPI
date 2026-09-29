package relayconvert

import (
	"fmt"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/relaykit/types"
)

type TextConverterQuality string

const (
	TextConverterQualityGood        TextConverterQuality = "good"
	TextConverterQualityFair        TextConverterQuality = "fair"
	TextConverterQualityDiscouraged TextConverterQuality = "discouraged"
)

type TextRequestSide struct {
	Convert        RequestConverterFunc
	StepConverters []string
}

type TextResponseSide struct {
	Convert            ResponseConverterFunc
	ConvertStream      ResponseStreamConverterFunc
	NewStreamState     ResponseStreamStateFactory
	ConvertStreamChunk ResponseStreamChunkConverterFunc
	FinalizeStream     ResponseStreamFinalizerFunc
	StepConverters     []string
	Aliases            []string
}

type TextConverterSpec struct {
	ID      string
	From    types.RelayFormat
	To      types.RelayFormat
	Quality TextConverterQuality
	Req     TextRequestSide
	Resp    TextResponseSide
}

var (
	textConverterMu      sync.RWMutex
	textConverters       = make(map[string]TextConverterSpec)
	textConverterAliases = make(map[string]string)
)

var builtinTextConverters = []TextConverterSpec{
	{
		ID:      ConverterClaudeMessagesToOpenAIChat,
		From:    types.RelayFormatClaude,
		To:      types.RelayFormatOpenAI,
		Quality: TextConverterQualityFair,
		Req: TextRequestSide{
			Convert: convertClaudeRequestToOpenAI,
		},
		Resp: TextResponseSide{
			Convert:            convertClaudeMessagesResponseToOAIChat,
			ConvertStream:      convertClaudeMessagesStreamResponseToOAIChat,
			NewStreamState:     newClaudeMessagesToOAIChatStreamState,
			ConvertStreamChunk: convertClaudeMessagesStreamResponseChunkToOAIChat,
			Aliases:            []string{ResponseConverterClaudeMessagesToOAIChat},
		},
	},
	{
		ID:      ConverterOpenAIChatToClaudeMessages,
		From:    types.RelayFormatOpenAI,
		To:      types.RelayFormatClaude,
		Quality: TextConverterQualityFair,
		Req: TextRequestSide{
			Convert: convertOpenAIRequestToClaude,
		},
		Resp: TextResponseSide{
			Convert:        convertOAIChatResponseToClaudeMessages,
			ConvertStream:  convertOAIChatStreamResponseToClaudeMessages,
			FinalizeStream: finalizeOAIChatStreamResponseToClaudeMessages,
			Aliases:        []string{ResponseConverterOAIChatToClaudeMessages},
		},
	},
	{
		ID:      ConverterGeminiContentToOpenAIChat,
		From:    types.RelayFormatGemini,
		To:      types.RelayFormatOpenAI,
		Quality: TextConverterQualityFair,
		Req: TextRequestSide{
			Convert: convertGeminiRequestToOpenAI,
		},
		Resp: TextResponseSide{
			Convert:            convertGeminiChatResponseToOAIChat,
			ConvertStream:      convertGeminiChatStreamResponseToOAIChat,
			NewStreamState:     newGeminiChatToOAIChatStreamState,
			ConvertStreamChunk: convertGeminiChatStreamResponseChunkToOAIChat,
			FinalizeStream:     finalizeGeminiChatStreamResponseToOAIChat,
			Aliases:            []string{ResponseConverterGeminiChatToOAIChat},
		},
	},
	{
		ID:      ConverterOpenAIChatToGeminiContent,
		From:    types.RelayFormatOpenAI,
		To:      types.RelayFormatGemini,
		Quality: TextConverterQualityFair,
		Req: TextRequestSide{
			Convert: convertOpenAIRequestToGemini,
		},
		Resp: TextResponseSide{
			Convert:            convertOAIChatResponseToGeminiChat,
			ConvertStream:      convertOAIChatStreamResponseToGeminiChat,
			NewStreamState:     newOAIChatToGeminiStreamState,
			ConvertStreamChunk: convertOAIChatStreamResponseChunkToGeminiChat,
			FinalizeStream:     finalizeOAIChatStreamResponseToGeminiChat,
			Aliases:            []string{ResponseConverterOAIChatToGeminiChat},
		},
	},
	{
		ID:      ConverterOpenAIChatToOpenAIResponses,
		From:    types.RelayFormatOpenAI,
		To:      types.RelayFormatOpenAIResponses,
		Quality: TextConverterQualityGood,
		Req: TextRequestSide{
			Convert: convertChatRequestToResponses,
		},
		Resp: TextResponseSide{
			Convert:            convertOAIChatResponseToOAIResponses,
			NewStreamState:     newOAIChatToOAIResponsesStreamState,
			ConvertStreamChunk: convertOAIChatStreamResponseToOAIResponses,
			FinalizeStream:     finalizeOAIChatStreamResponseToOAIResponses,
			Aliases:            []string{ResponseConverterOAIChatToOAIResponses},
		},
	},
	{
		ID:      ConverterOpenAIResponsesToOpenAIChat,
		From:    types.RelayFormatOpenAIResponses,
		To:      types.RelayFormatOpenAI,
		Quality: TextConverterQualityGood,
		Req: TextRequestSide{
			Convert: convertResponsesRequestToChat,
		},
		Resp: TextResponseSide{
			Convert:            convertOAIResponsesResponseToOAIChat,
			NewStreamState:     newOAIResponsesToOAIChatStreamState,
			ConvertStreamChunk: convertOAIResponsesStreamResponseToOAIChat,
			FinalizeStream:     finalizeOAIResponsesStreamResponseToOAIChat,
			Aliases:            []string{ResponseConverterOAIResponsesToOAIChat},
		},
	},
	{
		ID:      requestConverterClaudeToGemini,
		From:    types.RelayFormatClaude,
		To:      types.RelayFormatGemini,
		Quality: TextConverterQualityDiscouraged,
		Req: TextRequestSide{
			StepConverters: []string{
				ConverterClaudeMessagesToOpenAIChat,
				ConverterOpenAIChatToGeminiContent,
			},
		},
		Resp: TextResponseSide{
			StepConverters: []string{
				ConverterClaudeMessagesToOpenAIChat,
				ConverterOpenAIChatToGeminiContent,
			},
			Aliases: []string{responseConverterClaudeToGemini},
		},
	},
	{
		ID:      requestConverterClaudeToResponses,
		From:    types.RelayFormatClaude,
		To:      types.RelayFormatOpenAIResponses,
		Quality: TextConverterQualityFair,
		Req: TextRequestSide{
			Convert: convertClaudeRequestToOpenAIResponses,
		},
		Resp: TextResponseSide{
			StepConverters: []string{
				ConverterClaudeMessagesToOpenAIChat,
				ConverterOpenAIChatToOpenAIResponses,
			},
			Aliases: []string{responseConverterClaudeToResponses},
		},
	},
	{
		ID:      requestConverterGeminiToClaude,
		From:    types.RelayFormatGemini,
		To:      types.RelayFormatClaude,
		Quality: TextConverterQualityDiscouraged,
		Req: TextRequestSide{
			StepConverters: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToClaudeMessages,
			},
		},
		Resp: TextResponseSide{
			StepConverters: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToClaudeMessages,
			},
			Aliases: []string{responseConverterGeminiToClaude},
		},
	},
	{
		ID:      requestConverterGeminiToResponses,
		From:    types.RelayFormatGemini,
		To:      types.RelayFormatOpenAIResponses,
		Quality: TextConverterQualityFair,
		Req: TextRequestSide{
			StepConverters: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToOpenAIResponses,
			},
		},
		Resp: TextResponseSide{
			StepConverters: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToOpenAIResponses,
			},
			Aliases: []string{responseConverterGeminiToResponses},
		},
	},
	{
		ID:      ConverterOpenAIResponsesToClaudeMessages,
		From:    types.RelayFormatOpenAIResponses,
		To:      types.RelayFormatClaude,
		Quality: TextConverterQualityFair,
		Req: TextRequestSide{
			Convert: convertOpenAIResponsesRequestToClaudeMessages,
		},
		Resp: TextResponseSide{
			Convert:            convertOAIResponsesResponseToClaudeMessages,
			NewStreamState:     newOAIResponsesToClaudeMessagesStreamState,
			ConvertStreamChunk: convertOAIResponsesStreamResponseToClaudeMessages,
			FinalizeStream:     finalizeOAIResponsesStreamResponseToClaudeMessages,
			Aliases:            []string{responseConverterResponsesToClaude},
		},
	},
	{
		ID:      ConverterOpenAIResponsesToGemini,
		From:    types.RelayFormatOpenAIResponses,
		To:      types.RelayFormatGemini,
		Quality: TextConverterQualityFair,
		Req: TextRequestSide{
			Convert: convertOpenAIResponsesRequestToGeminiChat,
		},
		Resp: TextResponseSide{
			StepConverters: []string{
				ConverterOpenAIResponsesToOpenAIChat,
				ConverterOpenAIChatToGeminiContent,
			},
			Aliases: []string{responseConverterResponsesToGemini},
		},
	},
}

func init() {
	for _, spec := range builtinTextConverters {
		registerBuiltinTextConverter(spec)
	}
}

func LookupTextConverter(converter string) (TextConverterSpec, bool) {
	textConverterMu.RLock()
	defer textConverterMu.RUnlock()

	converterID := resolveTextConverterID(converter)
	spec, ok := textConverters[converterID]
	if !ok {
		return TextConverterSpec{}, false
	}
	return cloneTextConverterSpec(spec), true
}

func registerBuiltinTextConverter(spec TextConverterSpec) {
	spec.ID = strings.TrimSpace(spec.ID)
	if spec.ID == "" {
		panic("text converter ID is required")
	}
	if spec.From == "" || spec.To == "" {
		panic(fmt.Sprintf("text converter %q must declare from and to formats", spec.ID))
	}
	if spec.Quality == "" {
		panic(fmt.Sprintf("text converter %q must declare quality", spec.ID))
	}
	if !textRequestSideConfigured(spec.Req) {
		panic(fmt.Sprintf("text converter %q must declare request conversion", spec.ID))
	}
	if !textResponseSideConfigured(spec.Resp) {
		panic(fmt.Sprintf("text converter %q must declare response conversion", spec.ID))
	}
	if _, exists := textConverters[spec.ID]; exists {
		panic(fmt.Sprintf("text converter %q is already registered", spec.ID))
	}

	registerBuiltinRequestConverter(RequestConverterSpec{
		ID:             spec.ID,
		From:           spec.From,
		To:             spec.To,
		Quality:        RequestConverterQuality(spec.Quality),
		Convert:        spec.Req.Convert,
		StepConverters: cloneTextConverterStrings(spec.Req.StepConverters),
	})
	registerBuiltinResponseConverter(ResponseConverterSpec{
		ID:                 spec.ID,
		From:               spec.From,
		To:                 spec.To,
		Quality:            ResponseConverterQuality(spec.Quality),
		Convert:            spec.Resp.Convert,
		ConvertStream:      spec.Resp.ConvertStream,
		NewStreamState:     spec.Resp.NewStreamState,
		ConvertStreamChunk: spec.Resp.ConvertStreamChunk,
		FinalizeStream:     spec.Resp.FinalizeStream,
		StepConverters:     cloneTextConverterStrings(spec.Resp.StepConverters),
	})

	textConverters[spec.ID] = cloneTextConverterSpec(spec)
	for _, alias := range spec.Resp.Aliases {
		registerResponseConverterAlias(alias, spec.ID)
		registerTextConverterAlias(alias, spec.ID)
	}
}

func registerTextConverterAlias(alias string, converter string) {
	alias = strings.TrimSpace(alias)
	converter = strings.TrimSpace(converter)
	if alias == "" {
		panic("text converter alias is required")
	}
	if converter == "" {
		panic(fmt.Sprintf("text converter alias %q target is required", alias))
	}
	if alias == converter {
		return
	}
	if _, exists := textConverters[alias]; exists {
		panic(fmt.Sprintf("text converter alias %q conflicts with registered converter", alias))
	}
	if _, exists := textConverters[converter]; !exists {
		panic(fmt.Sprintf("text converter alias %q references unknown converter %q", alias, converter))
	}
	if existing, exists := textConverterAliases[alias]; exists && existing != converter {
		panic(fmt.Sprintf("text converter alias %q is already registered for %q", alias, existing))
	}
	textConverterAliases[alias] = converter
}

func textRequestSideConfigured(side TextRequestSide) bool {
	return side.Convert != nil || len(side.StepConverters) > 0
}

func textResponseSideConfigured(side TextResponseSide) bool {
	return side.Convert != nil ||
		side.ConvertStream != nil ||
		side.NewStreamState != nil ||
		side.ConvertStreamChunk != nil ||
		side.FinalizeStream != nil ||
		len(side.StepConverters) > 0
}

func resolveTextConverterID(converter string) string {
	converter = strings.TrimSpace(converter)
	if canonical, ok := textConverterAliases[converter]; ok {
		return canonical
	}
	return converter
}

func cloneTextConverterSpec(spec TextConverterSpec) TextConverterSpec {
	spec.Req.StepConverters = cloneTextConverterStrings(spec.Req.StepConverters)
	spec.Resp.StepConverters = cloneTextConverterStrings(spec.Resp.StepConverters)
	spec.Resp.Aliases = cloneTextConverterStrings(spec.Resp.Aliases)
	return spec
}

func cloneTextConverterStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string{}, values...)
}
