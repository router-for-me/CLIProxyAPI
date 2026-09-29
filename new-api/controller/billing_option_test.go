package controller

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateOptionRejectsInvalidTaskBillingExpressions(t *testing.T) {
	const pluginKey = "billing-save-probe"
	const modelName = "billing-save-model"
	source := `
export const meta = {
  apiVersion: 1, key: "billing-save-probe", name: "Billing Save Probe", version: "1.0.0", author: {name: "Test"},
  models: ["billing-save-model"], fetchMode: "per_task",
  usageSchema: {seconds: {type: "number", unit: "second"}}
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(pluginKey) })

	tests := []struct {
		name       string
		expression string
		errorText  string
	}{
		{
			name:       "invalid syntax",
			expression: `tier("base",`,
			errorText:  "expr compile error",
		},
		{
			name:       "undeclared usage key",
			expression: `tier("base", u("clips") * 0.1)`,
			errorText:  `usage key \"clips\" is not declared`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			expressions, marshalErr := common.Marshal(map[string]string{modelName: testCase.expression})
			require.NoError(t, marshalErr)
			body, marshalErr := common.Marshal(OptionUpdateRequest{
				Key:   "billing_setting.billing_expr",
				Value: string(expressions),
			})
			require.NoError(t, marshalErr)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(body)))

			UpdateOption(context)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), modelName)
			assert.Contains(t, recorder.Body.String(), testCase.errorText)
		})
	}
}

