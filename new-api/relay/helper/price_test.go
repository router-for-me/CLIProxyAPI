package helper

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelPriceHelperTieredUsesPreloadedRequestInput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"tiered-test-model":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"tiered-test-model":"param(\"stream\") == true ? tier(\"stream\", p * 3) : tier(\"base\", p * 2)"}`,
	}))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/channel/test/1", nil)
	req.Body = nil
	req.ContentLength = 0
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	ctx.Set("group", "default")

	info := &relaycommon.RelayInfo{
		OriginModelName: "tiered-test-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		RequestHeaders:  map[string]string{"Content-Type": "application/json"},
		BillingRequestInput: &billingexpr.RequestInput{
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    []byte(`{"stream":true}`),
		},
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{
		BillingRatios: map[string]float64{"n": 3},
	})
	require.NoError(t, err)
	require.Equal(t, 1500, priceData.QuotaToPreConsume)
	require.NotNil(t, info.TieredBillingSnapshot)
	require.Equal(t, "stream", info.TieredBillingSnapshot.EstimatedTier)
	require.Equal(t, billing_setting.BillingModeTieredExpr, info.TieredBillingSnapshot.BillingMode)
	require.Equal(t, common.QuotaPerUnit, info.TieredBillingSnapshot.QuotaPerUnit)
}

func TestFixedPricePreConsumeAndRealtimeRejection(t *testing.T) {
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"fixed-test":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"fixed-test":"len <= 32000 ? tier(\"short\", fixed(0.01)) : tier(\"long\", p * 2)"}`,
		"group_ratio_setting.group_ratio": `{"default":1.5}`,
	}))
	for _, tc := range []struct {
		name      string
		format    types.RelayFormat
		prompt    int
		wantError bool
	}{
		{"HTTP charges once", types.RelayFormatOpenAI, 0, false},
		{"Realtime rejects even unselected fixed branch", types.RelayFormatOpenAIRealtime, 50000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{OriginModelName: "fixed-test", UserGroup: "default", UsingGroup: "default", RelayFormat: tc.format, BillingRequestInput: &billingexpr.RequestInput{}}
			price, err := ModelPriceHelper(ctx, info, tc.prompt, &types.TokenCountMeta{})
			if tc.wantError {
				require.ErrorContains(t, err, "Realtime")
				assert.Nil(t, info.TieredBillingSnapshot)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 7500, price.QuotaToPreConsume)
			require.NotNil(t, info.TieredBillingSnapshot)
			assert.Equal(t, billingexpr.BillingUnitRequest, info.TieredBillingSnapshot.EstimatedBillingUnit)
			require.NotNil(t, info.TieredBillingSnapshot.EstimatedFixedPrice)
			assert.Equal(t, 0.01, *info.TieredBillingSnapshot.EstimatedFixedPrice)
		})
	}
}

func TestModelPriceHelperTieredInputPreConsumeMultiplier(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"tiered-fallback-model":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"tiered-fallback-model":"len <= 1200 ? tier(\"base\", p * 3 + c * 15) : tier(\"long\", p * 30 + c * 150)"}`,
		"group_ratio_setting.group_ratio": `{"default":1,"free":0}`,
	}))

	cases := []struct {
		name       string
		group      string
		prompt     int
		maxTokens  int
		multiplier float64
		expected   int
	}{
		{"default uses only input", "default", 1000, 0, 1, 1500},
		{"explicit output limit does not increase reservation", "default", 1000, 100, 1, 1500},
		{"fraction below one", "default", 1000, 0, 0.5, 750},
		{"fraction above one preserves context tier", "default", 1000, 0, 2.5, 3750},
		{"small input has no token floor", "default", 100, 0, 1, 150},
		{"zero input", "default", 0, 100, 1, 0},
		{"free group", "free", 1000, 0, 2.5, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req
			ctx.Set("group", tc.group)

			info := &relaycommon.RelayInfo{
				OriginModelName: "tiered-fallback-model",
				UserGroup:       tc.group,
				UsingGroup:      tc.group,
				RequestHeaders:  map[string]string{"Content-Type": "application/json"},
				BillingRequestInput: &billingexpr.RequestInput{
					Headers: map[string]string{"Content-Type": "application/json"},
					Body:    []byte(`{}`),
				},
			}

			operation_setting.GetQuotaSetting().PreConsumeMultiplier = tc.multiplier
			priceData, err := ModelPriceHelper(ctx, info, tc.prompt, &types.TokenCountMeta{MaxTokens: tc.maxTokens})
			require.NoError(t, err)
			assert.Equal(t, tc.expected, priceData.QuotaToPreConsume)
			require.NotNil(t, info.TieredBillingSnapshot)
			actual, err := billingexpr.ComputeTieredQuotaWithRequest(info.TieredBillingSnapshot, billingexpr.TokenParams{P: 1000, C: 100, Len: 1000}, *info.BillingRequestInput)
			require.NoError(t, err)
			wantActual := 2250
			if tc.group == "free" {
				wantActual = 0
			}
			assert.Equal(t, wantActual, actual.ActualQuotaAfterGroup, "reservation multiplier must not change settlement")
		})
	}
}

func TestModelPriceHelperTieredRejectsPreConsumeOverflow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"tiered-overflow-model":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"tiered-overflow-model":"tier(\"overflow\", p * 100000000000000000)"}`,
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "tiered-overflow-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		BillingRequestInput: &billingexpr.RequestInput{
			Body: []byte(`{}`),
		},
	}

	_, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})

	var clamp *common.QuotaClamp
	require.ErrorAs(t, err, &clamp)
	require.Equal(t, "QuotaRound", clamp.Op)
	require.Equal(t, common.QuotaClampOverflow, clamp.Kind)
}

