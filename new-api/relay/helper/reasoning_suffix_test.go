package helper

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	kitreasoning "github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func mustApplyReasoningModelSuffix(t *testing.T, info *relaycommon.RelayInfo, outbound ...dto.Request) {
	t.Helper()
	require.NoError(t, ApplyReasoningModelSuffix(nil, info, outbound...))
}

func TestApplyReasoningModelSuffixTrimsUpstreamAndAttachesState(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet-thinking",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "claude-3-7-sonnet-thinking",
		},
	}

	mustApplyReasoningModelSuffix(t, info)
	assert.Equal(t, "claude-3-7-sonnet", info.UpstreamModelName)
	require.NotNil(t, info.ReasoningConversion)
	assert.Equal(t, "enabled", info.ReasoningConversion.Mode)
}

func TestApplyReasoningModelSuffixRetryKeepsEquivalentState(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-opus-4-8-high",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "claude-opus-4-8-high",
		},
	}

	mustApplyReasoningModelSuffix(t, info)
	require.NotNil(t, info.ReasoningConversion)
	firstMode := info.ReasoningConversion.Mode
	firstEffort := info.ReasoningConversion.Effort

	info.UpstreamModelName = info.OriginModelName
	mustApplyReasoningModelSuffix(t, info)
	require.NotNil(t, info.ReasoningConversion)
	assert.Equal(t, firstMode, info.ReasoningConversion.Mode)
	assert.Equal(t, firstEffort, info.ReasoningConversion.Effort)
	assert.Equal(t, "claude-opus-4-8", info.UpstreamModelName)
}

func TestApplyReasoningModelSuffixRetryClearsStateWhenNewChannelHasNoSuffix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	req := &dto.ClaudeRequest{Model: "claude-3-7-sonnet"}
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet",
		Request:         req,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "claude-3-7-sonnet-thinking",
			IsModelMapped:     true,
		},
	}
	mustApplyReasoningModelSuffix(t, info)
	require.NotNil(t, info.ReasoningState())

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	common.SetContextKey(ctx, constant.ContextKeyOriginalModel, "claude-3-7-sonnet")
	common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeAnthropic)
	info.InitChannelMeta(ctx)
	assert.Nil(t, info.ReasoningState())

	mustApplyReasoningModelSuffix(t, info)
	assert.Nil(t, info.ReasoningState())
}

func TestApplyReasoningModelSuffixPassThroughDoesNotTrim(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	original := settings.PassThroughRequestEnabled
	t.Cleanup(func() { settings.PassThroughRequestEnabled = original })
	settings.PassThroughRequestEnabled = true

	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet-thinking",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "claude-3-7-sonnet-thinking",
		},
	}

	mustApplyReasoningModelSuffix(t, info)
	assert.Equal(t, "claude-3-7-sonnet-thinking", info.UpstreamModelName)
	assert.Nil(t, info.ReasoningConversion)
}

func TestApplyReasoningModelSuffixBlacklistDoesNotTrim(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	original := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = original })
	settings.ThinkingModelBlacklist = append(settings.ThinkingModelBlacklist, "claude-3-7-sonnet-thinking")

	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet-thinking",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "claude-3-7-sonnet-thinking",
		},
	}

	mustApplyReasoningModelSuffix(t, info)
	assert.Equal(t, "claude-3-7-sonnet-thinking", info.UpstreamModelName)
	assert.Nil(t, info.ReasoningConversion)
}

func TestApplyReasoningModelSuffixModifierOverridesExplicitConflict(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet-thinking",
		Request: &dto.ClaudeRequest{
			Model:    "claude-3-7-sonnet-thinking",
			Thinking: &dto.Thinking{Type: "disabled"},
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "claude-3-7-sonnet-thinking",
		},
	}

	mustApplyReasoningModelSuffix(t, info, info.Request)
	require.NotNil(t, info.ReasoningConversion)
	assert.Equal(t, "enabled", info.ReasoningConversion.Mode)
	assert.Nil(t, info.Request.(*dto.ClaudeRequest).Thinking)
	diagnostics := info.ConversionDiagnostics()
	require.NotEmpty(t, diagnostics)
	assert.Equal(t, "model_modifier_overrode_request", diagnostics[0].Code)
}

