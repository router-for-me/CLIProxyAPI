package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	appdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// normalizeResponsesWSTestMessage runs the read-loop envelope parse followed by
// request normalization, exactly as the session does for one response.create.
func normalizeResponsesWSTestMessage(message []byte) (responsesWSCreateRequest, string, error) {
	envelope, streamID, err := parseResponsesWSEnvelope(message)
	if err != nil {
		return responsesWSCreateRequest{StreamID: streamID}, envelope.EventID, err
	}
	create, err := normalizeResponsesWSCreateEvent(message, envelope, streamID)
	return create, envelope.EventID, err
}

func TestNormalizeResponsesWSMaxOutputTokens(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{value: "0", valid: true},
		{value: "1073741823", valid: true},
		{value: "1073741824"},
		{value: "18446744073686646784"},
		{value: "-1"},
	} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrapped=%t", tc.value, wrapped), func(t *testing.T) {
				fields := `"model":"gpt-5.1","input":"hi","max_output_tokens":` + tc.value
				payload := `{"type":"response.create",` + fields + `}`
				if wrapped {
					payload = `{"type":"response.create","response":{` + fields + `}}`
				}
				create, _, err := normalizeResponsesWSTestMessage([]byte(payload))
				if !tc.valid {
					require.Error(t, err)
					assert.Equal(t, http.StatusBadRequest, newResponsesWSInvalidRequestError(err).StatusCode)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, create.Request.MaxOutputTokens)
				assert.Equal(t, tc.value, fmt.Sprint(*create.Request.MaxOutputTokens))
			})
		}
	}
}

func TestSelectResponsesWSChannelHonorsPinsAndFilters(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	enabled := &model.Channel{Name: "enabled", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI}
	enabled.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	wsDisabled := &model.Channel{Name: "ws-disabled", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI}
	disabled := &model.Channel{Name: "disabled", Key: "sk-test", Status: common.ChannelStatusManuallyDisabled, Type: constant.ChannelTypeOpenAI}
	filtered := &model.Channel{Name: "filtered", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeAdvancedCustom}
	for _, channel := range []*model.Channel{enabled, disabled, filtered, wsDisabled} {
		require.NoError(t, database.Create(channel).Error)
	}
	for _, tc := range []struct {
		name      string
		channelID int
		status    int
	}{
		{name: "token pin overrides origin pin", channelID: enabled.Id},
		{name: "disabled pin rejects", channelID: disabled.Id, status: http.StatusForbidden},
		{name: "pin cannot bypass websocket switch", channelID: wsDisabled.Id, status: http.StatusBadRequest},
		{name: "pin cannot bypass path filter", channelID: filtered.Id, status: http.StatusBadRequest},
		{name: "missing pin rejects", channelID: 99999, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			constraints := service.GetChannelConstraints(c)
			constraints.AddPin(appdto.ChannelPin{ChannelId: disabled.Id, Source: appdto.PinSourceOriginTask, Rank: appdto.PinRankOriginTask, RetryMode: appdto.PinRetrySameChannel})
			constraints.AddPin(appdto.ChannelPin{ChannelId: tc.channelID, Source: appdto.PinSourceToken, Rank: appdto.PinRankToken, RetryMode: appdto.PinRetrySingleAttempt})
			constraints.AddFilter(appdto.ChannelFilter{Kind: appdto.FilterRequestPath, RequestPath: c.Request.URL.Path})
			channel, apiErr := selectResponsesWSChannel(c, "gpt-5.1", &service.RetryParam{Ctx: c, ModelName: "gpt-5.1", TokenGroup: "default"})
			if tc.status != 0 {
				require.NotNil(t, apiErr)
				assert.Equal(t, tc.status, apiErr.StatusCode)
				assert.Nil(t, channel)
				assert.False(t, service.ShouldRetryRelayError(c, apiErr, 2))
				return
			}
			require.Nil(t, apiErr)
			require.NotNil(t, channel)
			assert.Equal(t, enabled.Id, channel.Id)
			assert.Equal(t, enabled.Id, common.GetContextKeyInt(c, constant.ContextKeyChannelId))
			assert.False(t, service.ShouldRetryRelayError(c, types.NewErrorWithStatusCode(errors.New("upstream failed"), types.ErrorCodeDoRequestFailed, 503), 2))
		})
	}
}

