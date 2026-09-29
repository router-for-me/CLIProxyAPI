package relayconvert

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupBuiltinTextConverters(t *testing.T) {
	tests := []struct {
		id                        string
		from                      types.RelayFormat
		to                        types.RelayFormat
		quality                   TextConverterQuality
		reqSteps                  []string
		respSteps                 []string
		reqDirect                 bool
		respDirect                bool
		respAlias                 string
		streamDirect              bool
		skipStreamDirectAssertion bool
	}{
		{id: ConverterClaudeMessagesToOpenAIChat, from: types.RelayFormatClaude, to: types.RelayFormatOpenAI, quality: TextConverterQualityFair, reqDirect: true, respDirect: true, respAlias: ResponseConverterClaudeMessagesToOAIChat},
		{id: ConverterOpenAIChatToClaudeMessages, from: types.RelayFormatOpenAI, to: types.RelayFormatClaude, quality: TextConverterQualityFair, reqDirect: true, respDirect: true, respAlias: ResponseConverterOAIChatToClaudeMessages},
		{id: ConverterGeminiContentToOpenAIChat, from: types.RelayFormatGemini, to: types.RelayFormatOpenAI, quality: TextConverterQualityFair, reqDirect: true, respDirect: true, respAlias: ResponseConverterGeminiChatToOAIChat, streamDirect: true},
		{id: ConverterOpenAIChatToGeminiContent, from: types.RelayFormatOpenAI, to: types.RelayFormatGemini, quality: TextConverterQualityFair, reqDirect: true, respDirect: true, respAlias: ResponseConverterOAIChatToGeminiChat, skipStreamDirectAssertion: true},
		{id: ConverterOpenAIChatToOpenAIResponses, from: types.RelayFormatOpenAI, to: types.RelayFormatOpenAIResponses, quality: TextConverterQualityGood, reqDirect: true, respDirect: true, respAlias: ResponseConverterOAIChatToOAIResponses, streamDirect: true},
		{id: ConverterOpenAIResponsesToOpenAIChat, from: types.RelayFormatOpenAIResponses, to: types.RelayFormatOpenAI, quality: TextConverterQualityGood, reqDirect: true, respDirect: true, respAlias: ResponseConverterOAIResponsesToOAIChat, streamDirect: true},
		{
			id:      requestConverterClaudeToGemini,
			from:    types.RelayFormatClaude,
			to:      types.RelayFormatGemini,
			quality: TextConverterQualityDiscouraged,
			reqSteps: []string{
				ConverterClaudeMessagesToOpenAIChat,
				ConverterOpenAIChatToGeminiContent,
			},
			respSteps: []string{
				ConverterClaudeMessagesToOpenAIChat,
				ConverterOpenAIChatToGeminiContent,
			},
			respAlias: responseConverterClaudeToGemini,
		},
		{
			id:        requestConverterClaudeToResponses,
			from:      types.RelayFormatClaude,
			to:        types.RelayFormatOpenAIResponses,
			quality:   TextConverterQualityFair,
			reqDirect: true,
			respSteps: []string{
				ConverterClaudeMessagesToOpenAIChat,
				ConverterOpenAIChatToOpenAIResponses,
			},
			respAlias: responseConverterClaudeToResponses,
		},
		{
			id:      requestConverterGeminiToClaude,
			from:    types.RelayFormatGemini,
			to:      types.RelayFormatClaude,
			quality: TextConverterQualityDiscouraged,
			reqSteps: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToClaudeMessages,
			},
			respSteps: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToClaudeMessages,
			},
			respAlias: responseConverterGeminiToClaude,
		},
		{
			id:      requestConverterGeminiToResponses,
			from:    types.RelayFormatGemini,
			to:      types.RelayFormatOpenAIResponses,
			quality: TextConverterQualityFair,
			reqSteps: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToOpenAIResponses,
			},
			respSteps: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToOpenAIResponses,
			},
			respAlias: responseConverterGeminiToResponses,
		},
		{
			id:           requestConverterResponsesToClaude,
			from:         types.RelayFormatOpenAIResponses,
			to:           types.RelayFormatClaude,
			quality:      TextConverterQualityFair,
			reqDirect:    true,
			respDirect:   true,
			respAlias:    responseConverterResponsesToClaude,
			streamDirect: true,
		},
		{
			id:        ConverterOpenAIResponsesToGemini,
			from:      types.RelayFormatOpenAIResponses,
			to:        types.RelayFormatGemini,
			quality:   TextConverterQualityFair,
			reqDirect: true,
			respSteps: []string{
				ConverterOpenAIResponsesToOpenAIChat,
				ConverterOpenAIChatToGeminiContent,
			},
			respAlias: responseConverterResponsesToGemini,
		},
	}

	require.Len(t, textConverters, len(tests))

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			spec, ok := LookupTextConverter(tt.id)
			require.True(t, ok)
			assert.Equal(t, tt.id, spec.ID)
			assert.Equal(t, tt.from, spec.From)
			assert.Equal(t, tt.to, spec.To)
			assert.Equal(t, tt.quality, spec.Quality)
			assert.Equal(t, tt.reqSteps, spec.Req.StepConverters)
			assert.Equal(t, tt.respSteps, spec.Resp.StepConverters)
			assert.Equal(t, tt.reqDirect, spec.Req.Convert != nil)
			assert.Equal(t, tt.respDirect, spec.Resp.Convert != nil)
			if !tt.skipStreamDirectAssertion {
				assert.Equal(t, tt.streamDirect, spec.Resp.NewStreamState != nil && spec.Resp.ConvertStreamChunk != nil && spec.Resp.FinalizeStream != nil)
			}

			aliasSpec, ok := LookupTextConverter(tt.respAlias)
			require.True(t, ok)
			assert.Equal(t, tt.id, aliasSpec.ID)
		})
	}
}
