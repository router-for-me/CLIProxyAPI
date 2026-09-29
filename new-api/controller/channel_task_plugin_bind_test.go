package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupTaskPluginBindChannelTest(t *testing.T) {
	t.Helper()
	wasMaster := common.IsMasterNode
	common.IsMasterNode = true
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	originalDB, originalLogDB := model.DB, model.LOG_DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.CasbinRule{}, &model.AuthzRole{}, &model.Log{}, &model.AuditLog{}, &model.User{}))
	model.DB = database
	model.LOG_DB = database
	require.NoError(t, authz.Init(database))
	t.Cleanup(func() {
		common.IsMasterNode = wasMaster
		common.RedisEnabled = previousRedisEnabled
		model.DB = originalDB
		model.LOG_DB = originalLogDB
	})
}

func postAddChannel(t *testing.T, userID, role int, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", userID)
	context.Set("role", role)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/channel", strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	AddChannel(context)
	return recorder
}

func TestAddChannelTaskPluginRequiresBindPermission(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	const key = "channel-bind"
	source := `
export const meta = {apiVersion: 1, key: "channel-bind", name: "Bind", version: "1.0.0", author: {name: "Test"}, models: ["doc"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })

	taskPluginBody := `{"mode":"single","channel":{"type":61,"name":"plugin-channel","key":"sk","models":"doc","group":"default","base_url":"https://example.com","setting":"{\"task_plugin_key\":\"channel-bind\"}"}}`
	openaiBody := `{"mode":"single","channel":{"type":1,"name":"openai-channel","key":"sk","models":"gpt","group":"default"}}`

	adminDenied := postAddChannel(t, 2, common.RoleAdminUser, taskPluginBody)
	assert.Contains(t, adminDenied.Body.String(), "task plugin channels require the task_plugin.bind permission")
	assert.Contains(t, adminDenied.Body.String(), `"success":false`)

	rootAllowed := postAddChannel(t, 1, common.RoleRootUser, taskPluginBody)
	assert.Contains(t, rootAllowed.Body.String(), `"success":true`)
	assert.NotContains(t, rootAllowed.Body.String(), "task_plugin.bind")

	adminOtherType := postAddChannel(t, 2, common.RoleAdminUser, openaiBody)
	assert.Contains(t, adminOtherType.Body.String(), `"success":true`)
	assert.NotContains(t, adminOtherType.Body.String(), "task_plugin.bind")
}

func TestUpdateChannelTaskPluginRequiresBindPermission(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	const key = "channel-bind-update"
	source := `
export const meta = {apiVersion: 1, key: "channel-bind-update", name: "Bind", version: "1.0.0", author: {name: "Test"}, models: ["doc"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })

	baseURL := "https://example.com"
	setting := `{"task_plugin_key":"channel-bind-update"}`
	channel := model.Channel{
		Type:    constant.ChannelTypeTaskPlugin,
		Status:  common.ChannelStatusEnabled,
		Name:    "existing-plugin",
		Models:  "doc",
		Group:   "default",
		Key:     "sk",
		BaseURL: &baseURL,
		Setting: &setting,
	}
	require.NoError(t, channel.Insert())

	payload := fmt.Sprintf(
		`{"id":%d,"type":61,"name":"existing-plugin","key":"sk","models":"doc","group":"default","base_url":"https://example.com","setting":"{\"task_plugin_key\":\"channel-bind-update\"}"}`,
		channel.Id,
	)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 2)
	context.Set("role", common.RoleAdminUser)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/channel", strings.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	UpdateChannel(context)
	assert.Contains(t, recorder.Body.String(), "task plugin channels require the task_plugin.bind permission")
	assert.Contains(t, recorder.Body.String(), `"success":false`)
}