// The WebSocket relay must admit the same model names as the HTTP distributor
// when a token restricts models (reasoning suffixes and @modifiers included).
func TestCheckResponsesWSModelAccessMatchesHTTPTokenLimits(t *testing.T) {
	for _, tc := range []struct {
		model  string
		status int
	}{
		{model: "gpt-5.1"},
		{model: "gpt-5.1-high"},
		{model: "gpt-5.1@thinking:on"},
		{model: "gpt-4o", status: http.StatusForbidden},
	} {
		t.Run(tc.model, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			common.SetContextKey(c, constant.ContextKeyTokenModelLimitEnabled, true)
			common.SetContextKey(c, constant.ContextKeyTokenModelLimit, map[string]bool{"gpt-5.1": true})
			apiErr := checkResponsesWSModelAccess(c, tc.model)
			if tc.status == 0 {
				assert.Nil(t, apiErr)
				return
			}
			require.NotNil(t, apiErr)
			assert.Equal(t, tc.status, apiErr.StatusCode)
			assert.False(t, service.ShouldRetryRelayError(c, apiErr, 2))
		})
	}
}

func TestSelectResponsesWSChannelAcceptsNativeResponsesChannelTypes(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	nativeRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses"}}}
	noneRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses", Converter: "none"}}}
	convertedRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/chat/completions", Converter: "openai_responses_to_openai_chat_completions"}}}
	chatRoute := &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/v1/chat/completions"}}}
	for _, tc := range []struct {
		name        string
		channelType int
		advanced    *dto.AdvancedCustomConfig
		rejectedBy  appdto.ChannelFilterKind
	}{
		{name: "new api", channelType: constant.ChannelTypeNewAPI},
		{name: "sub2api", channelType: constant.ChannelTypeSub2API},
		{name: "advanced custom native responses route", channelType: constant.ChannelTypeAdvancedCustom, advanced: nativeRoute},
		{name: "advanced custom explicit none converter", channelType: constant.ChannelTypeAdvancedCustom, advanced: noneRoute},
		{name: "advanced custom converter route", channelType: constant.ChannelTypeAdvancedCustom, advanced: convertedRoute, rejectedBy: appdto.FilterResponsesWebSocket},
		{name: "advanced custom without responses route", channelType: constant.ChannelTypeAdvancedCustom, advanced: chatRoute, rejectedBy: appdto.FilterRequestPath},
		{name: "anthropic", channelType: constant.ChannelTypeAnthropic, rejectedBy: appdto.FilterResponsesWebSocket},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &model.Channel{Name: tc.name, Key: "sk-test", Status: common.ChannelStatusEnabled, Type: tc.channelType}
			channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
			channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: tc.advanced})
			require.NoError(t, database.Create(channel).Error)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			constraints := service.GetChannelConstraints(c)
			constraints.AddPin(appdto.ChannelPin{ChannelId: channel.Id, Source: appdto.PinSourceToken, Rank: appdto.PinRankToken, RetryMode: appdto.PinRetrySingleAttempt})
			constraints.AddFilter(appdto.ChannelFilter{Kind: appdto.FilterRequestPath, RequestPath: c.Request.URL.Path})
			selected, apiErr := selectResponsesWSChannel(c, "ws-model", &service.RetryParam{Ctx: c, ModelName: "ws-model", TokenGroup: "default"})
			if tc.rejectedBy != "" {
				require.NotNil(t, apiErr)
				assert.Nil(t, selected)
				assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
				assert.Equal(t, types.ErrorCode(tc.rejectedBy), apiErr.GetErrorCode())
				return
			}
			require.Nil(t, apiErr)
			require.NotNil(t, selected)
			assert.Equal(t, channel.Id, selected.Id)
		})
	}
}