func TestApplyReasoningModelSuffixGeminiNoThinkingWhenAdapterEnabled(t *testing.T) {
	settings := model_setting.GetGeminiSettings()
	original := settings.ThinkingAdapterEnabled
	t.Cleanup(func() { settings.ThinkingAdapterEnabled = original })
	settings.ThinkingAdapterEnabled = true

	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash-nothinking",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-2.5-flash-nothinking",
		},
	}

	mustApplyReasoningModelSuffix(t, info)
	assert.Equal(t, "gemini-2.5-flash", info.UpstreamModelName)
	require.NotNil(t, info.ReasoningConversion)
	assert.Equal(t, "disabled", info.ReasoningConversion.Mode)
	assert.Equal(t, "none", info.ReasoningConversion.Effort)
}

func TestApplyReasoningModelSuffixPreservesEffortTailModelID(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "qwen-max",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "qwen-max",
		},
	}

	mustApplyReasoningModelSuffix(t, info)
	assert.Equal(t, "qwen-max", info.UpstreamModelName)
	assert.Nil(t, info.ReasoningConversion)
}

func TestApplyReasoningModelSuffixLeavesDeepSeekV4SuffixForAdaptor(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "deepseek-v4-chat-max",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeDeepSeek,
			UpstreamModelName: "deepseek-v4-chat-max",
		},
	}

	mustApplyReasoningModelSuffix(t, info)
	assert.Equal(t, "deepseek-v4-chat-max", info.UpstreamModelName)
	assert.Nil(t, info.ReasoningConversion)
}

func TestApplyReasoningModelSuffixLeavesVolcengineDeepSeekThinkingForAdaptor(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "deepseek-r1-thinking",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeVolcEngine,
			UpstreamModelName: "deepseek-r1-thinking",
		},
	}

	mustApplyReasoningModelSuffix(t, info)
	assert.Equal(t, "deepseek-r1-thinking", info.UpstreamModelName)
	assert.Nil(t, info.ReasoningConversion)
}

func TestApplyReasoningModelSuffixStillParsesOpenAIEffortTail(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1-high",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			UpstreamModelName: "gpt-5.1-high",
		},
	}

	mustApplyReasoningModelSuffix(t, info)
	assert.Equal(t, "gpt-5.1", info.UpstreamModelName)
	require.NotNil(t, info.ReasoningConversion)
	assert.Equal(t, "enabled", info.ReasoningConversion.Mode)
	assert.Equal(t, "high", info.ReasoningConversion.Effort)
}

func TestApplyReasoningModelSuffixLeavesUnknownOpenRouterThinkingModel(t *testing.T) {
	openRouter := &relaycommon.RelayInfo{
		OriginModelName: "some-model-thinking",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenRouter,
			UpstreamModelName: "some-model-thinking",
		},
	}
	mustApplyReasoningModelSuffix(t, openRouter)
	assert.Equal(t, "some-model-thinking", openRouter.UpstreamModelName)
	assert.Nil(t, openRouter.ReasoningConversion)
}

func TestApplyReasoningModelSuffixLeavesVersionedQwenMaxUntouched(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "qwen3.8-max",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "qwen3.8-max",
		},
	}

	mustApplyReasoningModelSuffix(t, info)
	assert.Equal(t, "qwen3.8-max", info.UpstreamModelName)
	assert.Empty(t, info.BillingModelName)
	assert.Nil(t, info.ReasoningConversion)
}

