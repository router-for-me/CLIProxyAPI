package relayconvert

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertRequestDefaultPolicyAllowsGeminiCodeExecution(t *testing.T) {
	t.Parallel()

	tools, err := kitutil.Marshal([]map[string]any{{"codeExecution": map[string]any{}}})
	require.NoError(t, err)
	req := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{Role: "user", Parts: []dto.GeminiPart{{Text: "run this"}}},
		},
		Tools: tools,
	}

	result, err := ConvertRequest(nil, nil, types.RelayFormatOpenAI, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.IsType(t, &dto.GeneralOpenAIRequest{}, result.Value)
	assert.True(t, hasConversionDiagnosticCode(result.Diagnostics, "unsupported_hosted_tool"))
}

func TestConvertResponseStrictPolicyStillSucceedsOnContinuationLoss(t *testing.T) {
	t.Parallel()

	text := "hello"
	resp := &dto.ClaudeResponse{
		Id:         "msg_1",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-test",
		StopReason: "pause_turn",
		Content: []dto.ClaudeMediaMessage{
			{Type: "redacted_thinking", Data: "secret"},
			{Type: "text", Text: &text},
		},
	}
	info := &convmeta.Values{
		Options: &convmeta.Options{ToolLossPolicy: types.ConversionLossPolicyStrict},
	}

	result, err := ConvertResponse(nil, info, types.RelayFormatOpenAI, resp)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.IsType(t, &dto.OpenAITextResponse{}, result.Value)
	assert.True(t, hasConversionDiagnosticCode(result.Diagnostics, "continuation_state_lost"))
}

func TestConvertRequestSafePolicyReturnsConversionLossError(t *testing.T) {
	t.Parallel()

	tools, err := kitutil.Marshal([]map[string]any{{"codeExecution": map[string]any{}}})
	require.NoError(t, err)
	req := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{Role: "user", Parts: []dto.GeminiPart{{Text: "run this"}}},
		},
		Tools: tools,
	}
	info := &convmeta.Values{
		Options: &convmeta.Options{ToolLossPolicy: types.ConversionLossPolicySafe},
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatOpenAI, req)
	require.Error(t, err)
	var loss *types.ConversionLossError
	require.ErrorAs(t, err, &loss)
	require.NotEmpty(t, loss.Diagnostics)
	require.NotNil(t, result)
	assert.True(t, hasConversionDiagnosticCode(loss.Diagnostics, "unsupported_hosted_tool"))
	assert.True(t, hasConversionDiagnosticCode(result.Diagnostics, "unsupported_hosted_tool"))
}

func hasConversionDiagnosticCode(diagnostics []types.ConversionDiagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
