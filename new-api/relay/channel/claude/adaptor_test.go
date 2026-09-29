package claude

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertClaudeRequestTreatsZeroMaxTokensAsUnset(t *testing.T) {
	zero := uint(0)
	req := &dto.ClaudeRequest{
		Model:     "claude-sonnet-4-5",
		MaxTokens: &zero,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "claude-sonnet-4-5",
		},
	}

	out, err := (&Adaptor{}).ConvertClaudeRequest(nil, info, req)
	require.NoError(t, err)
	converted, ok := out.(*dto.ClaudeRequest)
	require.True(t, ok)
	require.NotNil(t, converted.MaxTokens)
	assert.Equal(t, uint(model_setting.GetClaudeSettings().GetDefaultMaxTokens(req.Model)), *converted.MaxTokens)
}

func TestConvertClaudeRequestPreservesNativeClaudeCodeThinking(t *testing.T) {
	budget := 10000
	maxTokens := uint(20000)
	temperature := 0.7
	topP := 0.9
	req := &dto.ClaudeRequest{
		Model:        "claude-opus-4-8",
		MaxTokens:    &maxTokens,
		Temperature:  &temperature,
		TopP:         &topP,
		Thinking:     &dto.Thinking{Type: "enabled", BudgetTokens: &budget},
		OutputConfig: []byte(`{"effort":"high"}`),
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: req.Model,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: req.Model,
		},
	}

	out, err := (&Adaptor{}).ConvertClaudeRequest(nil, info, req)
	require.NoError(t, err)
	converted, ok := out.(*dto.ClaudeRequest)
	require.True(t, ok)
	require.NotNil(t, converted.Thinking)
	assert.Equal(t, "enabled", converted.Thinking.Type)
	require.NotNil(t, converted.Thinking.BudgetTokens)
	assert.Equal(t, budget, *converted.Thinking.BudgetTokens)
	assert.Equal(t, temperature, *converted.Temperature)
	assert.Equal(t, topP, *converted.TopP)
	assert.JSONEq(t, `{"effort":"high"}`, string(converted.OutputConfig))
	assert.Empty(t, info.ConversionDiagnostics())
}

func TestConvertClaudeRequestPreservesMessageOutputConfig(t *testing.T) {
	body := `{"model":"claude-opus-5-5","max_tokens":64,"output_config":{"effort":"medium"},"messages":[` +
		`{"role":"user","content":"summary"},` +
		`{"role":"system","content":[],"output_config":{"effort":"high"}},` +
		`{"role":"assistant","content":"done"}]}`
	var req dto.ClaudeRequest
	require.NoError(t, common.UnmarshalJsonStr(body, &req))
	info := &relaycommon.RelayInfo{
		OriginModelName: req.Model,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: req.Model,
		},
	}

	out, err := (&Adaptor{}).ConvertClaudeRequest(nil, info, &req)
	require.NoError(t, err)
	encoded, err := common.Marshal(out)
	require.NoError(t, err)

	var upstream struct {
		Messages []map[string]any `json:"messages"`
	}
	require.NoError(t, common.Unmarshal(encoded, &upstream))
	require.Len(t, upstream.Messages, 3)
	assert.Equal(t, map[string]any{"effort": "high"}, upstream.Messages[1]["output_config"])
	assert.NotContains(t, upstream.Messages[0], "output_config")
	assert.NotContains(t, upstream.Messages[2], "output_config")
}

func TestConvertClaudeRequestZeroMaxTokensStillRaisesThinkingBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	zero := uint(0)
	original := &dto.ClaudeRequest{
		Model:     "claude-3-7-sonnet-thinking",
		MaxTokens: &zero,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet-thinking",
		Request:         original,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "claude-3-7-sonnet-thinking",
		},
	}
	outbound, err := common.DeepCopy(original)
	require.NoError(t, err)
	require.NoError(t, helper.ModelMappedHelper(c, info, outbound))
	err = helper.ApplyReasoningModelSuffix(nil, info, outbound)
	require.NoError(t, err)

	out, err := (&Adaptor{}).ConvertClaudeRequest(nil, info, outbound)
	require.NoError(t, err)
	converted, ok := out.(*dto.ClaudeRequest)
	require.True(t, ok)
	assert.Equal(t, "claude-3-7-sonnet", converted.Model)
	require.NotNil(t, converted.Thinking)
	require.NotNil(t, converted.MaxTokens)
	assert.Greater(t, *converted.MaxTokens, uint(1024))
}

