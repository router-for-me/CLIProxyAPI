package toolconv

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func geminiCodeExecutionRequest(t *testing.T) *dto.GeminiChatRequest {
	t.Helper()
	tools, err := kitutil.Marshal([]map[string]any{{"codeExecution": map[string]any{}}})
	require.NoError(t, err)
	return &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{Role: "user", Parts: []dto.GeminiPart{{Text: "run this"}}},
		},
		Tools: tools,
	}
}

func hasDiagnosticCode(diagnostics []types.ConversionDiagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestDefaultPolicyAllowsGeminiCodeExecutionToOpenAI(t *testing.T) {
	t.Parallel()

	_, set, err := ExtractRequest(types.RelayFormatGemini, geminiCodeExecutionRequest(t))
	require.NoError(t, err)
	target := &dto.GeneralOpenAIRequest{
		Model:    "gpt-4o",
		Messages: []dto.Message{{Role: "user", Content: "run this"}},
	}

	out, diagnostics, err := AttachRequest(types.RelayFormatOpenAI, target, set, &convmeta.Options{})
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.True(t, hasDiagnosticCode(diagnostics, "unsupported_hosted_tool"))
	assert.Equal(t, types.ConversionLossPolicyAllow, (&convmeta.Options{}).EffectiveToolLossPolicy())
}

func TestResponsePhaseNeverRejectsEvenUnderStrictPolicy(t *testing.T) {
	t.Parallel()

	text := "hello"
	resp := &dto.ClaudeResponse{
		Type:       "message",
		Role:       "assistant",
		StopReason: "pause_turn",
		Content: []dto.ClaudeMediaMessage{
			{Type: "redacted_thinking", Data: "secret"},
			{Type: "text", Text: &text},
		},
	}
	diagnostics := InspectResponse(types.RelayFormatClaude, types.RelayFormatOpenAI, resp)
	require.True(t, hasDiagnosticCode(diagnostics, "continuation_state_lost"))
	require.Error(t, types.RejectConversionLoss(types.ConversionLossPolicyStrict, diagnostics))

	_, hosted, err := ExtractHostedResponse(types.RelayFormatClaude, resp)
	require.NoError(t, err)
	out, _, err := AttachHostedResponse(
		types.RelayFormatOpenAI,
		&dto.OpenAITextResponse{},
		hosted,
		&convmeta.Options{ToolLossPolicy: types.ConversionLossPolicyStrict},
	)
	require.NoError(t, err)
	require.NotNil(t, out)
}

func TestSafePolicyRejectsRequestPhaseHostedToolLoss(t *testing.T) {
	t.Parallel()

	_, set, err := ExtractRequest(types.RelayFormatGemini, geminiCodeExecutionRequest(t))
	require.NoError(t, err)
	target := &dto.GeneralOpenAIRequest{
		Model:    "gpt-4o",
		Messages: []dto.Message{{Role: "user", Content: "run this"}},
	}

	_, diagnostics, err := AttachRequest(
		types.RelayFormatOpenAI,
		target,
		set,
		&convmeta.Options{ToolLossPolicy: types.ConversionLossPolicySafe},
	)
	require.Error(t, err)
	var loss *types.ConversionLossError
	require.ErrorAs(t, err, &loss)
	require.NotEmpty(t, loss.Diagnostics)
	assert.True(t, hasDiagnosticCode(loss.Diagnostics, "unsupported_hosted_tool"))
	assert.True(t, hasDiagnosticCode(diagnostics, "unsupported_hosted_tool"))
}
