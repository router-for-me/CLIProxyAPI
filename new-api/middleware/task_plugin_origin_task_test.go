package middleware

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	appI18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupOriginTaskDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Task{}, &model.Channel{}))
	model.DB = database
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
}

func insertOriginTaskChannel(t *testing.T, status int) *model.Channel {
	t.Helper()
	channel := &model.Channel{
		Name:   "origin-channel",
		Key:    "sk-origin",
		Status: status,
		Type:   constant.ChannelTypeDoubaoVideo,
	}
	require.NoError(t, model.DB.Create(channel).Error)
	return channel
}

func insertOriginOwnedTask(t *testing.T, taskID string, userID, channelID int, platform constant.TaskPlatform) *model.Task {
	t.Helper()
	task := &model.Task{
		TaskID:    taskID,
		UserId:    userID,
		ChannelId: channelID,
		Platform:  platform,
		Action:    "text_to_video",
		Status:    model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: "upstream-" + taskID,
		},
	}
	data, err := common.Marshal(map[string]any{"id": "upstream-" + taskID})
	require.NoError(t, err)
	task.Data = data
	require.NoError(t, model.DB.Create(task).Error)
	return task
}

func originTaskTestContext(userID int) *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/vendor/jobs", nil)
	common.SetContextKey(c, constant.ContextKeyUserId, userID)
	return c
}

func resolvedOriginPin(c *gin.Context) (int, bool) {
	pin, found, _ := service.GetChannelConstraints(c).ResolvedPin()
	if !found {
		return 0, false
	}
	return pin.ChannelId, true
}

func TestApplyOriginTaskIntent(t *testing.T) {
	setupOriginTaskDB(t)
	enabled := insertOriginTaskChannel(t, common.ChannelStatusEnabled)
	otherEnabled := insertOriginTaskChannel(t, common.ChannelStatusEnabled)
	disabled := insertOriginTaskChannel(t, common.ChannelStatusManuallyDisabled)
	insertOriginOwnedTask(t, "task-own", 7, enabled.Id, "origin-plugin")
	insertOriginOwnedTask(t, "task-own-b", 7, enabled.Id, "origin-plugin")
	insertOriginOwnedTask(t, "task-other-channel", 7, otherEnabled.Id, "origin-plugin")
	insertOriginOwnedTask(t, "task-foreign", 8, enabled.Id, "origin-plugin")
	insertOriginOwnedTask(t, "task-wrong-platform", 7, enabled.Id, "other-plugin")
	insertOriginOwnedTask(t, "task-legacy", 7, enabled.Id, constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeDoubaoVideo)))
	insertOriginOwnedTask(t, "task-disabled", 7, disabled.Id, "origin-plugin")

	tooMany := make([]any, 17)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("task-%d", i)
	}

	tests := []struct {
		name        string
		userID      int
		intent      map[string]any
		channelType int
		wantCode    string
		wantPinned  int
		wantIDs     []string
	}{
		{
			name:       "valid single origin id pins channel",
			userID:     7,
			intent:     map[string]any{"originTaskIds": []any{"task-own"}},
			wantPinned: enabled.Id,
			wantIDs:    []string{"task-own"},
		},
		{
			name:       "dedupes preserving first order",
			userID:     7,
			intent:     map[string]any{"originTaskIds": []any{"task-own", " task-own ", "task-own-b"}},
			wantPinned: enabled.Id,
			wantIDs:    []string{"task-own", "task-own-b"},
		},
		{
			name:        "legacy platform matches channelType",
			userID:      7,
			intent:      map[string]any{"originTaskIds": []any{"task-legacy"}},
			channelType: constant.ChannelTypeDoubaoVideo,
			wantPinned:  enabled.Id,
			wantIDs:     []string{"task-legacy"},
		},
		{
			name:     "unknown id",
			userID:   7,
			intent:   map[string]any{"originTaskIds": []any{"task-missing"}},
			wantCode: "origin_task_not_found",
		},
		{
			name:     "other user's task is not found",
			userID:   7,
			intent:   map[string]any{"originTaskIds": []any{"task-foreign"}},
			wantCode: "origin_task_not_found",
		},
		{
			name:     "platform mismatch",
			userID:   7,
			intent:   map[string]any{"originTaskIds": []any{"task-wrong-platform"}},
			wantCode: "origin_task_platform_mismatch",
		},
		{
			name:     "two ids on different channels",
			userID:   7,
			intent:   map[string]any{"originTaskIds": []any{"task-own", "task-other-channel"}},
			wantCode: "origin_task_channel_conflict",
		},
		{
			name:     "disabled channel",
			userID:   7,
			intent:   map[string]any{"originTaskIds": []any{"task-disabled"}},
			wantCode: "origin_task_channel_disabled",
		},
		{
			name:     "more than 16 ids",
			userID:   7,
			intent:   map[string]any{"originTaskIds": tooMany},
			wantCode: "invalid_origin_task_ids",
		},
		{
			name:     "non-array originTaskIds",
			userID:   7,
			intent:   map[string]any{"originTaskIds": "task-own"},
			wantCode: "invalid_origin_task_ids",
		},
		{
			name:     "empty string entry",
			userID:   7,
			intent:   map[string]any{"originTaskIds": []any{"task-own", "  "}},
			wantCode: "invalid_origin_task_ids",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			c := originTaskTestContext(testCase.userID)
			intentErr := applyOriginTaskIntent(c, testCase.intent, jsplugin.Meta{Key: "origin-plugin", ChannelTypes: []int{testCase.channelType}})
			if testCase.wantCode != "" {
				require.NotNil(t, intentErr)
				assert.Equal(t, testCase.wantCode, intentErr.Code)
				assert.Equal(t, http.StatusBadRequest, intentErr.StatusCode)
				_, pinned := resolvedOriginPin(c)
				assert.False(t, pinned)
				return
			}
			require.Nil(t, intentErr)
			pinnedID, ok := resolvedOriginPin(c)
			require.True(t, ok)
			assert.Equal(t, testCase.wantPinned, pinnedID)
			tasks, ok := common.GetContextKeyType[[]*model.Task](c, constant.ContextKeyOriginTasks)
			require.True(t, ok)
			require.Len(t, tasks, len(testCase.wantIDs))
			for i, wantID := range testCase.wantIDs {
				assert.Equal(t, wantID, tasks[i].TaskID)
			}
		})
	}
}