func TestApplyReasoningModelSuffixTreatsCloudflareAtAsModelName(t *testing.T) {
	const model = "@cf/meta/llama-3.1-8b-instruct"
	info := &relaycommon.RelayInfo{
		OriginModelName: model,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: model},
	}

	mustApplyReasoningModelSuffix(t, info)
	assert.Equal(t, model, info.UpstreamModelName)
	assert.Nil(t, info.ReasoningConversion)
}

func TestApplyReasoningModelSuffixAppliesExplicitModifierChain(t *testing.T) {
	temperature := 0.9
	topP := 1.0
	request := &dto.GeneralOpenAIRequest{
		Model:           "qwen3.8-max@thinking:on@effort:high@temperature:0.2@topp:0.8",
		ReasoningEffort: "low",
		Temperature:     &temperature,
		TopP:            &topP,
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: request.Model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: request.Model,
		},
	}

	mustApplyReasoningModelSuffix(t, info, request)
	assert.Equal(t, "qwen3.8-max", info.UpstreamModelName)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "qwen3.8-max", request.Model)
	assert.Equal(t, 0.2, *request.Temperature)
	assert.Equal(t, 0.8, *request.TopP)
	assert.Equal(t, "high", request.ReasoningEffort)
	require.NotNil(t, info.ReasoningConversion)
	assert.Equal(t, "enabled", info.ReasoningConversion.Mode)
	assert.Equal(t, "high", info.ReasoningConversion.Effort)
	assert.Contains(t, info.ConversionDiagnostics(), types.ConversionDiagnostic{
		Code:     "model_modifier_overrode_request",
		Path:     "model.@temperature",
		Message:  "model temperature modifier overrides request temperature 0.9",
		Severity: types.ConversionDiagnosticWarning,
	})
}

func TestApplyReasoningModelSuffixRejectsUnknownModifierKey(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "m@thinkin:on"}
	info := &relaycommon.RelayInfo{
		OriginModelName: request.Model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: request.Model,
		},
	}

	err := ApplyReasoningModelSuffix(nil, info, request)
	require.Error(t, err)
	assert.True(t, kitreasoning.IsClientError(err))
	assert.Contains(t, err.Error(), `unsupported model modifier "thinkin"`)
	assert.Contains(t, err.Error(), "Models that skip thinking suffix processing")
	assert.Contains(t, err.Error(), "re:")
	assert.Equal(t, "m@thinkin:on", info.UpstreamModelName)
	assert.Nil(t, info.ReasoningConversion)
}

func TestApplyReasoningModelSuffixThinkingOnOnly(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "qwen3.8-max@thinking:on"}
	info := &relaycommon.RelayInfo{
		OriginModelName: request.Model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: request.Model,
		},
	}

	mustApplyReasoningModelSuffix(t, info, request)
	assert.Equal(t, "qwen3.8-max", info.UpstreamModelName)
	assert.Equal(t, "", request.ReasoningEffort)
	require.NotNil(t, info.ReasoningConversion)
	assert.Equal(t, "enabled", info.ReasoningConversion.Mode)
	assert.Equal(t, "", info.ReasoningConversion.Effort)
}

func TestApplyReasoningModelSuffixThinkingOff(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "qwen3.8-max@thinking:off"}
	info := &relaycommon.RelayInfo{
		OriginModelName: request.Model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: request.Model,
		},
	}

	mustApplyReasoningModelSuffix(t, info, request)
	assert.Equal(t, "none", request.ReasoningEffort)
	require.NotNil(t, info.ReasoningConversion)
	assert.Equal(t, "disabled", info.ReasoningConversion.Mode)
	assert.Equal(t, "none", info.ReasoningConversion.Effort)
}

