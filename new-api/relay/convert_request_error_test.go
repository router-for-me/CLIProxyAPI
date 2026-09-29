package relay

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	kitreasoning "github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type imageReservation struct {
	held, limit int
}

func (s *imageReservation) Reserve(quota int) error {
	if quota > s.limit {
		return errors.New("insufficient image quota")
	}
	s.held = max(s.held, quota)
	return nil
}
func (s *imageReservation) GetPreConsumedQuota() int { return s.held }
func (*imageReservation) Settle(int) error           { return nil }
func (*imageReservation) Refund(*gin.Context)        {}
func (*imageReservation) NeedsRefund() bool          { return false }

func TestImageRequestReservesFinalQuantityBeforeUpstream(t *testing.T) {
	service.InitHttpClient()
	for _, tc := range []struct {
		name, body                                    string
		tiered, passThrough, insufficient, retryReset bool
		override                                      any
		count, status                                 int
	}{
		{name: "top-level count", body: `{"model":"gpt-image-2","n":2,"parameters":{}}`, count: 2},
		{name: "legacy channel quantity override", body: `{"model":"gpt-image-2","n":1}`, override: 4, count: 4},
		{name: "expression channel quantity override", body: `{"model":"gpt-image-2","n":1}`, override: 4, count: 4, tiered: true},
		{name: "pass-through quantity", body: `{"model":"gpt-image-2","n":2,"parameters":{}}`, count: 2, passThrough: true},
		{name: "oversized override rejected", body: `{"model":"gpt-image-2","n":1}`, override: 129, status: http.StatusBadRequest},
		{name: "insufficient reservation blocks upstream", body: `{"model":"gpt-image-2","n":1}`, override: 4, count: 4, insufficient: true, status: http.StatusForbidden},
		{name: "retry drops the previous attempt's quantity", body: `{"model":"gpt-image-2","n":1}`, count: 1, retryReset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan []byte, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err == nil {
					received <- body
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"error":{"message":"fixture upstream failure","type":"upstream_error"}}`)
			}))
			t.Cleanup(upstream.Close)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
			common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: tc.passThrough})
			if tc.override != nil {
				common.SetContextKey(c, constant.ContextKeyChannelParamOverride, map[string]any{"operations": []any{map[string]any{"path": "n", "mode": "set", "value": tc.override}}})
			}
			request, err := helper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesGenerations)
			require.NoError(t, err)
			reservation := &imageReservation{held: 20000, limit: 500000}
			if tc.insufficient {
				reservation.limit = reservation.held
			}
			info := &relaycommon.RelayInfo{Request: request, OriginModelName: "gpt-image-2", RelayMode: relayconstant.RelayModeImagesGenerations,
				RequestURLPath: c.Request.URL.Path, Billing: reservation,
				PriceData: hosttypes.PriceData{UsePrice: true, ModelPrice: 0.04, GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}},
			}
			if tc.tiered {
				expr := `tier("image", fixed(0.04)) * image_count`
				info.TieredBillingSnapshot = &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ExprString: expr, ExprHash: billingexpr.ExprHashString(expr), GroupRatio: 1, QuotaPerUnit: common.QuotaPerUnit, EstimatedImageCount: common.GetPointer(1)}
				info.BillingRequestInput = &billingexpr.RequestInput{Body: []byte(tc.body), ImageCount: common.GetPointer(1)}
				info.PriceData.UsePrice = false
			}
			if tc.retryReset {
				reservation.held = 80000
				info.PriceData.AddOtherRatio("n", 4)
				info.BillingImageCount = common.GetPointer(3)
			}
			apiErr := ImageHelper(c, info)
			require.NotNil(t, apiErr)
			if tc.status != 0 {
				assert.Equal(t, tc.status, apiErr.StatusCode)
				assert.Empty(t, received, "no upstream submission before quantity validation and reservation")
				return
			}
			assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
			require.Len(t, received, 1)
			body := <-received
			assert.Equal(t, int64(tc.count), gjson.GetBytes(body, "n").Int())
			assert.Equal(t, tc.count*20000, info.PriceData.QuotaToPreConsume)
			assert.GreaterOrEqual(t, reservation.held, info.PriceData.QuotaToPreConsume)
			assert.Nil(t, info.BillingImageCount)
			if tc.tiered {
				assert.Equal(t, tc.body, string(info.BillingRequestInput.Body))
				assert.Equal(t, 1, *info.BillingRequestInput.ImageCount)
			}
		})
	}
}

func TestOptInSafeToolLossRejectedAsBadRequestWithAdminDiagnostics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-4o",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-4o",
			ChannelOtherSettings: dto.ChannelOtherSettings{
				ToolLossPolicy: string(types.ConversionLossPolicySafe),
			},
		},
	}

	tools, err := common.Marshal([]map[string]any{{"codeExecution": map[string]any{}}})
	require.NoError(t, err)
	req := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{Role: "user", Parts: []dto.GeminiPart{{Text: "run this"}}},
		},
		Tools: tools,
	}

	result, convErr := service.ConvertRequest(c, info, types.RelayFormatOpenAI, req)
	require.Error(t, convErr)
	var loss *types.ConversionLossError
	require.ErrorAs(t, convErr, &loss)
	require.NotEmpty(t, loss.Diagnostics)
	require.NotNil(t, result)

	apiErr := newConvertRequestFailedError(c, info, convErr)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeConvertRequestFailed, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))

	diagnostics := info.ConversionDiagnostics()
	require.NotEmpty(t, diagnostics)
	assert.True(t, hasHostDiagnosticCode(diagnostics, "unsupported_hosted_tool"))

	other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 0, 0, 1)
	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, adminInfo, "conversion_diagnostics")
}

func TestUnknownModelModifierIsBadRequestWithoutRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		OriginModelName: "m@thinkin:on",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "m@thinkin:on",
		},
	}
	err := helper.ApplyReasoningModelSuffix(c, info)
	require.Error(t, err)
	require.True(t, kitreasoning.IsClientError(err))
	assert.Contains(t, err.Error(), `unsupported model modifier "thinkin"`)
	assert.Contains(t, err.Error(), "re:")

	apiErr := newConvertRequestFailedError(c, info, err)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeConvertRequestFailed, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr))
}

func hasHostDiagnosticCode(diagnostics []types.ConversionDiagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestGeminiThinkingControlsConvertBestEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := model_setting.GetGeminiSettings()
	originalAdapter := settings.ThinkingAdapterEnabled
	t.Cleanup(func() { settings.ThinkingAdapterEnabled = originalAdapter })

	t.Run("openai thinking_budget on gemini 3 becomes thinkingLevel", func(t *testing.T) {
		settings.ThinkingAdapterEnabled = false
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req := &dto.GeneralOpenAIRequest{
			Model:     "gemini-3.1-pro-preview-thinking",
			Messages:  []dto.Message{{Role: "user", Content: "hello"}},
			ExtraBody: []byte(`{"google":{"thinking_config":{"thinking_budget":8192}}}`),
		}
		info := &relaycommon.RelayInfo{
			RelayFormat:     types.RelayFormatOpenAI,
			OriginModelName: "gemini-3.1-pro-preview-thinking",
			Request:         req,
			ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-pro-preview"},
		}
		require.NoError(t, helper.ApplyReasoningModelSuffix(c, info, req))

		result, err := service.ConvertRequest(c, info, types.RelayFormatGemini, req)
		require.NoError(t, err)
		converted, ok := result.Value.(*dto.GeminiChatRequest)
		require.True(t, ok)
		require.NotNil(t, converted.GenerationConfig.ThinkingConfig)
		assert.Equal(t, "medium", converted.GenerationConfig.ThinkingConfig.ThinkingLevel)
		assert.Nil(t, converted.GenerationConfig.ThinkingConfig.ThinkingBudget)
		assert.True(t, hasHostDiagnosticCode(info.ConversionDiagnostics(), "gemini_budget_to_level"))
	})

	t.Run("thinking alias with native thinking_level keeps the level", func(t *testing.T) {
		settings.ThinkingAdapterEnabled = true
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req := &dto.GeneralOpenAIRequest{
			Model:     "gemini-3.1-pro-preview-thinking",
			Messages:  []dto.Message{{Role: "user", Content: "hello"}},
			ExtraBody: []byte(`{"google":{"thinking_config":{"thinking_level":"low"}}}`),
		}
		info := &relaycommon.RelayInfo{
			RelayFormat:     types.RelayFormatOpenAI,
			OriginModelName: "gemini-3.1-pro-preview-thinking",
			Request:         req,
			ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-pro-preview-thinking"},
		}
		require.NoError(t, helper.ApplyReasoningModelSuffix(c, info, req))
		assert.Equal(t, "gemini-3.1-pro-preview", info.UpstreamModelName)

		result, err := service.ConvertRequest(c, info, types.RelayFormatGemini, req)
		require.NoError(t, err)
		converted, ok := result.Value.(*dto.GeminiChatRequest)
		require.True(t, ok)
		require.NotNil(t, converted.GenerationConfig.ThinkingConfig)
		assert.Equal(t, "low", converted.GenerationConfig.ThinkingConfig.ThinkingLevel)
		assert.Nil(t, converted.GenerationConfig.ThinkingConfig.ThinkingBudget)
	})

	t.Run("gemini thinkingBudget converts to openai reasoning_effort", func(t *testing.T) {
		settings.ThinkingAdapterEnabled = false
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.1-pro-preview:generateContent", nil)
		budget := 8192
		req := &dto.GeminiChatRequest{
			Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{{Text: "hello"}}}},
			GenerationConfig: dto.GeminiChatGenerationConfig{
				ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingBudget: &budget},
			},
		}
		info := &relaycommon.RelayInfo{
			RelayFormat:     types.RelayFormatGemini,
			OriginModelName: "gemini-3.1-pro-preview",
			Request:         req,
			ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "gpt-5"},
		}

		result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAI, req)
		require.NoError(t, err)
		converted, ok := result.Value.(*dto.GeneralOpenAIRequest)
		require.True(t, ok)
		assert.Equal(t, "medium", converted.ReasoningEffort)
		assert.True(t, hasHostDiagnosticCode(info.ConversionDiagnostics(), "gemini_budget_to_level"))
	})
}

// An Ali image model the alibaba task plugin does not claim is rejected by the
// adaptor with a classified 400 that skips retries. ImageHelper must surface
// that classification unchanged instead of wrapping it into a retryable
// conversion failure that other channels would then be asked to serve.
func TestImageHelperKeepsAdaptorClassifiedConvertError(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"wanx-style-repaint-v1","prompt":"a cat"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeAli)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://dashscope.invalid")
	request, err := helper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesGenerations)
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{Request: request, OriginModelName: "wanx-style-repaint-v1", RelayMode: relayconstant.RelayModeImagesGenerations,
		RequestURLPath: c.Request.URL.Path, Billing: &imageReservation{limit: 500000},
		PriceData: hosttypes.PriceData{UsePrice: true, ModelPrice: 0.04, GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}},
	}

	apiErr := ImageHelper(c, info)

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr), "other channels must not be asked to serve the same name")
	assert.Contains(t, apiErr.Error(), "not served by the alibaba task plugin")
}