func TestModelPriceHelperRequestBillingRatiosOnlyApplyToFixedPrice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	savedModelPrices := ratio_setting.ModelPrice2JSONString()
	savedModelRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedModelPrices))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedModelRatios))
	})

	modelPrices, err := common.Marshal(map[string]float64{
		"fixed-image-price":      0.04,
		"fractional-image-price": 0.0000012,
		"overflow-image-price":   float64(common.MaxQuota) / common.QuotaPerUnit / 2,
	})
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(modelPrices)))
	modelRatios, err := common.Marshal(map[string]float64{"ratio-image-price": 15})
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(modelRatios)))

	tests := []struct {
		name           string
		model          string
		wantQuota      int
		wantUsePrice   bool
		wantImageCount bool
	}{
		{
			name:           "fixed price applies image count",
			model:          "fixed-image-price",
			wantQuota:      180000,
			wantUsePrice:   true,
			wantImageCount: true,
		},
		{
			name:         "ratio price ignores request billing ratios",
			model:        "ratio-image-price",
			wantQuota:    15000,
			wantUsePrice: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Set("group", "default")
			info := &relaycommon.RelayInfo{
				OriginModelName: tt.model,
				UserGroup:       "default",
				UsingGroup:      "default",
			}
			meta := &types.TokenCountMeta{
				ImagePriceRatio: 3,
				BillingRatios:   map[string]float64{"n": 3},
			}

			priceData, err := ModelPriceHelper(ctx, info, 1000, meta)

			require.NoError(t, err)
			require.Equal(t, tt.wantQuota, priceData.QuotaToPreConsume)
			require.Equal(t, tt.wantUsePrice, priceData.UsePrice)
			require.Equal(t, tt.wantImageCount, priceData.HasOtherRatio("n"))
			require.Equal(t, priceData.OtherRatios(), info.PriceData.OtherRatios())
		})
	}

	newInfo := func(model string) (*gin.Context, *relaycommon.RelayInfo) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Set("group", "default")
		return ctx, &relaycommon.RelayInfo{
			OriginModelName: model,
			UserGroup:       "default",
			UsingGroup:      "default",
		}
	}
	meta := &types.TokenCountMeta{BillingRatios: map[string]float64{"n": 3}}

	ctx, info := newInfo("fractional-image-price")
	priceData, err := ModelPriceHelper(ctx, info, 0, meta)
	require.NoError(t, err)
	// 0.0000012 * 500000 * 3 = 1.8, then truncate once to 1.
	require.Equal(t, 1, priceData.QuotaToPreConsume)

	ctx, info = newInfo("overflow-image-price")
	_, err = ModelPriceHelper(ctx, info, 0, meta)
	var clamp *common.QuotaClamp
	require.ErrorAs(t, err, &clamp)
	require.Equal(t, "QuotaFromFloat", clamp.Op)
	require.Equal(t, common.QuotaClampOverflow, clamp.Kind)
	require.Nil(t, info.Billing)
}