func TestApplyReasoningModelSuffixThinkingLegacyValuesRejected(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "qwen3.8-max@thinking:enabled"}
	info := &relaycommon.RelayInfo{
		OriginModelName: request.Model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: request.Model,
		},
	}

	err := ApplyReasoningModelSuffix(nil, info, request)
	require.Error(t, err)
	assert.True(t, kitreasoning.IsClientError(err))
	assert.Contains(t, err.Error(), `invalid thinking modifier value "enabled"`)
	assert.Contains(t, err.Error(), "Models that skip thinking suffix processing")
	assert.Equal(t, "qwen3.8-max@thinking:enabled", info.UpstreamModelName)
	assert.Equal(t, "qwen3.8-max@thinking:enabled", request.Model)
	assert.Nil(t, info.ReasoningConversion)
	assert.Empty(t, info.ConversionDiagnostics())
}

func TestApplyReasoningModelSuffixDuplicateModifierLastWins(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "qwen3.8-max@thinking:on@thinking:off"}
	info := &relaycommon.RelayInfo{
		OriginModelName: request.Model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: request.Model,
		},
	}

	mustApplyReasoningModelSuffix(t, info, request)
	assert.Equal(t, "qwen3.8-max", info.UpstreamModelName)
	require.NotNil(t, info.ReasoningConversion)
	assert.Equal(t, "disabled", info.ReasoningConversion.Mode)
	assert.Contains(t, info.ConversionDiagnostics(), types.ConversionDiagnostic{
		Code:     "duplicate_model_modifier",
		Path:     "model.@thinking",
		Message:  "model modifier \"thinking\" is repeated; the rightmost value is used",
		Severity: types.ConversionDiagnosticWarning,
	})
}

func TestApplyReasoningModelSuffixExactExemptionKeepsOpaqueName(t *testing.T) {
	const model = "opaque@sha256:abc"
	settings := model_setting.GetGlobalSettings()
	original := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = original })
	settings.ThinkingModelBlacklist = append(original, model)

	request := &dto.GeneralOpenAIRequest{Model: model}
	info := &relaycommon.RelayInfo{
		OriginModelName: model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: model,
		},
	}

	mustApplyReasoningModelSuffix(t, info, request)
	assert.Equal(t, model, info.UpstreamModelName)
	assert.Equal(t, model, request.Model)
	assert.Nil(t, info.ReasoningConversion)
	assert.Empty(t, info.ConversionDiagnostics())
}

func TestApplyReasoningModelSuffixRegexExemptionKeepsOpaqueName(t *testing.T) {
	const model = "m@sha256:abc"
	settings := model_setting.GetGlobalSettings()
	original := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = original })
	settings.ThinkingModelBlacklist = append(original, "re:.*@sha256:.*")

	request := &dto.GeneralOpenAIRequest{Model: model}
	info := &relaycommon.RelayInfo{
		OriginModelName: model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: model,
		},
	}

	mustApplyReasoningModelSuffix(t, info, request)
	assert.Equal(t, model, info.UpstreamModelName)
	assert.Equal(t, model, request.Model)
	assert.Nil(t, info.ReasoningConversion)
	assert.Empty(t, info.ConversionDiagnostics())
}

func TestApplyReasoningModelSuffixEffortNoneDisables(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "qwen3.8-max@effort:none"}
	info := &relaycommon.RelayInfo{
		OriginModelName: request.Model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: request.Model,
		},
	}

	mustApplyReasoningModelSuffix(t, info, request)
	assert.Equal(t, "none", request.ReasoningEffort)
	require.NotNil(t, info.ReasoningConversion)
	assert.Equal(t, "disabled", info.ReasoningConversion.Mode)
	assert.Equal(t, "none", info.ReasoningConversion.Effort)
}