func TestRestoreConnectionContextRejectsChangedAdvancedCustomRoute(t *testing.T) {
	database := setupRelayChannelDB(t)
	baseURL := "http://upstream.example"
	channel := &model.Channel{Name: "advanced", Key: "sk-test", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeAdvancedCustom, BaseURL: &baseURL}
	channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses"}}}})
	require.NoError(t, database.Create(channel).Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	service.GetChannelConstraints(c).AddPin(appdto.ChannelPin{ChannelId: channel.Id, Source: appdto.PinSourceToken, Rank: appdto.PinRankToken, RetryMode: appdto.PinRetrySingleAttempt})
	route, ok := channel.GetOtherSettings().AdvancedCustom.MatchPathForModel("/v1/responses", "ws-model")
	require.True(t, ok)
	session := &responsesWSSession{lockedChannelID: channel.Id, lockedModel: "ws-model", lockedKey: "sk-test", lockedRoute: route,
		lockedContext: map[constant.ContextKey]any{constant.ContextKeyChannelBaseUrl: channel.GetBaseURL(), constant.ContextKeyChannelHeaderOverride: channel.GetHeaderOverride()}}
	require.Nil(t, session.restoreConnectionContext(c, "ws-model"))

	// Request-level edits (model list, explicit none converter) keep the connection.
	channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v1/responses", Converter: "none", Models: []string{"ws-model", "other-model"}}}}})
	require.NoError(t, database.Model(channel).Update("settings", channel.OtherSettings).Error)
	require.Nil(t, session.restoreConnectionContext(c, "ws-model"))

	channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/responses", UpstreamPath: "/v2/responses"}}}})
	require.NoError(t, database.Model(channel).Update("settings", channel.OtherSettings).Error)
	apiErr := session.restoreConnectionContext(c, "ws-model")
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.ErrorContains(t, apiErr, "upstream route changed")
}

func TestResponsesWSChannelRoutingRequiresExplicitOptIn(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.Ability{}))
	legacy := &model.Channel{Name: "legacy-http", Key: "sk-test", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(10))}
	enabled := &model.Channel{Name: "websocket", Key: "sk-test", Type: constant.ChannelTypeCodex, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(0))}
	unsupported := &model.Channel{Name: "unsupported", Key: "sk-test", Type: constant.ChannelTypeAnthropic, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(5))}
	enabled.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	unsupported.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	for _, channel := range []*model.Channel{legacy, enabled, unsupported} {
		require.NoError(t, database.Create(channel).Error)
		require.NoError(t, database.Create(&model.Ability{ChannelId: channel.Id, Model: "ws-model", Group: "default", Enabled: true, Priority: channel.Priority}).Error)
	}
	previousCache := common.MemoryCacheEnabled
	t.Cleanup(func() {
		defer func() { common.MemoryCacheEnabled = previousCache }()
		ids := []int{legacy.Id, enabled.Id, unsupported.Id}
		require.NoError(t, database.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error)
		require.NoError(t, database.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
		model.InitChannelCache()
	})
	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	params := &service.RetryParam{Ctx: c, ModelName: "ws-model", TokenGroup: "default"}
	channel, apiErr := selectResponsesWSChannel(c, "ws-model", params)
	require.Nil(t, apiErr)
	require.NotNil(t, channel)
	assert.Equal(t, enabled.Id, channel.Id)
	httpChannel, err := model.GetRandomSatisfiedChannel("default", "ws-model", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, httpChannel)
	assert.Equal(t, legacy.Id, httpChannel.Id)

	// Disabling the saved setting takes effect for the next create on an existing session.
	enabled.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: false})
	require.NoError(t, database.Model(enabled).Update("setting", enabled.Setting).Error)
	model.InitChannelCache()
	session := &responsesWSSession{lockedChannelID: enabled.Id, lockedModel: "ws-model"}
	apiErr = session.restoreConnectionContext(c, "ws-model")
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	channel, apiErr = selectResponsesWSChannel(c, "ws-model", params)
	require.NotNil(t, apiErr)
	assert.Nil(t, channel)
}