func TestUpdateOptionRejectsUsageExpressionWithoutTaskPlugin(t *testing.T) {
	const modelName = "billing-save-model-without-plugin"
	expressions, err := common.Marshal(map[string]string{
		modelName: `u("mode") == "std" ? 1 : 2`,
	})
	require.NoError(t, err)
	body, err := common.Marshal(OptionUpdateRequest{
		Key:   "billing_setting.billing_expr",
		Value: string(expressions),
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/",
		strings.NewReader(string(body)),
	)

	UpdateOption(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	assert.Contains(t, recorder.Body.String(), modelName)
	assert.Contains(t, recorder.Body.String(), "mode")
	assert.Contains(t, recorder.Body.String(), "no task plugin usage schema")
}

func TestUpdateOptionAliasBillingExprUsesPluginSchema(t *testing.T) {
	database := modelManagementDB(t, "sqlite", "")
	require.NoError(t, database.AutoMigrate(&model.Log{}))
	const pluginKey = "billing-alias-probe"
	source := `
export const meta = {
  apiVersion: 1, key: "billing-alias-probe", name: "Billing Alias Probe", version: "1.0.0", author: {name: "Test"},
  models: ["declared-model"], fetchMode: "per_task",
  usageSchema: {seconds: {type: "number", unit: "second"}, image_count: {type: "number", unit: "count"}},
  usageProfiles: [{models: ["declared-model"], schema: {seconds: {type: "number", unit: "second"}}}]
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(pluginKey) })

	mapping := `{"alias-model":"declared-model"}`
	require.NoError(t, model.DB.Create(&model.Channel{
		Id:           1,
		Type:         54,
		Key:          "key-1",
		Status:       common.ChannelStatusEnabled,
		Name:         "ch-1",
		Group:        "default",
		Models:       "alias-model,declared-model",
		ModelMapping: &mapping,
	}).Error)
	model.InitChannelCache()

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	putExpr := func(modelName, expression string) *httptest.ResponseRecorder {
		t.Helper()
		expressions, marshalErr := common.Marshal(map[string]string{modelName: expression})
		require.NoError(t, marshalErr)
		body, marshalErr := common.Marshal(OptionUpdateRequest{
			Key:   "billing_setting.billing_expr",
			Value: string(expressions),
		})
		require.NoError(t, marshalErr)
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(body)))
		UpdateOption(context)
		return recorder
	}

	accepted := putExpr("alias-model", `u("seconds")`)
	assert.Equal(t, http.StatusOK, accepted.Code)
	assert.Contains(t, accepted.Body.String(), `"success":true`)

	rejectedKey := putExpr("alias-model", `u("image_count")`)
	assert.Equal(t, http.StatusOK, rejectedKey.Code)
	assert.Contains(t, rejectedKey.Body.String(), `"success":false`)
	assert.Contains(t, rejectedKey.Body.String(), `usage key \"image_count\" is not declared`)

	rejectedDeclared := putExpr("declared-model", `u("image_count")`)
	assert.Contains(t, rejectedDeclared.Body.String(), `"success":false`)
	assert.Contains(t, rejectedDeclared.Body.String(), `usage key \"image_count\" is not declared`)

	unresolvable := putExpr("unknown-alias-model", `u("seconds")`)
	assert.Equal(t, http.StatusOK, unresolvable.Code)
	assert.Contains(t, unresolvable.Body.String(), `"success":false`)
	assert.Contains(t, unresolvable.Body.String(), "no task plugin usage schema")
}

// Covers configuration persistence, validation, reservation and reconciliation
// through the same public entry points used by the relay.
func TestPreConsumePolicyDatabaseMatrix(t *testing.T) {
	previousConfig := config.GlobalConfig.ExportAllConfigs()
	previousUnit, previousBatch := common.QuotaPerUnit, common.BatchUpdateEnabled
	common.QuotaPerUnit, common.BatchUpdateEnabled = 500000, false
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(previousConfig))
		common.QuotaPerUnit, common.BatchUpdateEnabled = previousUnit, previousBatch
	})
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.Token{}))
			for key, value := range map[string]string{
				"quota_setting.trust_quota_usd":        "10.5",
				"quota_setting.pre_consume_multiplier": "1.5",
			} {
				var response struct{ Success bool }
				modelManagementRequest(t, UpdateOption, http.MethodPut, "/api/option/", OptionUpdateRequest{Key: key, Value: value}, &response)
				require.True(t, response.Success)
				var saved model.Option
				require.NoError(t, db.Where(&model.Option{Key: key}).First(&saved).Error)
				assert.Equal(t, value, saved.Value)
			}
			var savedOptions []model.Option
			require.NoError(t, db.Find(&savedOptions).Error)
			stored := map[string]string{}
			for _, option := range savedOptions {
				stored[option.Key] = option.Value
			}
			for range 2 {
				operation_setting.GetQuotaSetting().TrustQuotaUSD = 10
				operation_setting.GetQuotaSetting().PreConsumeMultiplier = 1
				require.NoError(t, config.GlobalConfig.LoadFromDB(stored))
				assert.Equal(t, 10.5, operation_setting.GetQuotaSetting().TrustQuotaUSD)
				assert.Equal(t, 1.5, operation_setting.GetQuotaSetting().PreConsumeMultiplier)
			}
			for _, tc := range []struct{ key, value string }{
				{"trust_quota_usd", "-1"}, {"trust_quota_usd", "NaN"}, {"trust_quota_usd", "+Inf"},
				{"pre_consume_multiplier", "0"}, {"pre_consume_multiplier", "-0.5"},
				{"pre_consume_multiplier", "NaN"}, {"pre_consume_multiplier", "Inf"},
				{"pre_consume_multiplier", "1e309"}, {"pre_consume_multiplier", ""},
			} {
				key := "quota_setting." + tc.key
				var response struct{ Success bool }
				modelManagementRequest(t, UpdateOption, http.MethodPut, "/api/option/", OptionUpdateRequest{Key: key, Value: tc.value}, &response)
				assert.False(t, response.Success, "%s=%q", key, tc.value)
				var saved model.Option
				require.NoError(t, db.Where(&model.Option{Key: key}).First(&saved).Error)
				assert.Equal(t, stored[key], saved.Value, "invalid saves must preserve the previous value")
			}
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
				"billing_setting.billing_mode":    `{"policy-model":"tiered_expr"}`,
				"billing_setting.billing_expr":    `{"policy-model":"tier(\"base\", p * 3 + c * 15)"}`,
				"group_ratio_setting.group_ratio": `{"default":1}`,
			}))
			cases := []struct {
				name                  string
				threshold, multiplier float64
				wallet, token         int
				force, unlimited      bool
				wantHeld              int
			}{
				{"above threshold", 10, 1, 5500000, 5500000, false, false, 0},
				{"custom threshold", 20, 1, 5500000, 5500000, false, false, 1500},
				{"zero disables bypass", 0, 1, 5500000, 5500000, false, false, 1500},
				{"wallet equals threshold", 10, 1, 5000000, 5500000, false, false, 1500},
				{"token equals threshold", 10, 1, 5500000, 5000000, false, false, 1500},
				{"fractional threshold", 10.5, 1, 5250001, 5250001, false, false, 0},
				{"unlimited token", 10, 1, 5500000, 0, false, true, 0},
				{"forced reservation", 10, 1, 5500000, 5500000, true, false, 1500},
				{"half input cost", 0, 0.5, 100000, 100000, false, false, 750},
				{"fractional multiple refunds excess", 0, 2.5, 100000, 100000, false, false, 3750},
			}
			for i, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					require.NoError(t, model.UpdateOption("quota_setting.trust_quota_usd", strconv.FormatFloat(tc.threshold, 'f', -1, 64)))
					require.NoError(t, model.UpdateOption("quota_setting.pre_consume_multiplier", strconv.FormatFloat(tc.multiplier, 'f', -1, 64)))
					user := model.User{Username: fmt.Sprintf("policy-user-%d", i), Quota: tc.wallet, Group: "default", AffCode: fmt.Sprintf("policy-aff-%d", i)}
					require.NoError(t, db.Create(&user).Error)
					token := model.Token{UserId: user.Id, Key: fmt.Sprintf("policy-token-%d", i), RemainQuota: tc.token, UnlimitedQuota: tc.unlimited}
					require.NoError(t, db.Create(&token).Error)
					ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
					ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
					ctx.Set("token_quota", tc.token)
					info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, TokenKey: token.Key, TokenUnlimited: tc.unlimited, ForcePreConsume: tc.force, OriginModelName: "policy-model", UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{}}
					info.UserSetting.BillingPreference = "wallet_only"
					price, err := helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 10000})
					require.NoError(t, err)
					require.Nil(t, service.PreConsumeBilling(ctx, price.QuotaToPreConsume, info))
					assert.Equal(t, tc.wantHeld, info.FinalPreConsumedQuota)
					require.NoError(t, db.First(&user, user.Id).Error)
					assert.Equal(t, tc.wallet-tc.wantHeld, user.Quota)
					if !tc.unlimited {
						require.NoError(t, db.First(&token, token.Id).Error)
						assert.Equal(t, tc.token-tc.wantHeld, token.RemainQuota)
					}
					_, actual, _ := service.TryTieredSettle(info, billingexpr.TokenParams{P: 1000, C: 100, Len: 1000})
					assert.Equal(t, 2250, actual)
					require.NoError(t, info.Billing.Settle(actual))
					require.NoError(t, info.Billing.Settle(actual))
					require.NoError(t, db.First(&user, user.Id).Error)
					assert.Equal(t, tc.wallet-actual, user.Quota)
					if !tc.unlimited {
						require.NoError(t, db.First(&token, token.Id).Error)
						assert.Equal(t, tc.token-actual, token.RemainQuota)
					}
				})
			}
		})
	}
}

func TestPreConsumeMultiplierRejectsInvalidRuntimeAndOverflow(t *testing.T) {
	previous := config.GlobalConfig.ExportAllConfigs()
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(previous)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"policy-overflow":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"policy-overflow":"tier(\"base\", p * 3)"}`,
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))
	for _, multiplier := range []float64{0, -0.5, math.NaN(), math.Inf(1), math.MaxFloat64} {
		operation_setting.GetQuotaSetting().PreConsumeMultiplier = multiplier
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		info := &relaycommon.RelayInfo{OriginModelName: "policy-overflow", UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{}}
		_, err := helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
		require.Error(t, err)
		assert.Nil(t, info.Billing)
	}
}