// Pricing identity is resolved once in ModelPriceHelper via the candidate
// ladder: raw name (only when it has no @ modifiers) → canonical
// base@effort:E@thinking:S → base@thinking:S → base. Each level is looked up
// after FormatMatchingModelName wildcard normalization. A hit on the raw
// gemini-2.5-flash-thinking-* wildcard must keep the client origin as the
// consume-log name.
func TestModelPriceHelperUsesSuffixedOriginLikeMain(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["gemini-2.5-flash"] = 0.15
	ratios["gemini-2.5-flash-thinking-*"] = 0.075
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = true
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")

	suffixed := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash-thinking-8192",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	suffixedPrice, err := ModelPriceHelper(ctx, suffixed, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, suffixed.BillingModelName)
	assert.Equal(t, "gemini-2.5-flash-thinking-8192", suffixed.GetBillingModelName())
	assert.Equal(t, 0.075, suffixedPrice.ModelRatio)

	geminiSettings := model_setting.GetGeminiSettings()
	oldThinking := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = oldThinking })

	adapterOn := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash-thinking-8192",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	adapterOnPrice, err := ModelPriceHelper(ctx, adapterOn, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, adapterOn.BillingModelName)
	assert.Equal(t, "gemini-2.5-flash-thinking-8192", adapterOn.GetBillingModelName())
	assert.Equal(t, 0.075, adapterOnPrice.ModelRatio)

	base := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	basePrice, err := ModelPriceHelper(ctx, base, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, base.BillingModelName)
	assert.Equal(t, "gemini-2.5-flash", base.GetBillingModelName())
	assert.Equal(t, 0.15, basePrice.ModelRatio)
}

func TestModelPriceHelperHonorsCustomClaudeThinkingAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["claude-3-7-sonnet"] = 1.5
	ratios["claude-3-7-sonnet-thinking"] = 3.0
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	claudeSettings := model_setting.GetClaudeSettings()
	oldThinking := claudeSettings.ThinkingAdapterEnabled
	claudeSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { claudeSettings.ThinkingAdapterEnabled = oldThinking })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-3-7-sonnet-thinking",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "claude-3-7-sonnet-thinking", info.GetBillingModelName())
	assert.Equal(t, 3.0, priceData.ModelRatio)
}

func TestModelPriceHelperCanonicalBillingLadder(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")

	t.Run("level2 full form", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		delete(ratios, "qwen3-max")
		ratios["qwen3-max@effort:high@thinking:on"] = 4.0
		ratios["qwen3-max@thinking:on"] = 3.0
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@thinking:on@effort:high@temperature:0.2",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max@effort:high@thinking:on", info.BillingModelName)
		assert.Equal(t, 4.0, priceData.ModelRatio)
	})

	t.Run("level3 thinking form shuffled budget", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		delete(ratios, "qwen3-max")
		delete(ratios, "qwen3-max@effort:high@thinking:on")
		ratios["qwen3-max@thinking:on"] = 3.0
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@temperature:0.3@thinking:8192",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max@thinking:on", info.BillingModelName)
		assert.Equal(t, 3.0, priceData.ModelRatio)
	})

	t.Run("level4 base fallback", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		delete(ratios, "qwen3-max@thinking:on")
		delete(ratios, "qwen3-max@effort:high@thinking:on")
		ratios["qwen3-max"] = 1.25
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@thinking:off",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max", info.BillingModelName)
		assert.Equal(t, 1.25, priceData.ModelRatio)
	})

	t.Run("thinking minus one bills as on", func(t *testing.T) {
		ratios := ratio_setting.GetModelRatioCopy()
		ratios["qwen3-max@thinking:on"] = 3.0
		ratioJSON, err := common.Marshal(ratios)
		require.NoError(t, err)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

		info := &relaycommon.RelayInfo{
			OriginModelName: "qwen3-max@thinking:-1",
			UserGroup:       "default",
			UsingGroup:      "default",
		}
		priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-max@thinking:on", info.BillingModelName)
		assert.Equal(t, 3.0, priceData.ModelRatio)
	})
}

func TestModelPriceHelperMigratesLegacyGeminiWildcardToCanonical(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	delete(ratios, "gemini-2.5-flash-thinking-*")
	ratios["gemini-2.5-flash"] = 0.15
	ratios["gemini-2.5-flash@thinking:on"] = 0.09
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	geminiSettings := model_setting.GetGeminiSettings()
	oldThinking := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = oldThinking })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-2.5-flash-thinking-8192",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Equal(t, "gemini-2.5-flash@thinking:on", info.BillingModelName)
	assert.Equal(t, 0.09, priceData.ModelRatio)
}

func TestModelPriceHelperModifierNameFallsBackToBase(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["qwen3.8-max"] = 2.0
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "qwen3.8-max@thinking:on@temperature:0.2",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Equal(t, "qwen3.8-max", info.BillingModelName)
	assert.Equal(t, 2.0, priceData.ModelRatio)
}