// Session affinity follows the HTTP distributor: a strict binding to an
// unusable channel fails the request, a prefer binding falls back, and an
// unusable binding is dropped from the cache either way.
func TestSelectResponsesWSChannelHonorsStrictSessionBinding(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := setupRelayChannelDB(t)
	require.NoError(t, database.AutoMigrate(&model.Ability{}))
	bound := &model.Channel{Name: "bound", Key: "sk-test", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusAutoDisabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(10))}
	fallback := &model.Channel{Name: "fallback", Key: "sk-test", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Group: "default", Models: "ws-model", Priority: common.GetPointer(int64(0))}
	for _, channel := range []*model.Channel{bound, fallback} {
		channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
		require.NoError(t, database.Create(channel).Error)
		require.NoError(t, database.Create(&model.Ability{ChannelId: channel.Id, Model: "ws-model", Group: "default", Enabled: channel.Status == common.ChannelStatusEnabled, Priority: channel.Priority}).Error)
	}
	previousCache := common.MemoryCacheEnabled
	t.Cleanup(func() {
		defer func() { common.MemoryCacheEnabled = previousCache }()
		ids := []int{bound.Id, fallback.Id}
		require.NoError(t, database.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error)
		require.NoError(t, database.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
		model.InitChannelCache()
	})
	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	affinity := operation_setting.GetChannelAffinitySetting()
	previousAffinity := *affinity
	t.Cleanup(func() { *affinity = previousAffinity })
	snapshot, err := model.BuildRequestPolicy(map[string]string{
		"channel_affinity_setting.enabled":      "true",
		"channel_affinity_setting.session_mode": "strict",
		"channel_affinity_setting.rules":        `[{"name":"session","model_regex":[".*"],"key_sources":[{"type":"request_header","key":"X-Session"}],"session_mode":"inherit"}]`,
	})
	require.NoError(t, err)
	*affinity = snapshot.Affinity
	newSessionContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		c.Request.Header.Set("X-Session", t.Name())
		return c
	}
	seed := newSessionContext()
	_, found := service.GetPreferredChannelByAffinity(seed, "ws-model", "default")
	require.False(t, found)
	seed.Set("channel_id", bound.Id)
	service.RecordChannelAffinity(seed, bound.Id)
	t.Cleanup(func() { service.ClearCurrentChannelAffinityCache(seed) })

	strict := newSessionContext()
	channel, apiErr := selectResponsesWSChannel(strict, "ws-model", &service.RetryParam{Ctx: strict, ModelName: "ws-model", TokenGroup: "default", Retry: common.GetPointer(0)})
	require.NotNil(t, apiErr)
	assert.Nil(t, channel)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	assert.False(t, service.ShouldRetryRelayError(strict, apiErr, 2))
	_, found = service.GetPreferredChannelByAffinity(seed, "ws-model", "default")
	assert.False(t, found, "an unusable binding is cleared unless keep_on_channel_disabled is set")

	affinity.SessionMode = "prefer"
	service.RecordChannelAffinity(seed, bound.Id)
	prefer := newSessionContext()
	channel, apiErr = selectResponsesWSChannel(prefer, "ws-model", &service.RetryParam{Ctx: prefer, ModelName: "ws-model", TokenGroup: "default", Retry: common.GetPointer(0)})
	require.Nil(t, apiErr)
	require.NotNil(t, channel)
	assert.Equal(t, fallback.Id, channel.Id)
}

func TestNormalizeResponsesWSCreateEventWrapper(t *testing.T) {
	message := []byte(`{
		"type": "response.create",
		"event_id": "evt_1",
		"generate": false,
		"response": {
			"model": "gpt-5.3-codex-spark",
			"input": "hi",
			"store": false,
			"stream": true,
			"stream_options": {"include_usage": true}
		}
	}`)

	create, eventID, err := normalizeResponsesWSTestMessage(message)
	if err != nil {
		t.Fatalf("normalizeResponsesWSTestMessage() error = %v", err)
	}
	req := create.Request
	if eventID != "evt_1" {
		t.Fatalf("eventID = %q, want evt_1", eventID)
	}
	if req.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("model = %q", req.Model)
	}
	if strings.TrimSpace(string(create.Generate)) != "false" {
		t.Fatalf("generate = %s, want false", create.Generate)
	}
	if req.Stream != nil {
		t.Fatalf("stream = %v, want nil", req.Stream)
	}
	if req.StreamOptions != nil {
		t.Fatalf("stream_options = %#v, want nil", req.StreamOptions)
	}
	if strings.TrimSpace(string(req.Store)) != "false" {
		t.Fatalf("store = %s, want false", req.Store)
	}
}