func TestApplyReasoningModelSuffixPassThroughKeepsModifierBodyVerbatim(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	original := settings.PassThroughRequestEnabled
	t.Cleanup(func() { settings.PassThroughRequestEnabled = original })
	settings.PassThroughRequestEnabled = true

	gin.SetMode(gin.TestMode)
	const model = "qwen3.8-max@thinking:on@effort:high@temperature:0.2@topp:0.8"
	body := `{"model":"` + model + `","messages":[],"vendor_extension":{"keep":true}}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	request := &dto.GeneralOpenAIRequest{Model: model}
	info := &relaycommon.RelayInfo{
		OriginModelName: model,
		Request:         request,
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: model,
		},
	}

	require.NoError(t, ApplyReasoningModelSuffix(c, info, request))
	assert.Equal(t, model, info.UpstreamModelName)
	assert.Equal(t, model, request.Model)
	assert.Nil(t, info.ReasoningConversion)
	assert.Empty(t, info.ConversionDiagnostics())
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	got, err := storage.Bytes()
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
}

func TestApplyReasoningModelSuffixChannelPassThroughKeepsModifierBodyVerbatim(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const model = "qwen3.8-max@thinking:on"
	body := `{"model":"` + model + `","messages":[]}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	request := &dto.GeneralOpenAIRequest{Model: model}
	info := &relaycommon.RelayInfo{
		OriginModelName: model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: model,
			ChannelSetting:    dto.ChannelSettings{PassThroughBodyEnabled: true},
		},
	}

	require.NoError(t, ApplyReasoningModelSuffix(c, info, request))
	assert.Equal(t, model, info.UpstreamModelName)
	assert.Nil(t, info.ReasoningConversion)
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	got, err := storage.Bytes()
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
}

func TestApplyReasoningModelSuffixPassThroughAllowsUnknownModifier(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	original := settings.PassThroughRequestEnabled
	t.Cleanup(func() { settings.PassThroughRequestEnabled = original })
	settings.PassThroughRequestEnabled = true

	const model = "m@sha256:abc"
	request := &dto.GeneralOpenAIRequest{Model: model}
	info := &relaycommon.RelayInfo{
		OriginModelName: model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: model,
		},
	}

	mustApplyReasoningModelSuffix(t, info, request)
	assert.Equal(t, model, info.UpstreamModelName)
	assert.Nil(t, info.ReasoningConversion)
}

func TestApplyReasoningModelSuffixAppliesModifiersWhenPassThroughOff(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "qwen3.8-max@thinking:on@effort:high"}
	info := &relaycommon.RelayInfo{
		OriginModelName: request.Model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: request.Model,
		},
	}

	mustApplyReasoningModelSuffix(t, info, request)
	assert.Equal(t, "qwen3.8-max", info.UpstreamModelName)
	assert.Equal(t, "high", request.ReasoningEffort)
	require.NotNil(t, info.ReasoningConversion)
	assert.Equal(t, "enabled", info.ReasoningConversion.Mode)
}

func TestApplyReasoningModelSuffixThinkingMinusOnePassthrough(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "m@thinking:-1"}
	info := &relaycommon.RelayInfo{
		OriginModelName: request.Model,
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: request.Model,
		},
	}

	require.NotPanics(t, func() {
		mustApplyReasoningModelSuffix(t, info, request)
	})
	assert.Equal(t, "m", info.UpstreamModelName)
	require.NotNil(t, info.ReasoningConversion)
	require.NotNil(t, info.ReasoningConversion.BudgetTokens)
	assert.Equal(t, -1, *info.ReasoningConversion.BudgetTokens)
	require.NotEmpty(t, request.Reasoning)
	assert.Equal(t, float64(-1), gjson.GetBytes(request.Reasoning, "max_tokens").Num)
}

func TestApplyReasoningModelSuffixPreservesGpt51CodexMax(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1-codex-max",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-5.1-codex-max",
		},
	}

	mustApplyReasoningModelSuffix(t, info)
	assert.Equal(t, "gpt-5.1-codex-max", info.UpstreamModelName)
	assert.Nil(t, info.ReasoningConversion)
	assert.Equal(t, "gpt-5.1-codex-max", hostreasoning.BaseModelName("gpt-5.1-codex-max"))
}