func TestModelPriceHelperExemptAtNameBillsVerbatim(t *testing.T) {
	gin.SetMode(gin.TestMode)

	settings := model_setting.GetGlobalSettings()
	originalBlacklist := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = originalBlacklist })
	settings.ThinkingModelBlacklist = append(originalBlacklist, "re:.*@sha256:.*")

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["opaque"] = 1.0
	ratios["opaque@sha256:deadbeef"] = 7.0
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "opaque@sha256:deadbeef",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "opaque@sha256:deadbeef", info.GetBillingModelName())
	assert.Equal(t, 7.0, priceData.ModelRatio)
}

func TestModelPriceHelperPreservesGpt51CodexMaxIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["gpt-5.1-codex-max"] = 1.75
	ratios["gpt-5.1-codex"] = 9.9
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = false
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-5.1-codex-max",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "gpt-5.1-codex-max", info.GetBillingModelName())
	assert.Equal(t, 1.75, priceData.ModelRatio)
}

func TestModelPriceHelperNativeGeminiNoThinkingDoesNotAliasBillingModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
	})
	ratios := ratio_setting.GetModelRatioCopy()
	ratios["gemini-3-pro"] = 1.25
	ratioJSON, err := common.Marshal(ratios)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(ratioJSON)))

	oldSelfUse := operation_setting.SelfUseModeEnabled
	operation_setting.SelfUseModeEnabled = true
	t.Cleanup(func() { operation_setting.SelfUseModeEnabled = oldSelfUse })

	geminiSettings := model_setting.GetGeminiSettings()
	oldThinking := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = oldThinking })

	budget := 0
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gemini-3-pro",
		UserGroup:       "default",
		UsingGroup:      "default",
		Request: &dto.GeminiChatRequest{
			GenerationConfig: dto.GeminiChatGenerationConfig{
				ThinkingConfig: &dto.GeminiThinkingConfig{
					ThinkingBudget: &budget,
				},
			},
		},
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Empty(t, info.BillingModelName)
	assert.Equal(t, "gemini-3-pro", info.GetBillingModelName())
	assert.Equal(t, 1.25, priceData.ModelRatio)
	assert.NotEqual(t, 37.5, priceData.ModelRatio)
}

func TestInputPreConsumeMultiplierLegacyAndRequestPrices(t *testing.T) {
	previous := config.GlobalConfig.ExportAllConfigs()
	previousRatios, previousPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(previous))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices))
	})
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"legacy-input-policy":1.5}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"request-input-policy":0.01}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"fixed-input-policy":"tiered_expr","image-input-policy":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"fixed-input-policy":"tier(\"request\", fixed(0.01))","image-input-policy":"tier(\"image\", p * 3) * image_count"}`,
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))
	for _, tc := range []struct {
		model        string
		multiplier   float64
		prompt, want int
	}{
		{"legacy-input-policy", 0.5, 100, 75},
		{"legacy-input-policy", 2.5, 100, 375},
		{"legacy-input-policy", 1, 0, 0},
		{"request-input-policy", 2.5, 100, 5000},
		{"fixed-input-policy", 2.5, 100, 5000},
		{"image-input-policy", 2.5, 100, 375},
	} {
		t.Run(tc.model+"/"+fmt.Sprint(tc.multiplier, "/", tc.prompt), func(t *testing.T) {
			operation_setting.GetQuotaSetting().PreConsumeMultiplier = tc.multiplier
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{OriginModelName: tc.model, UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{}}
			price, err := ModelPriceHelper(ctx, info, tc.prompt, &types.TokenCountMeta{MaxTokens: 10000})
			require.NoError(t, err)
			assert.Equal(t, tc.want, price.QuotaToPreConsume)
			if tc.model == "image-input-policy" {
				reservation := &priceTestReservation{held: price.QuotaToPreConsume}
				info.Billing = reservation
				operation_setting.GetQuotaSetting().PreConsumeMultiplier = 10
				require.Nil(t, service.PrepareImageBillingForRequest(ctx, info, 2))
				assert.Equal(t, 750, reservation.held, "retry must reuse the original fractional multiplier")
			}
		})
	}
}

// priceTestReservation observes the reservation requested before image submission.
type priceTestReservation struct{ held int }

func (s *priceTestReservation) Settle(int) error         { return nil }
func (s *priceTestReservation) Refund(*gin.Context)      {}
func (s *priceTestReservation) NeedsRefund() bool        { return false }
func (s *priceTestReservation) GetPreConsumedQuota() int { return s.held }
func (s *priceTestReservation) Reserve(quota int) error  { s.held = max(s.held, quota); return nil }