func TestNormalizeResponsesWSCreateEventFlat(t *testing.T) {
	message := []byte(`{
		"type": "response.create",
		"event_id": "evt_2",
		"model": "gpt-5.3-codex-spark",
		"input": "hi",
		"generate": false,
		"stream": true,
		"background": true,
		"stream_options": {"include_usage": true}
	}`)

	create, eventID, err := normalizeResponsesWSTestMessage(message)
	if err != nil {
		t.Fatalf("normalizeResponsesWSTestMessage() error = %v", err)
	}
	req := create.Request
	if eventID != "evt_2" {
		t.Fatalf("eventID = %q, want evt_2", eventID)
	}
	if req.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("model = %q", req.Model)
	}
	if strings.TrimSpace(string(create.Generate)) != "false" {
		t.Fatalf("generate = %s, want false", create.Generate)
	}
	if req.Stream != nil {
		t.Fatalf("stream = %v, want nil", req.Stream)
	}
	if req.StreamOptions != nil {
		t.Fatalf("stream_options = %#v, want nil", req.StreamOptions)
	}
}

func TestBuildResponsesWSCreateEventIsFlat(t *testing.T) {
	payload := []byte(`{
		"model": "gpt-5.3-codex-spark",
		"input": "hi",
		"store": false,
		"event_id": "evt_upstream",
		"stream": true,
		"background": true,
		"stream_options": {"include_usage": true}
	}`)

	got, err := buildResponsesWSCreateEvent(payload, common.RawMessage(`false`), "")
	if err != nil {
		t.Fatalf("buildResponsesWSCreateEvent() error = %v", err)
	}
	var data map[string]any
	if err := common.Unmarshal(got, &data); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if data["type"] != responsesWSEventTypeResponseCreate {
		t.Fatalf("type = %#v", data["type"])
	}
	if data["model"] != "gpt-5.3-codex-spark" || data["input"] != "hi" || data["store"] != false {
		t.Fatalf("unexpected flat event fields: %s", got)
	}
	if data["generate"] != false {
		t.Fatalf("generate = %#v, want false", data["generate"])
	}
	for _, key := range []string{"response", "event_id", "stream", "background", "stream_options"} {
		if _, ok := data[key]; ok {
			t.Fatalf("field %q should not be present in upstream event: %s", key, got)
		}
	}
}

func TestHTTPResponsesRequestOmitsWebSocketMetadata(t *testing.T) {
	var req dto.OpenAIResponsesRequest
	require.NoError(t, common.Unmarshal([]byte(`{"model":"gpt-5.3-codex-spark","input":"hi","generate":false,"stream_id":"planner"}`), &req))
	got, err := common.Marshal(req)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, common.Unmarshal(got, &data))
	assert.NotContains(t, data, "stream_id")
	assert.NotContains(t, data, "generate")
}

func TestBuildResponsesWSErrorPayloadIncludesStatus(t *testing.T) {
	payload, err := buildResponsesWSErrorPayload("evt_err", "", types.NewErrorWithStatusCode(
		errors.New("model is required"),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	))
	if err != nil {
		t.Fatalf("buildResponsesWSErrorPayload() error = %v", err)
	}
	var data struct {
		Type    string             `json:"type"`
		Status  int                `json:"status"`
		EventID string             `json:"event_id"`
		Error   *types.OpenAIError `json:"error"`
	}
	if err := common.Unmarshal(payload, &data); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if data.Type != "error" || data.Status != http.StatusBadRequest || data.EventID != "evt_err" {
		t.Fatalf("unexpected error event: %s", payload)
	}
	if data.Error == nil || data.Error.Code != string(types.ErrorCodeInvalidRequest) {
		t.Fatalf("unexpected error body: %#v", data.Error)
	}
}