func TestApplyOriginTaskIntentAbsentAndEmptyAreNoop(t *testing.T) {
	setupOriginTaskDB(t)
	c := originTaskTestContext(7)
	require.Nil(t, applyOriginTaskIntent(c, map[string]any{}, jsplugin.Meta{Key: "origin-plugin"}))
	require.Nil(t, applyOriginTaskIntent(c, map[string]any{"originTaskIds": []any{}}, jsplugin.Meta{Key: "origin-plugin"}))
	_, pinned := resolvedOriginPin(c)
	assert.False(t, pinned)
}

func TestPrepareTaskPluginRoutePinsOriginTaskChannel(t *testing.T) {
	setupOriginTaskDB(t)
	channel := insertOriginTaskChannel(t, common.ChannelStatusEnabled)
	insertOriginOwnedTask(t, "task-route", 7, channel.Id, "origin-route")
	plugin := compileTaskRoutePlugin(t, `
export const meta = {
  apiVersion: 1, key: "origin-route", name: "Origin", version: "1.0.0",
  author: {name: "Test"},
  models: ["resolved-model"], fetchMode: "per_task",
  routes: [{method: "POST", path: "/vendor/jobs", type: "submit", decode: "decodeJob", render: "jobCreated"}],
};
export const native = {
  decodeJob: function() { return {kind: "submit", model: "resolved-model", originTaskIds: ["task-route"], requestBody: {prompt: "ok"}}; },
  jobCreated: function(ctx, task) { return task; },
};
export function buildSubmitRequest() { return {url: "https://example.com"}; }
export function parseSubmitResponse() { return {taskId: "one"}; }
export function buildQueryRequest() { return {url: "https://example.com"}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
`)
	reached := false
	router := gin.New()
	router.POST("/vendor/jobs", pinTaskPluginRoute(plugin, 0), func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUserId, 7)
		c.Next()
	}, PrepareTaskPluginRoute(), func(c *gin.Context) {
		reached = true
		pinnedID, ok := resolvedOriginPin(c)
		require.True(t, ok)
		assert.Equal(t, channel.Id, pinnedID)
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/vendor/jobs", strings.NewReader(`{"model":"resolved-model"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.True(t, reached)
	assert.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestPrepareTaskPluginRouteRejectsUnknownOriginTask(t *testing.T) {
	setupOriginTaskDB(t)
	plugin := compileTaskRoutePlugin(t, `
export const meta = {
  apiVersion: 1, key: "origin-route-missing", name: "Origin", version: "1.0.0",
  author: {name: "Test"},
  models: ["resolved-model"], fetchMode: "per_task",
  routes: [{method: "POST", path: "/vendor/jobs", type: "submit", decode: "decodeJob", render: "jobCreated"}],
};
export const native = {
  decodeJob: function() { return {kind: "submit", model: "resolved-model", originTaskIds: ["missing"], requestBody: {}}; },
  jobCreated: function(ctx, task) { return task; },
};
export function buildSubmitRequest() { return {url: "https://example.com"}; }
export function parseSubmitResponse() { return {taskId: "one"}; }
export function buildQueryRequest() { return {url: "https://example.com"}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
`)
	reached := false
	router := gin.New()
	router.POST("/vendor/jobs", pinTaskPluginRoute(plugin, 0), func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUserId, 7)
		c.Next()
	}, PrepareTaskPluginRoute(), func(c *gin.Context) { reached = true })
	request := httptest.NewRequest(http.MethodPost, "/vendor/jobs", strings.NewReader(`{"model":"resolved-model"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.False(t, reached)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestPrepareTaskPluginEndpointPinsOriginTaskChannel(t *testing.T) {
	setupOriginTaskDB(t)
	channel := insertOriginTaskChannel(t, common.ChannelStatusEnabled)
	insertOriginOwnedTask(t, "task-endpoint", 7, channel.Id, "origin-endpoint")
	const key = "origin-endpoint"
	_, err := jsplugin.DefaultRegistry.Register(taskProtocolPluginSource(
		key,
		"1.0.0",
		`["claimed-model"]`,
		"/v1/responses",
		`return {model: ctx.model, originTaskIds: ["task-endpoint"], requestBody: {prompt: "ok"}};`,
	), jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jsplugin.DefaultRegistry.Unregister(key)) })

	reached := false
	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUserId, 7)
		c.Next()
	}, PinTaskPluginEndpoint(), PrepareTaskPluginEndpoint(), func(c *gin.Context) {
		reached = true
		pinnedID, ok := resolvedOriginPin(c)
		require.True(t, ok)
		assert.Equal(t, channel.Id, pinnedID)
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"claimed-model","input":"hello"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.True(t, reached)
	assert.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestPrepareTaskPluginEndpointRejectsUnknownOriginTask(t *testing.T) {
	setupOriginTaskDB(t)
	const key = "origin-endpoint-missing"
	_, err := jsplugin.DefaultRegistry.Register(taskProtocolPluginSource(
		key,
		"1.0.0",
		`["claimed-model"]`,
		"/v1/responses",
		`return {model: ctx.model, originTaskIds: ["missing"], requestBody: {prompt: "ok"}};`,
	), jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jsplugin.DefaultRegistry.Unregister(key)) })

	reached := false
	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUserId, 7)
		c.Next()
	}, PinTaskPluginEndpoint(), PrepareTaskPluginEndpoint(), func(c *gin.Context) { reached = true })
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"claimed-model","input":"hello"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.False(t, reached)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "origin_task_not_found")
}

func TestDistributeHonorsOriginTaskChannelPin(t *testing.T) {
	require.NoError(t, appI18n.Init())
	setupOriginTaskDB(t)
	channel := insertOriginTaskChannel(t, common.ChannelStatusEnabled)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/vendor/jobs", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("resolved_task_model", "resolved-model")
	service.GetChannelConstraints(c).AddPin(dto.ChannelPin{
		ChannelId: channel.Id,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})

	nextCalled := false
	handler := Distribute()
	handler(c)
	if !c.IsAborted() {
		nextCalled = true
	}
	assert.True(t, nextCalled)
	assert.Equal(t, channel.Id, common.GetContextKeyInt(c, constant.ContextKeyChannelId))
}

func TestDistributeTokenPinBeatsOriginPin(t *testing.T) {
	require.NoError(t, appI18n.Init())
	setupOriginTaskDB(t)
	tokenChannel := insertOriginTaskChannel(t, common.ChannelStatusEnabled)
	originChannel := insertOriginTaskChannel(t, common.ChannelStatusEnabled)

	var warnBuf bytes.Buffer
	previousWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &warnBuf
	t.Cleanup(func() { gin.DefaultErrorWriter = previousWriter })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/vendor/jobs", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("resolved_task_model", "resolved-model")
	constraints := service.GetChannelConstraints(c)
	constraints.AddPin(dto.ChannelPin{
		ChannelId: tokenChannel.Id,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	constraints.AddPin(dto.ChannelPin{
		ChannelId: originChannel.Id,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	nextCalled := false
	Distribute()(c)
	if !c.IsAborted() {
		nextCalled = true
	}
	assert.True(t, nextCalled)
	assert.Equal(t, tokenChannel.Id, common.GetContextKeyInt(c, constant.ContextKeyChannelId))
	warn := warnBuf.String()
	assert.Contains(t, warn, "winning_source=token")
	assert.Contains(t, warn, fmt.Sprintf("winning_channel_id=%d", tokenChannel.Id))
	assert.Contains(t, warn, "overridden_source=origin_task")
	assert.Contains(t, warn, fmt.Sprintf("overridden_channel_id=%d", originChannel.Id))
}

func TestDistributePinViolatingIdentityFilterErrors(t *testing.T) {
	require.NoError(t, appI18n.Init())
	setupOriginTaskDB(t)
	channel := insertOriginTaskChannel(t, common.ChannelStatusEnabled)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/vendor/jobs", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("resolved_task_model", "resolved-model")
	c.Set("expected_task_plugin_key", "alpha")
	service.GetChannelConstraints(c).AddPin(dto.ChannelPin{
		ChannelId: channel.Id,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})

	Distribute()(c)
	assert.True(t, c.IsAborted())
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), string(dto.FilterTaskPluginIdentity))
}