func TestConvertClaudeRequestDoesNotOverwriteTrimmedUpstreamModelName(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model: "claude-3-7-sonnet-thinking",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "claude-3-7-sonnet",
		},
	}

	_, err := (&Adaptor{}).ConvertClaudeRequest(nil, info, req)
	require.NoError(t, err)
	assert.Equal(t, "claude-3-7-sonnet", info.UpstreamModelName)
}

func geminiToClaudeInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "claude-3-7-sonnet",
		},
	}
}

func TestConvertGeminiRequestMapsSystemInstructionToolsAndMultimodal(t *testing.T) {
	req := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{
				Role: "user",
				Parts: []dto.GeminiPart{
					{Text: "What is in this image?"},
					{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "aGVsbG8="}},
				},
			},
		},
		SystemInstructions: &dto.GeminiChatContent{
			Parts: []dto.GeminiPart{{Text: "You are a helpful assistant."}},
		},
	}
	req.SetTools([]dto.GeminiChatTool{
		{
			FunctionDeclarations: []dto.FunctionRequest{
				{
					Name:        "lookup",
					Description: "Lookup data",
					Parameters: map[string]any{
						"type":       "object",
						"properties": map[string]any{"q": map[string]any{"type": "string"}},
					},
				},
			},
		},
	})

	out, err := (&Adaptor{}).ConvertGeminiRequest(nil, geminiToClaudeInfo(), req)
	require.NoError(t, err)
	converted, ok := out.(*dto.ClaudeRequest)
	require.True(t, ok)

	system := converted.ParseSystem()
	require.NotEmpty(t, system)
	assert.Contains(t, system[0].GetText(), "You are a helpful assistant.")
	require.NotEmpty(t, converted.Messages)
	assert.Equal(t, "user", converted.Messages[0].Role)

	blocks, parseErr := converted.Messages[0].ParseContent()
	require.NoError(t, parseErr)
	var foundImage bool
	for _, block := range blocks {
		if block.Type == "image" || (block.Source != nil && block.Source.Type == "base64") {
			foundImage = true
			break
		}
	}
	assert.True(t, foundImage)

	require.NotNil(t, converted.Tools)
	tools, err := common.Marshal(converted.Tools)
	require.NoError(t, err)
	assert.Contains(t, string(tools), `"lookup"`)
	require.NotNil(t, converted.MaxTokens)
	assert.Greater(t, *converted.MaxTokens, uint(0))
}

func TestConvertGeminiRequestThinkingConfigUsesReasoningIntent(t *testing.T) {
	budget := 1024
	maxTokens := uint(4096)
	req := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{Role: "user", Parts: []dto.GeminiPart{{Text: "think"}}},
		},
		GenerationConfig: dto.GeminiChatGenerationConfig{
			MaxOutputTokens: &maxTokens,
			ThinkingConfig:  &dto.GeminiThinkingConfig{ThinkingBudget: &budget},
		},
	}

	out, err := (&Adaptor{}).ConvertGeminiRequest(nil, geminiToClaudeInfo(), req)
	require.NoError(t, err)
	converted, ok := out.(*dto.ClaudeRequest)
	require.True(t, ok)
	require.NotNil(t, converted.Thinking)
	assert.Equal(t, "enabled", converted.Thinking.Type)
	require.NotNil(t, converted.Thinking.BudgetTokens)
	assert.Equal(t, 1024, *converted.Thinking.BudgetTokens)
}

func TestConvertGeminiRequestNilRequest(t *testing.T) {
	_, err := (&Adaptor{}).ConvertGeminiRequest(nil, geminiToClaudeInfo(), nil)
	require.Error(t, err)
}