func TestResponsesWSInvalidRequestErrorUsesBadRequestStatus(t *testing.T) {
	payload, err := buildResponsesWSErrorPayload("", "", newResponsesWSInvalidRequestError(errors.New("bad event")))
	if err != nil {
		t.Fatalf("buildResponsesWSErrorPayload() error = %v", err)
	}
	var data struct {
		Status int `json:"status"`
	}
	if err := common.Unmarshal(payload, &data); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if data.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", data.Status, http.StatusBadRequest)
	}
}

func newTestResponsesWSTarget(t *testing.T) (*websocket.Conn, func()) {
	t.Helper()
	target, _, cleanup := newTestWebSocketPair(t)
	return target, cleanup
}

func newTestWebSocketPair(t *testing.T) (*websocket.Conn, *websocket.Conn, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	serverConnCh := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		serverConnCh <- conn
	}))

	targetURL := "ws" + strings.TrimPrefix(server.URL, "http")
	target, _, err := websocket.DefaultDialer.Dial(targetURL, nil)
	if err != nil {
		server.Close()
		t.Fatalf("dial websocket: %v", err)
	}
	serverConn := <-serverConnCh
	cleanup := func() {
		_ = target.Close()
		_ = serverConn.Close()
		server.Close()
	}
	return target, serverConn, cleanup
}

func TestResponsesWSMessageSizeLimit(t *testing.T) {
	previous := constant.MaxRequestBodyMB
	constant.MaxRequestBodyMB = 1
	t.Cleanup(func() { constant.MaxRequestBodyMB = previous })
	client, server, cleanup := newTestWebSocketPair(t)
	defer cleanup()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	var admitted atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		ResponsesWebSocketHelper(c, server, func(*http.Request, string, func(*gin.Context) *types.NewAPIError) *types.NewAPIError {
			admitted.Store(true)
			return nil
		})
	}()
	_ = client.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", (1<<20)+1)))
	require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := client.ReadMessage()
	assert.True(t, websocket.IsCloseError(err, websocket.CloseMessageTooBig), "oversized message must be rejected before admission: %v", err)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("websocket request did not exit after oversized message")
	}
	assert.False(t, admitted.Load())
}

func TestResponsesWSShutdownInterruptsBusyWriter(t *testing.T) {
	client, server, cleanupClient := newTestWebSocketPair(t)
	defer cleanupClient()
	target, peer, cleanupTarget := newTestWebSocketPair(t)
	defer cleanupTarget()
	ctx, cancel := context.WithCancel(context.Background())
	s := &responsesWSSession{ctx: ctx, cancel: cancel, client: server, target: target}
	// A blocked network writer owns this lock. Closing the connection must
	// remain possible so that writer can be interrupted.
	s.targetWriteMu.Lock()
	defer s.targetWriteMu.Unlock()
	done := make(chan struct{})
	go func() { s.shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited for the network writer")
	}
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := peer.ReadMessage()
	assert.Error(t, err)
	require.NoError(t, client.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err = client.ReadMessage()
	assert.Error(t, err)
	assert.Nil(t, s.getTarget())
}

func TestResponsesWSPassthroughPreservesRawPricingParameters(t *testing.T) {
	create, _, err := normalizeResponsesWSTestMessage([]byte(`{"type":"response.create","generate":false,"response":{"model":"gpt-5.1","input":"hi","vendor":{"tier":"premium"},"stream":true}}`))
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(create.Body)))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, create.Request.Model)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: true})
	info := relaycommon.GenRelayInfoResponses(c, &create.Request)
	payload, apiErr := buildResponsesWSCreatePayload(c, info, create.Request, create.Generate, create.StreamID)
	require.Nil(t, apiErr)
	assert.JSONEq(t, `{"type":"response.create","generate":false,"model":"gpt-5.1","input":"hi","vendor":{"tier":"premium"}}`, string(payload))
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	require.NoError(t, storage.Close())
}

func TestResponsesWSStreamIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, fields, want string
		invalid            bool
	}{
		{name: "default", fields: `"model":"gpt-4o"`},
		{name: "named", fields: `"model":"gpt-4o","stream_id":"planner.A-1_2"`, want: "planner.A-1_2"},
		{name: "wrapped", fields: `"response":{"model":"gpt-4o","stream_id":"wrapped"}`, want: "wrapped"},
		{name: "top-level-wins", fields: `"stream_id":"outer","response":{"model":"gpt-4o","stream_id":"inner"}`, want: "outer"},
		{name: "maximum", fields: `"model":"gpt-4o","stream_id":"` + strings.Repeat("a", 256) + `"`, want: strings.Repeat("a", 256)},
		{name: "too-long", fields: `"stream_id":"` + strings.Repeat("a", 257) + `"`, invalid: true},
		{name: "empty", fields: `"stream_id":""`, invalid: true},
		{name: "null", fields: `"stream_id":null`, invalid: true},
		{name: "number", fields: `"stream_id":123`, invalid: true},
		{name: "unicode", fields: `"stream_id":"计划"`, invalid: true},
		{name: "spaces", fields: `"stream_id":"a b"`, invalid: true},
		{name: "invalid-top-level", fields: `"stream_id":"","response":{"stream_id":"inner"}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create, eventID, err := normalizeResponsesWSTestMessage([]byte(`{"type":"response.create","event_id":"test",` + tc.fields + `}`))
			assert.Equal(t, "test", eventID)
			if tc.invalid {
				require.ErrorContains(t, err, "stream_id")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, create.StreamID)
			assert.NotContains(t, string(create.Body), "stream_id")
			for _, passthrough := range []bool{false, true} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(create.Body)))
				common.SetContextKey(c, constant.ContextKeyOriginalModel, create.Request.Model)
				common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
				common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: passthrough})
				info := relaycommon.GenRelayInfoResponses(c, &create.Request)
				payload, apiErr := buildResponsesWSCreatePayload(c, info, create.Request, create.Generate, create.StreamID)
				require.Nil(t, apiErr)
				var event map[string]any
				require.NoError(t, common.Unmarshal(payload, &event))
				if tc.want == "" {
					assert.NotContains(t, event, "stream_id")
				} else {
					assert.Equal(t, tc.want, event["stream_id"])
				}
				if storage, err := common.GetBodyStorage(c); err == nil {
					require.NoError(t, storage.Close())
				}
			}
		})
	}
}

func TestResponsesWSErrorAttribution(t *testing.T) {
	for _, tc := range []struct {
		name, payload, control            string
		terminal, ambiguous, controlError bool
	}{
		{name: "active-server-failure", payload: `{"status":500,"error":{"type":"server_error"}}`, terminal: true},
		{name: "other-stream", payload: `{"stream_id":"other","status":500}`},
		{name: "previous-response", payload: `{"response_id":"previous","status":500}`},
		{name: "cancel-event-id", payload: `{"event_id":"cancel","status":500}`, control: `{"event_id":"cancel"}`, controlError: true},
		{name: "cancel-target", payload: `{"response_id":"missing","status":400}`, control: `{"response_id":"missing"}`, controlError: true},
		{name: "cancel-code", payload: `{"error":{"type":"invalid_request_error","code":"response_not_found"}}`, control: `{"response_id":"missing"}`, controlError: true},
		{name: "ambiguous-after-cancel", payload: `{"status":500}`, control: `{"response_id":"active"}`, terminal: true, ambiguous: true},
		{name: "explicit-generation-failure", payload: `{"response_id":"active","status":500}`, control: `{"response_id":"active"}`, terminal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var event responsesWSErrorEvent
			require.NoError(t, common.Unmarshal([]byte(tc.payload), &event))
			terminal, ambiguous, controlError := responsesWSErrorEndsRequest(event, "planner", "active", []byte(tc.control))
			assert.Equal(t, tc.terminal, terminal)
			assert.Equal(t, tc.ambiguous, ambiguous)
			assert.Equal(t, tc.controlError, controlError)
		})
	}
}