func TestAddChannelTaskPluginPersistsPluginDefaultBaseURLAndAuditsSource(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	for key, baseURLField := range map[string]string{"bind-default-url": `baseUrl: "http://10.0.0.5:8000/",`, "bind-no-default": ""} {
		source := fmt.Sprintf(`
export const meta = {apiVersion: 1, key: %q, name: "Bind", version: "1.0.0", author: {name: "Test"}, %s models: ["doc"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`, key, baseURLField)
		_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
		require.NoError(t, err)
		t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })
	}
	body := func(pluginKey string) string {
		return fmt.Sprintf(`{"mode":"single","channel":{"type":61,"name":"%s","key":"sk","models":"doc","group":"default","setting":"{\"task_plugin_key\":\"%s\"}"}}`, pluginKey, pluginKey)
	}

	noDefault := postAddChannel(t, 1, common.RoleRootUser, body("bind-no-default"))
	assert.Contains(t, noDefault.Body.String(), "base URL is required for task plugin channels")

	filled := postAddChannel(t, 1, common.RoleRootUser, body("bind-default-url"))
	require.Contains(t, filled.Body.String(), `"success":true`)
	var created model.Channel
	require.NoError(t, model.DB.Where("name = ?", "bind-default-url").First(&created).Error)
	require.NotNil(t, created.BaseURL)
	assert.Equal(t, "http://10.0.0.5:8000", *created.BaseURL, "the normalized plugin default is stored on the channel row")

	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "channel.create").Find(&audits).Error)
	encoded, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"base_url_source":"plugin_default"`)
}

func putUpdateChannel(t *testing.T, userID, role int, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", userID)
	context.Set("role", role)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/channel", strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	UpdateChannel(context)
	return recorder
}

func TestNewAPIChannelPluginBindingsRequireBindPermission(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	const key = "gateway-bind"
	source := `
export const meta = {apiVersion: 1, key: "gateway-bind", name: "Gateway", version: "1.0.0", author: {name: "Test"}, models: ["gateway-doc"], fetchMode: "per_task", upstreams: ["vendor", "new_api"]};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })

	bound := `{"mode":"single","channel":{"type":60,"name":"gateway","key":"sk","models":"gateway-doc","group":"default","base_url":"https://gateway.example","setting":"{\"task_extend_plugin_keys\":[\"gateway-bind\"]}"}}`
	unbound := `{"mode":"single","channel":{"type":60,"name":"plain-gateway","key":"sk","models":"gpt","group":"default","base_url":"https://gateway.example"}}`
	adminDenied := postAddChannel(t, 2, common.RoleAdminUser, bound)
	assert.Contains(t, adminDenied.Body.String(), "task plugin channels require the task_plugin.bind permission")
	rootAllowed := postAddChannel(t, 1, common.RoleRootUser, bound)
	assert.Contains(t, rootAllowed.Body.String(), `"success":true`)
	adminUnbound := postAddChannel(t, 2, common.RoleAdminUser, unbound)
	assert.Contains(t, adminUnbound.Body.String(), `"success":true`, "a gateway channel without plugin bindings needs no bind permission")

	baseURL := "https://gateway.example"
	setting := `{"task_extend_plugin_keys":["gateway-bind"]}`
	channel := model.Channel{Type: constant.ChannelTypeNewAPI, Status: common.ChannelStatusEnabled, Name: "existing-gateway", Models: "gateway-doc", Group: "default", Key: "sk", BaseURL: &baseURL, Setting: &setting}
	require.NoError(t, channel.Insert())
	update := func(name, setting string) string {
		return fmt.Sprintf(`{"id":%d,"type":60,"name":%q,"key":"sk","models":"gateway-doc","group":"default","base_url":"https://gateway.example","setting":%q}`, channel.Id, name, setting)
	}
	unchanged := putUpdateChannel(t, 2, common.RoleAdminUser, update("renamed-gateway", setting))
	assert.Contains(t, unchanged.Body.String(), `"success":true`, "resubmitting the stored bindings does not need the bind permission")
	assert.NotContains(t, unchanged.Body.String(), "task_plugin.bind")
	rebound := putUpdateChannel(t, 2, common.RoleAdminUser, update("renamed-gateway", `{"task_extend_plugin_keys":[]}`))
	assert.Contains(t, rebound.Body.String(), "task plugin channels require the task_plugin.bind permission")
	rootRebound := putUpdateChannel(t, 1, common.RoleRootUser, update("renamed-gateway", `{"task_extend_plugin_keys":[]}`))
	assert.Contains(t, rootRebound.Body.String(), `"success":true`)

	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("setting", setting).Error)
	copyChannel := func(userID, role int) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Set("id", userID)
		context.Set("role", role)
		context.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
		context.Request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/channel/copy/%d", channel.Id), nil)
		CopyChannel(context)
		return recorder
	}
	assert.Contains(t, copyChannel(2, common.RoleAdminUser).Body.String(), "task plugin channels require the task_plugin.bind permission")
	assert.Contains(t, copyChannel(1, common.RoleRootUser).Body.String(), `"success":true`)
}
