package relay

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	jspluginadaptor "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetTaskAdaptorMapsMigratedPlatformsToFactoryPlugins(t *testing.T) {
	platforms := []constant.TaskPlatform{
		constant.TaskPlatformSuno,
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeAli)),
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeDoubaoVideo)),
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeVolcEngine)),
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeGemini)),
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeMiniMax)),
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeJimeng)),
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeKling)),
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeVidu)),
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeSora)),
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeOpenAI)),
		constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeVertexAi)),
	}
	for _, platform := range platforms {
		_, isJS := GetTaskAdaptor(platform).(*jspluginadaptor.TaskAdaptor)
		assert.True(t, isJS, "platform %s should use its factory plugin", platform)
	}
}

func TestGetTaskAdaptorUsesPlatformAsThirdPartyPluginKey(t *testing.T) {
	t.Cleanup(func() {
		pluginruntime.DefaultRegistry.Unregister("registry-fallback")
	})
	source := `
export const meta = {apiVersion: 1, key: "registry-fallback", name: "Registry Fallback", version: "1.0.0", author: {name: "Test"}, channelTypes: [1999], models: ["fallback-v1"], fetchMode: "per_task"};
export function buildSubmitRequest(ctx) { return {url: ctx.baseUrl + "/submit"}; }
export function parseSubmitResponse(ctx, resp) { return {taskId: "id", taskData: resp.body}; }
export function buildQueryRequest(ctx) { return {url: ctx.baseUrl + "/tasks/" + ctx.taskId}; }
export function parseTaskResult(ctx, body) { return {taskId: body.id, status: "SUCCESS"}; }
`
	_, err := pluginruntime.DefaultRegistry.Register(source, pluginruntime.Options{})
	require.NoError(t, err)

	adaptor := GetTaskAdaptor(constant.TaskPlatform("registry-fallback"))
	require.NotNil(t, adaptor)
	assert.Equal(t, "Registry Fallback", adaptor.GetChannelName())
}

func TestGetTaskAdaptorReturnsNilForUnknownPlatform(t *testing.T) {
	assert.Nil(t, GetTaskAdaptor(constant.TaskPlatform("missing-task-platform")))
}

func TestGetTaskAdaptorForRequestUsesExactPinnedPlugin(t *testing.T) {
	source := `
export const meta = {apiVersion: 1, key: "pinned-request", name: "Pinned Generation", version: "1.0.0", author: {name: "Test"}, models: ["pinned-v1"], fetchMode: "per_task"};
export function buildSubmitRequest(ctx) { return {url: ctx.baseUrl + "/submit"}; }
export function parseSubmitResponse(ctx) { return {taskId: "one"}; }
export function buildQueryRequest(ctx) { return {url: ctx.baseUrl + "/query"}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
`
	pinned, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/vendor/submit", nil)
	c.Set(pluginruntime.ContextKeyPinnedPlugin, pluginruntime.PinnedPlugin{Plugin: pinned})

	platform, adaptor := getTaskAdaptorForRequest(c, constant.TaskPlatform("missing-task-platform"))
	require.NotNil(t, adaptor)
	assert.Equal(t, constant.TaskPlatform("pinned-request"), platform)
	assert.Equal(t, "Pinned Generation", adaptor.GetChannelName())
}

func TestGetTaskAdaptorForRequestPinsLegacyMappedPlugin(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/video_1/remix", nil)
	legacyPlatform := constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeSora))

	platform, adaptor := getTaskAdaptorForRequest(c, legacyPlatform)

	require.NotNil(t, adaptor)
	assert.Equal(t, legacyPlatform, platform)
	pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedPlugin)
	require.True(t, exists)
	pinned, ok := pinnedValue.(pluginruntime.PinnedPlugin)
	require.True(t, ok)
	require.NotNil(t, pinned.Generation)
	require.NotNil(t, pinned.Plugin)
	assert.Equal(t, "sora", pinned.Plugin.Meta.Key)
	assert.Same(t, pinned.Generation, pluginruntime.DefaultRegistry.Generation())
}
