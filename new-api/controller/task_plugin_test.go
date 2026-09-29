package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupTaskPluginControllerTest(t *testing.T) {
	t.Helper()
	originalDB := model.DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.TaskPlugin{}, &model.Channel{}, &model.Ability{}, &model.Task{}, &model.Option{}))
	model.DB = database
	t.Cleanup(func() { model.DB = originalDB })
}

const lifecyclePluginSource = `
export const meta = {apiVersion: 1, key: "lifecycle-only", name: "Lifecycle", version: "1.0.0", author: {name: "Test"}, models: ["doc"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`

func taskPluginControllerTestSource(key, version string) string {
	return fmt.Sprintf(`
export const meta = {apiVersion: 1, key: %q, name: "Test", version: %q, author: {name: "Test"}, models: ["doc-1"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`, key, version)
}

func taskPluginControllerChannelSource(key, version string, channelType int) string {
	return fmt.Sprintf(`
export const meta = {apiVersion: 1, key: %q, name: "Test", version: %q, author: {name: "Test"}, channelTypes: [%d], models: ["doc-1"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`, key, version, channelType)
}

func cleanupTaskPluginControllerRuntime(t *testing.T, key string) {
	t.Helper()
	t.Cleanup(func() {
		jsplugin.DefaultRegistry.Unregister(key)
		taskPluginSyncState.Lock()
		delete(taskPluginSyncState.hashes, key)
		delete(taskPluginSyncState.errors, key)
		taskPluginSyncState.Unlock()
	})
}

func TestDeleteThirdPartyPluginReportsAssociatedChannelsAndInFlightTasks(t *testing.T) {
	setupTaskPluginControllerTest(t)
	loaded, err := jsplugin.DefaultRegistry.Register(lifecyclePluginSource, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("lifecycle-only") })
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{Key: loaded.Meta.Key, APIVersion: 1, Version: "1", Source: lifecyclePluginSource, SourceHash: "hash", Enabled: true}))
	baseURL := "https://example.com"
	setting := `{"task_plugin_key":"lifecycle-only"}`
	channel := model.Channel{Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Name: "linked", Models: "doc", Group: "default", BaseURL: &baseURL, Setting: &setting}
	require.NoError(t, channel.Insert())
	require.NoError(t, model.DB.Create(&model.Task{Platform: "lifecycle-only", Status: model.TaskStatusInProgress}).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "key", Value: "lifecycle-only"}, {Key: "version", Value: "1"}}
	context.Request = httptest.NewRequest(http.MethodDelete, "/api/plugin/task/lifecycle-only/versions/1", nil)
	DeleteTaskPluginVersion(context)

	assert.Contains(t, recorder.Body.String(), `"name":"linked"`)
	assert.Contains(t, recorder.Body.String(), `"in_flight_count":1`)
}

func TestDisableThirdPartyPluginSupportsCascadeAndForce(t *testing.T) {
	setupTaskPluginControllerTest(t)
	loaded, err := jsplugin.DefaultRegistry.Register(lifecyclePluginSource, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("lifecycle-only") })
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{Key: loaded.Meta.Key, APIVersion: 1, Version: "1", Source: lifecyclePluginSource, SourceHash: "hash", Enabled: true}))
	baseURL := "https://example.com"
	setting := `{"task_plugin_key":"lifecycle-only"}`
	channel := model.Channel{Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Name: "linked", Models: "doc", Group: "default", BaseURL: &baseURL, Setting: &setting}
	require.NoError(t, channel.Insert())
	gatewaySetting := `{"task_extend_plugin_keys":["lifecycle-only","other"]}`
	gateway := model.Channel{Type: constant.ChannelTypeNewAPI, Status: common.ChannelStatusEnabled, Name: "gateway", Models: "doc,gpt", Group: "default", BaseURL: &baseURL, Setting: &gatewaySetting}
	require.NoError(t, gateway.Insert())
	require.NoError(t, model.DB.Create(&model.Task{Platform: "lifecycle-only", Status: model.TaskStatusSubmitted}).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "key", Value: "lifecycle-only"}}
	context.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task/lifecycle-only/status?cascade=true&force=true", strings.NewReader(`{"enabled":false}`))
	context.Request.Header.Set("Content-Type", "application/json")
	SetTaskPluginStatus(context)

	assert.Contains(t, recorder.Body.String(), `"success":true`)
	assert.Contains(t, recorder.Body.String(), `"disabled_channels":1`)
	assert.Contains(t, recorder.Body.String(), `"unbound_channels":1`)
	updated, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, updated.Status)
	updatedGateway, err := model.GetChannelById(gateway.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, updatedGateway.Status, "a gateway channel keeps serving its other traffic")
	assert.Equal(t, []string{"other"}, updatedGateway.GetSetting().TaskExtendPluginKeys, "only the disabled plugin is unbound")
}

// klingFactoryVersion returns the version declared in the embedded kling factory
// manifest so tests do not hardcode a value that moves with every plugin release.
func klingFactoryVersion(t *testing.T) string {
	t.Helper()
	factorySource, err := plugins.Source("kling")
	require.NoError(t, err)
	match := regexp.MustCompile(`version:\s*"([^"]+)"`).FindStringSubmatch(factorySource)
	require.Len(t, match, 2, "kling factory manifest must declare a version")
	return match[1]
}

func setupTaskPluginFactoryDisableTest(t *testing.T) {
	t.Helper()
	setupTaskPluginControllerTest(t)
	originalMap := common.OptionMap
	common.OptionMapRWMutex.Lock()
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		jsplugin.DefaultRegistry.SetDisabledFactoryKeys(nil)
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalMap
		common.OptionMapRWMutex.Unlock()
	})
}

func postTaskPluginStatus(t *testing.T, key, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "key", Value: key}}
	path := "/api/plugin/task/" + key + "/status"
	if query != "" {
		path += "?" + query
	}
	context.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	SetTaskPluginStatus(context)
	return recorder
}

func listTaskPluginItem(t *testing.T, key string) taskPluginListItem {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/plugin/task", nil)
	ListTaskPlugins(context)
	var response struct {
		Success bool                 `json:"success"`
		Data    []taskPluginListItem `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	for _, item := range response.Data {
		if item.Meta.Key == key {
			return item
		}
	}
	t.Fatalf("task plugin %q not found", key)
	return taskPluginListItem{}
}

func taskPluginOptionsHasKey(t *testing.T, key string) bool {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/task_plugin_options", nil)
	GetTaskPluginOptions(context)
	var response struct {
		Success bool `json:"success"`
		Data    []struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	for _, option := range response.Data {
		if option.Key == key {
			return true
		}
	}
	return false
}

func TestDisableFactoryPluginPersistsOptionAndHidesFromBindOptions(t *testing.T) {
	setupTaskPluginFactoryDisableTest(t)
	const key = "kling"

	recorder := postTaskPluginStatus(t, key, "", `{"enabled":false}`)
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	assert.Equal(t, []string{key}, setting.GetTaskPluginDisabledFactoryKeys())
	var stored model.Option
	require.NoError(t, model.DB.Where("key = ?", setting.TaskPluginDisabledFactoryKeysKey).First(&stored).Error)
	assert.Equal(t, `["kling"]`, stored.Value)

	item := listTaskPluginItem(t, key)
	assert.Equal(t, "factory", item.Source)
	assert.False(t, item.Enabled)
	assert.Equal(t, "disabled", item.RuntimeStatus)
	assert.False(t, taskPluginOptionsHasKey(t, key))
	_, ok := jsplugin.DefaultRegistry.Get(key)
	assert.False(t, ok)

	recorder = postTaskPluginStatus(t, key, "", `{"enabled":true}`)
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	assert.Empty(t, setting.GetTaskPluginDisabledFactoryKeys())
	item = listTaskPluginItem(t, key)
	assert.True(t, item.Enabled)
	assert.Equal(t, "registered", item.RuntimeStatus)
	assert.True(t, taskPluginOptionsHasKey(t, key))
	_, ok = jsplugin.DefaultRegistry.Get(key)
	assert.True(t, ok)
}

func TestDisableFactoryPluginRespectsInUseGuard(t *testing.T) {
	setupTaskPluginFactoryDisableTest(t)
	const key = "kling"
	baseURL := "https://example.com"
	channelSetting := `{"task_plugin_key":"kling"}`
	channel := model.Channel{Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Name: "linked-factory", Models: "doc", Group: "default", BaseURL: &baseURL, Setting: &channelSetting}
	require.NoError(t, channel.Insert())

	recorder := postTaskPluginStatus(t, key, "", `{"enabled":false}`)
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	assert.Contains(t, recorder.Body.String(), "task plugin is still in use")
	assert.Empty(t, setting.GetTaskPluginDisabledFactoryKeys())
	_, ok := jsplugin.DefaultRegistry.Get(key)
	assert.True(t, ok)
}

func TestDisableFactoryOverrideRowSuppressesBothLayers(t *testing.T) {
	setupTaskPluginFactoryDisableTest(t)
	factorySource, err := plugins.Source("kling")
	require.NoError(t, err)
	factoryVersion := klingFactoryVersion(t)
	overrideSource := strings.Replace(factorySource, `version: "`+factoryVersion+`"`, `version: "`+factoryVersion+`-test-factory-status"`, 1)
	require.NotEqual(t, factorySource, overrideSource, "factory version marker must be found in kling source")
	loaded, err := jsplugin.DefaultRegistry.Register(overrideSource, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("kling") })
	plugin := model.TaskPlugin{
		Key: "kling", APIVersion: loaded.Meta.APIVersion, Version: loaded.Meta.Version,
		Source: model.LongText(overrideSource), SourceHash: "test-hash", Enabled: true,
	}
	require.NoError(t, model.SaveTaskPlugin(&plugin))
	require.NoError(t, syncTaskPluginsOnce())

	recorder := postTaskPluginStatus(t, "kling", "", `{"enabled":false}`)
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	assert.Equal(t, []string{"kling"}, setting.GetTaskPluginDisabledFactoryKeys())
	row, err := model.GetTaskPluginVersion("kling", "")
	require.NoError(t, err)
	assert.False(t, row.Enabled)
	item := listTaskPluginItem(t, "kling")
	assert.Equal(t, "override_over_factory", item.Source)
	assert.False(t, item.Enabled)
	assert.Equal(t, "disabled", item.RuntimeStatus)
	assert.False(t, taskPluginOptionsHasKey(t, "kling"))
	_, ok := jsplugin.DefaultRegistry.Get("kling")
	assert.False(t, ok, "factory built-in must not keep serving after the plugin is switched off")

	recorder = postTaskPluginStatus(t, "kling", "", `{"enabled":true}`)
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	assert.Empty(t, setting.GetTaskPluginDisabledFactoryKeys())
	row, err = model.GetTaskPluginVersion("kling", "")
	require.NoError(t, err)
	assert.True(t, row.Enabled)
	got, ok := jsplugin.DefaultRegistry.Get("kling")
	require.True(t, ok)
	assert.Equal(t, loaded.Meta.Version, got.Meta.Version)
	assert.Equal(t, "registered", listTaskPluginItem(t, "kling").RuntimeStatus)

	// Regression: with every kling layer off, a plugin whose model differs from
	// a built-in kling model only by case must upload without a routing conflict.
	recorder = postTaskPluginStatus(t, "kling", "", `{"enabled":false}`)
	require.Contains(t, recorder.Body.String(), `"success":true`)
	cleanupTaskPluginControllerRuntime(t, "kling-shadow")
	const shadowSource = `
export const meta = {apiVersion: 1, key: "kling-shadow", name: "Shadow", version: "1.0.0", author: {name: "Test"}, models: ["KLING-V1"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	body, err := common.Marshal(map[string]any{"source": shadowSource})
	require.NoError(t, err)
	uploadRecorder := httptest.NewRecorder()
	uploadContext, _ := gin.CreateTestContext(uploadRecorder)
	uploadContext.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task", strings.NewReader(string(body)))
	uploadContext.Request.Header.Set("Content-Type", "application/json")
	UploadTaskPlugin(uploadContext)
	assert.Contains(t, uploadRecorder.Body.String(), `"success":true`)
	_, ok = jsplugin.DefaultRegistry.Get("kling-shadow")
	assert.True(t, ok)
}

// The shipped alibaba plugin is already larger than a MySQL TEXT column
// (64 KiB), so storing it through the model path the upload endpoint uses and
// reading it back unchanged proves the source and icon columns are not
// truncated. Set TEST_TASK_DB_DIALECT plus TEST_MYSQL_DSN or TEST_POSTGRES_DSN
// to run the same contract against a real MySQL or PostgreSQL server.
func TestTaskPluginSourceAboveMySQLTextLimitRoundTrips(t *testing.T) {
	database, _ := openTaskDialectDatabase(t, &model.TaskPlugin{})
	originalDB := model.DB
	model.DB = database
	t.Cleanup(func() { model.DB = originalDB })

	source, err := plugins.Source("alibaba")
	require.NoError(t, err)
	require.Greater(t, len(source), 65535, "fixture must exceed the MySQL TEXT limit")
	icon := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(strings.Repeat(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`, 2048)))
	require.Greater(t, len(icon), 65535, "icon fixture must exceed the MySQL TEXT limit")

	plugin := model.TaskPlugin{Key: "alibaba", APIVersion: 1, Version: "roundtrip", Source: model.LongText(source), SourceHash: "hash", Enabled: true}
	require.NoError(t, model.SaveTaskPlugin(&plugin))
	// Re-saving the same version attaches the icon through the update path.
	withIcon := model.TaskPlugin{Key: "alibaba", APIVersion: 1, Version: "roundtrip", Source: model.LongText(source), SourceHash: "hash", Icon: model.LongText(icon), Enabled: true}
	require.NoError(t, model.SaveTaskPlugin(&withIcon))

	stored, err := model.GetTaskPluginVersion("alibaba", "roundtrip")
	require.NoError(t, err)
	assert.Equal(t, source, string(stored.Source))
	assert.Equal(t, icon, string(stored.Icon))

	// The 30-second sync poll must not pull these payloads; it reads hashes
	// and fetches source only for rows it has to recompile.
	snapshot, err := model.GetTaskPluginSyncSnapshot()
	require.NoError(t, err)
	require.Len(t, snapshot.Plugins, 1)
	assert.Equal(t, stored.Id, snapshot.Plugins[0].Id)
	assert.Equal(t, "hash", snapshot.Plugins[0].SourceHash)
	assert.Empty(t, snapshot.Plugins[0].Source)
	assert.Empty(t, snapshot.Plugins[0].Icon)
	loaded, err := model.GetTaskPluginSource(snapshot.Plugins[0].Id)
	require.NoError(t, err)
	assert.Equal(t, source, string(loaded))
}

func TestUploadTaskPluginAcceptsSourcesUpToEightMiB(t *testing.T) {
	setupTaskPluginControllerTest(t)
	cleanupTaskPluginControllerRuntime(t, "large-source")
	upload := func(source string) *httptest.ResponseRecorder {
		body, err := common.Marshal(map[string]any{"source": source})
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task", bytes.NewReader(body))
		context.Request.Header.Set("Content-Type", "application/json")
		UploadTaskPlugin(context)
		return recorder
	}

	// A trailing comment pads a valid plugin past the former 1 MiB cap.
	accepted := taskPluginControllerTestSource("large-source", "1.0.0") + "// " + strings.Repeat("x", 2<<20) + "\n"
	recorder := upload(accepted)
	require.Contains(t, recorder.Body.String(), `"success":true`)
	stored, err := model.GetTaskPluginVersion("large-source", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, accepted, string(stored.Source))

	rejected := upload(accepted + strings.Repeat("x", 6<<20))
	assert.Contains(t, rejected.Body.String(), `"success":false`)
	assert.Contains(t, rejected.Body.String(), "plugin source exceeds 8 MiB")
	_, err = model.GetTaskPluginVersion("large-source", "2.0.0")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestListTaskPluginsIncludesFactoryWithoutDatabaseRows(t *testing.T) {
	setupTaskPluginControllerTest(t)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/plugin/task", nil)

	ListTaskPlugins(context)

	var response struct {
		Success bool                 `json:"success"`
		Data    []taskPluginListItem `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	var factoryItem *taskPluginListItem
	for i := range response.Data {
		if response.Data[i].Meta.Key == "kling" {
			factoryItem = &response.Data[i]
			break
		}
	}
	require.NotNil(t, factoryItem)
	assert.Equal(t, "factory", factoryItem.Source)
	assert.Equal(t, "registered", factoryItem.RuntimeStatus)
	assert.NotEmpty(t, factoryItem.SourceHash)
}

func TestMasterSwitchEmptiesOptionsAndKeepsList(t *testing.T) {
	setupTaskPluginControllerTest(t)
	originalEnabled := constant.TaskPluginEnabled
	jsplugin.DefaultRegistry.SetEnabled(false)
	t.Cleanup(func() {
		constant.TaskPluginEnabled = originalEnabled
		jsplugin.DefaultRegistry.SetEnabled(originalEnabled)
	})

	assert.False(t, taskPluginOptionsHasKey(t, "kling"))
	item := listTaskPluginItem(t, "kling")
	assert.Equal(t, "factory", item.Source)
	assert.Equal(t, "kling", item.Meta.Key)
}

func TestGetTaskPluginOptionsIncludesDescriptionUsageSchemaIconBaseURLAndChannelTypes(t *testing.T) {
	setupTaskPluginControllerTest(t)
	const key = "usage-options-probe"
	source := `
export const meta = {
  apiVersion: 1, key: "usage-options-probe", name: "Usage Options", version: "1.0.0", author: {name: "Test"},
  description: {en: "Video generation via the vendor API", zh: "通过厂商接口生成视频"},
  icon: "text:UO", baseUrl: "http://localhost:9000/",
  channelTypes: [1990, 1991],
  upstreams: ["vendor", "new_api"],
  models: ["usage-options-model"], fetchMode: "per_task",
  usageSchema: {seconds: {type: "number", unit: "second", description: "Video generation unit price"}},
  usageProfiles: [{models:["usage-options-model"],schema:{image_count:{type:"number",unit:"count"}}}]
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/task_plugin_options", nil)

	GetTaskPluginOptions(context)

	var response struct {
		Success bool `json:"success"`
		Data    []struct {
			Key           string                               `json:"key"`
			Description   jsplugin.LocalizedText               `json:"description"`
			Icon          string                               `json:"icon"`
			BaseURL       string                               `json:"baseUrl"`
			ChannelTypes  []int                                `json:"channelTypes"`
			Upstreams     []string                             `json:"upstreams"`
			UsageSchema   map[string]jsplugin.UsageFieldSchema `json:"usageSchema"`
			UsageProfiles []jsplugin.UsageProfile              `json:"usageProfiles"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	for _, option := range response.Data {
		if option.Key != key {
			continue
		}
		assert.Equal(t, jsplugin.LocalizedText{"en": "Video generation via the vendor API", "zh": "通过厂商接口生成视频"}, option.Description)
		assert.Equal(t, "second", option.UsageSchema["seconds"].Unit)
		assert.Equal(t, "Video generation unit price", option.UsageSchema["seconds"].Description["en"])
		require.Len(t, option.UsageProfiles, 1)
		assert.Equal(t, []string{"usage-options-model"}, option.UsageProfiles[0].Models)
		assert.Equal(t, "count", option.UsageProfiles[0].Schema["image_count"].Unit)
		assert.Equal(t, "text:UO", option.Icon)
		assert.Equal(t, []int{1990, 1991}, option.ChannelTypes)
		assert.Equal(t, []string{"vendor", "new_api"}, option.Upstreams, "the New API channel form lists only plugins declaring new_api")
		assert.Equal(t, "http://localhost:9000", option.BaseURL, "the drawer prefills the normalized plugin default")
		return
	}
	t.Fatal("task plugin option not found")
}

func TestDeleteActiveOverrideFallsBackToFactoryAndDeletesRecord(t *testing.T) {
	setupTaskPluginControllerTest(t)
	factorySource, err := plugins.Source("kling")
	require.NoError(t, err)
	factoryVersion := klingFactoryVersion(t)
	overrideSource := strings.Replace(factorySource, `version: "`+factoryVersion+`"`, `version: "`+factoryVersion+`-test-override"`, 1)
	require.NotEqual(t, factorySource, overrideSource, "factory version marker must be found in kling source")
	loaded, err := jsplugin.DefaultRegistry.Register(overrideSource, jsplugin.Options{Key: "kling", Version: "test-override"})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("kling") })
	plugin := model.TaskPlugin{
		Key: "kling", APIVersion: loaded.Meta.APIVersion, Version: loaded.Meta.Version,
		Source: model.LongText(overrideSource), SourceHash: "test-hash", Enabled: true,
	}
	require.NoError(t, model.SaveTaskPlugin(&plugin))
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "key", Value: "kling"}, {Key: "version", Value: loaded.Meta.Version}}
	context.Request = httptest.NewRequest(http.MethodDelete, "/api/plugin/task/kling/versions/"+loaded.Meta.Version, nil)

	DeleteTaskPluginVersion(context)

	assert.Contains(t, recorder.Body.String(), `"success":true`)
	versions, err := model.ListTaskPluginVersions("kling")
	require.NoError(t, err)
	assert.Empty(t, versions)
	runtimePlugin, ok := jsplugin.DefaultRegistry.Get("kling")
	require.True(t, ok)
	assert.NotEqual(t, loaded.Meta.Version, runtimePlugin.Meta.Version)
}

func TestDeleteActiveTaskPluginPromotesEnabledVersionInRuntime(t *testing.T) {
	setupTaskPluginControllerTest(t)
	key := "delete-promote-probe"
	cleanupTaskPluginControllerRuntime(t, key)
	v1Source := taskPluginControllerTestSource(key, "1.0.0")
	v2Source := taskPluginControllerTestSource(key, "2.0.0")
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: key, APIVersion: 1, Version: "1.0.0", Source: model.LongText(v1Source), SourceHash: "hash-v1", Enabled: true,
	}))
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: key, APIVersion: 1, Version: "2.0.0", Source: model.LongText(v2Source), SourceHash: "hash-v2", Enabled: true,
	}))
	_, err := jsplugin.DefaultRegistry.Register(v1Source, jsplugin.Options{Key: key, Version: "1.0.0"})
	require.NoError(t, err)
	taskPluginSyncState.Lock()
	taskPluginSyncState.hashes[key] = "hash-v1"
	taskPluginSyncState.errors[key] = "stale compile error"
	taskPluginSyncState.Unlock()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "key", Value: key}, {Key: "version", Value: "1.0.0"}}
	context.Request = httptest.NewRequest(http.MethodDelete, "/api/plugin/task/"+key+"/versions/1.0.0", nil)
	DeleteTaskPluginVersion(context)

	assert.Contains(t, recorder.Body.String(), `"success":true`)
	active, err := model.GetTaskPluginVersion(key, "")
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", active.Version)
	runtimePlugin, ok := jsplugin.DefaultRegistry.Get(key)
	require.True(t, ok)
	assert.Equal(t, "2.0.0", runtimePlugin.Meta.Version)
	taskPluginSyncState.Lock()
	syncedHash := taskPluginSyncState.hashes[key]
	_, hasSyncError := taskPluginSyncState.errors[key]
	taskPluginSyncState.Unlock()
	assert.Equal(t, "hash-v2", syncedHash)
	assert.False(t, hasSyncError)
}

func TestUploadTaskPluginRefreshesRuntimeSyncState(t *testing.T) {
	setupTaskPluginControllerTest(t)
	key := "upload-sync-probe"
	cleanupTaskPluginControllerRuntime(t, key)
	source := taskPluginControllerTestSource(key, "1.0.0")
	taskPluginSyncState.Lock()
	taskPluginSyncState.hashes[key] = "stale-hash"
	taskPluginSyncState.errors[key] = "stale compile error"
	taskPluginSyncState.Unlock()
	body, err := common.Marshal(map[string]any{"source": source})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task", strings.NewReader(string(body)))
	context.Request.Header.Set("Content-Type", "application/json")
	UploadTaskPlugin(context)

	assert.Contains(t, recorder.Body.String(), `"success":true`)
	stored, err := model.GetTaskPluginVersion(key, "")
	require.NoError(t, err)
	taskPluginSyncState.Lock()
	syncedHash := taskPluginSyncState.hashes[key]
	_, hasSyncError := taskPluginSyncState.errors[key]
	taskPluginSyncState.Unlock()
	assert.Equal(t, stored.SourceHash, syncedHash)
	assert.False(t, hasSyncError)
}

func TestActivateTaskPluginRefreshesRuntimeSyncState(t *testing.T) {
	setupTaskPluginControllerTest(t)
	key := "activate-sync-probe"
	cleanupTaskPluginControllerRuntime(t, key)
	v1Source := taskPluginControllerTestSource(key, "1.0.0")
	v2Source := taskPluginControllerTestSource(key, "2.0.0")
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: key, APIVersion: 1, Version: "1.0.0", Source: model.LongText(v1Source), SourceHash: "hash-v1", Enabled: true,
	}))
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: key, APIVersion: 1, Version: "2.0.0", Source: model.LongText(v2Source), SourceHash: "hash-v2", Enabled: true,
	}))
	_, err := jsplugin.DefaultRegistry.Register(v1Source, jsplugin.Options{Key: key, Version: "1.0.0"})
	require.NoError(t, err)
	taskPluginSyncState.Lock()
	taskPluginSyncState.hashes[key] = "hash-v1"
	taskPluginSyncState.errors[key] = "stale compile error"
	taskPluginSyncState.Unlock()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "key", Value: key}}
	context.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task/"+key+"/activate", strings.NewReader(`{"version":"2.0.0"}`))
	context.Request.Header.Set("Content-Type", "application/json")
	ActivateTaskPlugin(context)

	assert.Contains(t, recorder.Body.String(), `"success":true`)
	runtimePlugin, ok := jsplugin.DefaultRegistry.Get(key)
	require.True(t, ok)
	assert.Equal(t, "2.0.0", runtimePlugin.Meta.Version)
	taskPluginSyncState.Lock()
	syncedHash := taskPluginSyncState.hashes[key]
	_, hasSyncError := taskPluginSyncState.errors[key]
	taskPluginSyncState.Unlock()
	assert.Equal(t, "hash-v2", syncedHash)
	assert.False(t, hasSyncError)
}

func TestSyncTaskPluginsPublishesOneGenerationForWholeBatch(t *testing.T) {
	setupTaskPluginControllerTest(t)
	firstKey := "batch-sync-first"
	secondKey := "batch-sync-second"
	cleanupTaskPluginControllerRuntime(t, firstKey)
	cleanupTaskPluginControllerRuntime(t, secondKey)
	firstSource := taskPluginControllerTestSource(firstKey, "1.0.0")
	secondSource := taskPluginControllerTestSource(secondKey, "1.0.0")
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: firstKey, APIVersion: 1, Version: "1.0.0", Source: model.LongText(firstSource), SourceHash: "first-hash", Enabled: true,
	}))
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: secondKey, APIVersion: 1, Version: "1.0.0", Source: model.LongText(secondSource), SourceHash: "second-hash", Enabled: true,
	}))
	before := jsplugin.DefaultRegistry.Generation().Number

	SyncTaskPluginsOnce()

	assert.Equal(t, before+1, jsplugin.DefaultRegistry.Generation().Number)
	_, firstRegistered := jsplugin.DefaultRegistry.Get(firstKey)
	_, secondRegistered := jsplugin.DefaultRegistry.Get(secondKey)
	assert.True(t, firstRegistered)
	assert.True(t, secondRegistered)

	published := jsplugin.DefaultRegistry.Generation()
	SyncTaskPluginsOnce()
	assert.Same(t, published, jsplugin.DefaultRegistry.Generation())
}

func TestTaskPluginRuntimeExposesDatabaseRevisionAheadOfLocalGeneration(t *testing.T) {
	setupTaskPluginControllerTest(t)
	key := "runtime-revision-probe"
	cleanupTaskPluginControllerRuntime(t, key)
	taskPluginSyncState.Lock()
	previousRebuild := taskPluginSyncState.lastRebuild
	taskPluginSyncState.Unlock()
	t.Cleanup(func() {
		taskPluginSyncState.Lock()
		taskPluginSyncState.lastRebuild = previousRebuild
		taskPluginSyncState.Unlock()
	})

	v1Source := taskPluginControllerTestSource(key, "1.0.0")
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: key, APIVersion: 1, Version: "1.0.0",
		Source: model.LongText(v1Source), SourceHash: "runtime-v1", Enabled: true,
	}))
	require.NoError(t, syncTaskPluginsOnce())
	localGeneration := jsplugin.DefaultRegistry.Generation().Number
	taskPluginSyncState.Lock()
	syncedRevision := taskPluginSyncState.lastRebuild.DatabaseRevision
	taskPluginSyncState.Unlock()

	v2Source := taskPluginControllerTestSource(key, "2.0.0")
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: key, APIVersion: 1, Version: "2.0.0",
		Source: model.LongText(v2Source), SourceHash: "runtime-v2", Enabled: true,
	}))
	require.NoError(t, model.ActivateTaskPlugin(key, "2.0.0"))

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/plugin/task/runtime/status", nil)
	GetTaskPluginRuntime(context)

	var response struct {
		Success bool                    `json:"success"`
		Data    taskPluginRuntimeStatus `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, localGeneration, response.Data.CurrentGeneration)
	assert.NotZero(t, response.Data.GenerationPublishedAt)
	assert.NotEqual(t, syncedRevision, response.Data.DatabaseRevision)
	assert.Equal(t, "success", response.Data.LastRebuild.Status)
	assert.Equal(t, syncedRevision, response.Data.LastRebuild.DatabaseRevision)
	assert.Empty(t, response.Data.PluginErrors)

	active, ok := jsplugin.DefaultRegistry.Get(key)
	require.True(t, ok)
	assert.Equal(t, "1.0.0", active.Meta.Version)
}

func TestTaskPluginRuntimeReportsPluginLevelCompileErrors(t *testing.T) {
	setupTaskPluginControllerTest(t)
	key := "runtime-error-probe"
	cleanupTaskPluginControllerRuntime(t, key)
	taskPluginSyncState.Lock()
	previousRebuild := taskPluginSyncState.lastRebuild
	taskPluginSyncState.Unlock()
	t.Cleanup(func() {
		taskPluginSyncState.Lock()
		taskPluginSyncState.lastRebuild = previousRebuild
		taskPluginSyncState.Unlock()
	})
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: key, APIVersion: 1, Version: "1.0.0",
		Source: "export const meta = {", SourceHash: "broken-source", Enabled: true,
	}))
	require.NoError(t, syncTaskPluginsOnce())

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/plugin/task/runtime/status", nil)
	GetTaskPluginRuntime(context)

	var response struct {
		Success bool                    `json:"success"`
		Data    taskPluginRuntimeStatus `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, "partial", response.Data.LastRebuild.Status)
	assert.Equal(t, response.Data.DatabaseRevision, response.Data.LastRebuild.DatabaseRevision)
	assert.GreaterOrEqual(t, response.Data.LastRebuild.PluginErrorCount, 1)
	assert.NotEmpty(t, response.Data.PluginErrors[key])
	_, registered := jsplugin.DefaultRegistry.Get(key)
	assert.False(t, registered)
}

func TestTaskPluginRuntimeSurvivesDatabaseSyncFailure(t *testing.T) {
	setupTaskPluginControllerTest(t)
	key := "runtime-database-failure"
	cleanupTaskPluginControllerRuntime(t, key)
	taskPluginSyncState.Lock()
	previousRebuild := taskPluginSyncState.lastRebuild
	taskPluginSyncState.Unlock()
	t.Cleanup(func() {
		taskPluginSyncState.Lock()
		taskPluginSyncState.lastRebuild = previousRebuild
		taskPluginSyncState.Unlock()
	})

	source := taskPluginControllerTestSource(key, "1.0.0")
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: key, APIVersion: 1, Version: "1.0.0",
		Source: model.LongText(source), SourceHash: "runtime-database-v1", Enabled: true,
	}))
	require.NoError(t, syncTaskPluginsOnce())
	generation := jsplugin.DefaultRegistry.Generation().Number
	taskPluginSyncState.Lock()
	syncedRevision := taskPluginSyncState.lastRebuild.DatabaseRevision
	taskPluginSyncState.Unlock()

	sqlDatabase, err := model.DB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDatabase.Close())
	require.Error(t, syncTaskPluginsOnce())

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/plugin/task/runtime/status", nil)
	GetTaskPluginRuntime(context)

	var response struct {
		Success bool                    `json:"success"`
		Data    taskPluginRuntimeStatus `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, generation, response.Data.CurrentGeneration)
	assert.Equal(t, syncedRevision, response.Data.DatabaseRevision)
	assert.Equal(t, "database snapshot unavailable", response.Data.DatabaseError)
	assert.Equal(t, "failed", response.Data.LastRebuild.Status)
	assert.Equal(t, syncedRevision, response.Data.LastRebuild.DatabaseRevision)
	assert.Contains(t, response.Data.LastRebuild.Error, "sync task plugins")
}

func TestSyncTaskPluginsCachesRejectedDesiredSourceWithoutLosingIncumbent(t *testing.T) {
	setupTaskPluginControllerTest(t)
	pluginKey := "sync-retained-plugin"
	ownerKey := "sync-retained-owner"
	cleanupTaskPluginControllerRuntime(t, pluginKey)
	cleanupTaskPluginControllerRuntime(t, ownerKey)
	v1Source := taskPluginControllerChannelSource(pluginKey, "1.0.0", 9001)
	v2Source := taskPluginControllerChannelSource(pluginKey, "2.0.0", 9002)
	ownerSource := taskPluginControllerChannelSource(ownerKey, "1.0.0", 9002)
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: pluginKey, APIVersion: 1, Version: "1.0.0", Source: model.LongText(v1Source), SourceHash: "retained-v1", Enabled: true,
	}))
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: ownerKey, APIVersion: 1, Version: "1.0.0", Source: model.LongText(ownerSource), SourceHash: "owner-v1", Enabled: true,
	}))
	require.NoError(t, syncTaskPluginsOnce())

	incumbent, ok := jsplugin.DefaultRegistry.Get(pluginKey)
	require.True(t, ok)
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: pluginKey, APIVersion: 1, Version: "2.0.0", Source: model.LongText(v2Source), SourceHash: "retained-v2", Enabled: true,
	}))
	require.NoError(t, model.ActivateTaskPlugin(pluginKey, "2.0.0"))

	require.NoError(t, syncTaskPluginsOnce())
	rejectedGeneration := jsplugin.DefaultRegistry.Generation()
	for range 2 {
		require.NoError(t, syncTaskPluginsOnce())
		active, found := jsplugin.DefaultRegistry.Get(pluginKey)
		require.True(t, found)
		assert.Same(t, incumbent, active)
		assert.Equal(t, "2.0.0", jsplugin.DefaultRegistry.OverridePlugins()[pluginKey].Meta.Version)
		assert.Same(t, incumbent, jsplugin.DefaultRegistry.ActiveOverridePlugins()[pluginKey])
		assert.Contains(t, jsplugin.DefaultRegistry.RoutingErrors()[pluginKey], "channelType 9002 conflicts")
		taskPluginSyncState.Lock()
		assert.Equal(t, "retained-v2", taskPluginSyncState.hashes[pluginKey])
		taskPluginSyncState.Unlock()
		assert.Same(t, rejectedGeneration, jsplugin.DefaultRegistry.Generation())
	}

	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{
		Key: ownerKey, APIVersion: 1, Version: "2.0.0",
		Source: model.LongText(taskPluginControllerChannelSource(ownerKey, "2.0.0", 9003)), SourceHash: "owner-v2", Enabled: true,
	}))
	require.NoError(t, model.ActivateTaskPlugin(ownerKey, "2.0.0"))
	require.NoError(t, syncTaskPluginsOnce())
	active, ok := jsplugin.DefaultRegistry.Get(pluginKey)
	require.True(t, ok)
	assert.Equal(t, "2.0.0", active.Meta.Version)
	assert.NotContains(t, jsplugin.DefaultRegistry.RoutingErrors(), pluginKey)
}

const dryRunPluginSource = `
export const meta = {apiVersion: 1, key: "dryrun-probe", name: "DryRun", version: "1.0.0", author: {name: "Test"}, models: ["doc-1"], fetchMode: "per_task"};
export function buildSubmitRequest(payload) {
	if (!payload || !payload.model) { throw new Error("model required"); }
	return {model: payload.model};
}
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
export const native = { info: function(ctx, task) { return "task:" + task.id; } };
`

func runTaskPluginDryRun(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "key", Value: "dryrun-probe"}}
	context.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task/dryrun-probe/dryrun", strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	DryRunTaskPlugin(context)
	return recorder
}

func TestDryRunTaskPluginExecutesHookAndRendererMember(t *testing.T) {
	setupTaskPluginControllerTest(t)
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{Key: "dryrun-probe", APIVersion: 1, Version: "1.0.0", Source: dryRunPluginSource, SourceHash: "hash", Enabled: true}))

	hookRecorder := runTaskPluginDryRun(t, `{"hook":"buildSubmitRequest","args":[{"model":"doc-1"}]}`)
	assert.Contains(t, hookRecorder.Body.String(), `"success":true`)
	assert.Contains(t, hookRecorder.Body.String(), `"model":"doc-1"`)

	memberRecorder := runTaskPluginDryRun(t, `{"hook":"native","member":"info","args":[{}, {"id":"t-1"}]}`)
	assert.Contains(t, memberRecorder.Body.String(), `"success":true`)
	assert.Contains(t, memberRecorder.Body.String(), "task:t-1")
}

func TestDryRunTaskPluginReportsUnknownHook(t *testing.T) {
	setupTaskPluginControllerTest(t)
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{Key: "dryrun-probe", APIVersion: 1, Version: "1.0.0", Source: dryRunPluginSource, SourceHash: "hash", Enabled: true}))

	recorder := runTaskPluginDryRun(t, `{"hook":"missingHook"}`)

	assert.Contains(t, recorder.Body.String(), `"success":false`)
	assert.Contains(t, recorder.Body.String(), `plugin export \"missingHook\" not found`)
}

func TestDryRunTaskPluginSurfacesBadArgumentErrors(t *testing.T) {
	setupTaskPluginControllerTest(t)
	require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{Key: "dryrun-probe", APIVersion: 1, Version: "1.0.0", Source: dryRunPluginSource, SourceHash: "hash", Enabled: true}))

	malformedRecorder := runTaskPluginDryRun(t, `{"hook":"buildSubmitRequest","args":[{`)
	assert.Contains(t, malformedRecorder.Body.String(), `"success":false`)

	rejectedRecorder := runTaskPluginDryRun(t, `{"hook":"buildSubmitRequest","args":[{}]}`)
	assert.Contains(t, rejectedRecorder.Body.String(), `"success":false`)
	assert.Contains(t, rejectedRecorder.Body.String(), "model required")
}

func TestUploadTaskPluginPreflightConflict(t *testing.T) {
	setupTaskPluginControllerTest(t)
	enabledFalse := false
	tests := []struct {
		name        string
		key         string
		enabled     *bool
		force       bool
		wantSuccess bool
		wantError   string
	}{
		{
			name:      "enabled conflict rejected",
			key:       "preflight-reject",
			wantError: "channelType 50 conflicts",
		},
		{
			name:        "force saves despite conflict",
			key:         "preflight-force",
			force:       true,
			wantSuccess: true,
		},
		{
			name:        "disabled skips preflight",
			key:         "preflight-disabled",
			enabled:     &enabledFalse,
			wantSuccess: true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			cleanupTaskPluginControllerRuntime(t, testCase.key)
			source := taskPluginControllerChannelSource(testCase.key, "1.0.0", 50)
			payload := map[string]any{"source": source}
			if testCase.enabled != nil {
				payload["enabled"] = *testCase.enabled
			}
			if testCase.force {
				payload["force"] = true
			}
			body, err := common.Marshal(payload)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task", strings.NewReader(string(body)))
			context.Request.Header.Set("Content-Type", "application/json")

			UploadTaskPlugin(context)

			if testCase.wantSuccess {
				assert.Contains(t, recorder.Body.String(), `"success":true`)
				_, err = model.GetTaskPluginVersion(testCase.key, "")
				require.NoError(t, err)
				return
			}
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), testCase.wantError)
			assert.Contains(t, recorder.Body.String(), "kling")
			var count int64
			require.NoError(t, model.DB.Model(&model.TaskPlugin{}).Where("key = ?", testCase.key).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestUploadTaskPluginLocalizesUnknownMetaField(t *testing.T) {
	require.NoError(t, i18n.Init())
	body, err := common.Marshal(map[string]any{
		"source": `export const meta = { futureField: true };`,
	})
	require.NoError(t, err)
	for _, tc := range []struct {
		language string
		message  string
	}{
		{"en", `Plugin metadata contains an unknown field "futureField". If this plugin was downloaded from the official marketplace, it may require a newer version of new-api. Try updating new-api and installing the plugin again.`},
		{"zh-CN", "插件元数据包含未知字段“futureField”。如果插件来自官方市场，可能需要更高版本的 new-api。请尝试更新 new-api 后重新安装插件。"},
		{"zh-TW", "外掛中繼資料包含未知欄位「futureField」。如果外掛來自官方市集，可能需要較新版本的 new-api。請嘗試更新 new-api 後重新安裝外掛。"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("Accept-Language", tc.language)
			UploadTaskPlugin(c)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.False(t, response.Success)
			assert.Equal(t, tc.message, response.Message)
		})
	}
}

func TestUploadTaskPluginRejectsMetaViolatingV1Schema(t *testing.T) {
	setupTaskPluginControllerTest(t)
	cases := []struct {
		name          string
		meta          string
		expectedError string
	}{
		{
			name:          "key with uppercase characters",
			meta:          `{apiVersion: 1, key: "Bad-Key", name: "Bad", version: "1.0.0", author: {name: "Test"}, models: ["doc-1"], fetchMode: "per_task"}`,
			expectedError: "plugin meta key must match",
		},
		{
			name:          "version that is not semver",
			meta:          `{apiVersion: 1, key: "bad-plugin", name: "Bad", version: "one", author: {name: "Test"}, models: ["doc-1"], fetchMode: "per_task"}`,
			expectedError: "plugin meta version must be semver",
		},
		{
			name:          "unsupported fetch mode",
			meta:          `{apiVersion: 1, key: "bad-plugin", name: "Bad", version: "1.0.0", author: {name: "Test"}, models: ["doc-1"], fetchMode: "sometimes"}`,
			expectedError: "plugin meta fetchMode must be per_task or batch",
		},
		{
			name:          "empty model list",
			meta:          `{apiVersion: 1, key: "bad-plugin", name: "Bad", version: "1.0.0", author: {name: "Test"}, models: [], fetchMode: "per_task"}`,
			expectedError: "plugin meta models must contain at least one model",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := "export const meta = " + testCase.meta + `;
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
			body, err := common.Marshal(map[string]any{"source": source})
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task", strings.NewReader(string(body)))
			context.Request.Header.Set("Content-Type", "application/json")

			UploadTaskPlugin(context)

			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), testCase.expectedError)
			var count int64
			require.NoError(t, model.DB.Model(&model.TaskPlugin{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestDeletePureFactoryPluginIsRejected(t *testing.T) {
	for _, query := range []string{"", "?force=true"} {
		t.Run(query, func(t *testing.T) {
			setupTaskPluginControllerTest(t)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Params = gin.Params{{Key: "key", Value: "kling"}, {Key: "version", Value: "1.0.0"}}
			context.Request = httptest.NewRequest(http.MethodDelete, "/api/plugin/task/kling/versions/1.0.0"+query, nil)

			DeleteTaskPluginVersion(context)

			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), "factory plugins cannot be deleted")
			_, ok := jsplugin.DefaultRegistry.Get("kling")
			assert.True(t, ok)
		})
	}
}

func TestUploadTaskPluginSourceSha256(t *testing.T) {
	setupTaskPluginControllerTest(t)
	tests := []struct {
		name        string
		key         string
		withHash    bool
		hash        string
		wantSuccess bool
		wantError   string
	}{
		{
			name:        "matching hash succeeds",
			key:         "sha256-match",
			withHash:    true,
			wantSuccess: true,
		},
		{
			name:      "mismatching hash rejected",
			key:       "sha256-mismatch",
			withHash:  true,
			hash:      "deadbeef",
			wantError: "plugin source sha256 mismatch",
		},
		{
			name:        "absent field unchanged",
			key:         "sha256-absent",
			wantSuccess: true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			cleanupTaskPluginControllerRuntime(t, testCase.key)
			source := taskPluginControllerTestSource(testCase.key, "1.0.0")
			payload := map[string]any{"source": source}
			if testCase.withHash {
				hash := testCase.hash
				if hash == "" {
					hash = "  " + strings.ToUpper(fmt.Sprintf("%x", sha256.Sum256([]byte(source)))) + "  "
				}
				payload["sourceSha256"] = hash
			}
			body, err := common.Marshal(payload)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task", strings.NewReader(string(body)))
			context.Request.Header.Set("Content-Type", "application/json")

			UploadTaskPlugin(context)

			if testCase.wantSuccess {
				assert.Contains(t, recorder.Body.String(), `"success":true`)
				_, err = model.GetTaskPluginVersion(testCase.key, "")
				require.NoError(t, err)
				return
			}
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), testCase.wantError)
			var count int64
			require.NoError(t, model.DB.Model(&model.TaskPlugin{}).Where("key = ?", testCase.key).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func setupTaskPluginMarketplaceSourcesTest(t *testing.T) {
	t.Helper()
	setupTaskPluginControllerTest(t)
	originalMap := common.OptionMap
	common.OptionMapRWMutex.Lock()
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalMap
		common.OptionMapRWMutex.Unlock()
	})
}

func TestGetTaskPluginMarketplaceSourcesDefaultWhenUnset(t *testing.T) {
	setupTaskPluginMarketplaceSourcesTest(t)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/plugin/task/marketplace/sources", nil)

	GetTaskPluginMarketplaceSources(context)

	var response struct {
		Success bool                                  `json:"success"`
		Data    []setting.TaskPluginMarketplaceSource `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Equal(t, []setting.TaskPluginMarketplaceSource{
		{Name: "Official", IndexURL: "https://www.newapi.ai/api/v1/plugins/index.json"},
		{Name: "GitHub", IndexURL: "https://raw.githubusercontent.com/QuantumNous/new-api-plugins/main/index.json"},
	}, response.Data)
	var count int64
	require.NoError(t, model.DB.Model(&model.Option{}).Where("key = ?", setting.TaskPluginMarketplaceSourcesKey).Count(&count).Error)
	assert.Zero(t, count)
}

func TestUpdateTaskPluginMarketplaceSourcesRoundTrip(t *testing.T) {
	setupTaskPluginMarketplaceSourcesTest(t)
	payload := []setting.TaskPluginMarketplaceSource{
		{Name: "Mirror", IndexURL: "https://example.com/plugins/index.json"},
		{Name: "Official", IndexURL: "https://www.newapi.ai/api/v1/plugins/index.json"},
	}
	body, err := common.Marshal(payload)
	require.NoError(t, err)
	putRecorder := httptest.NewRecorder()
	putContext, _ := gin.CreateTestContext(putRecorder)
	putContext.Request = httptest.NewRequest(http.MethodPut, "/api/plugin/task/marketplace/sources", strings.NewReader(string(body)))
	putContext.Request.Header.Set("Content-Type", "application/json")

	UpdateTaskPluginMarketplaceSources(putContext)

	assert.Contains(t, putRecorder.Body.String(), `"success":true`)

	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/plugin/task/marketplace/sources", nil)
	GetTaskPluginMarketplaceSources(getContext)

	var response struct {
		Success bool                                  `json:"success"`
		Data    []setting.TaskPluginMarketplaceSource `json:"data"`
	}
	require.NoError(t, common.Unmarshal(getRecorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, payload, response.Data)
}

func TestUpdateTaskPluginMarketplaceSourcesValidation(t *testing.T) {
	setupTaskPluginMarketplaceSourcesTest(t)
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "empty name",
			body:    `[{"name":"","index_url":"https://example.com/index.json"}]`,
			wantErr: "marketplace source name is required",
		},
		{
			name:    "invalid URL",
			body:    `[{"name":"Local","index_url":"not-a-url"}]`,
			wantErr: "marketplace source index_url must be an absolute http(s) URL",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/plugin/task/marketplace/sources", strings.NewReader(testCase.body))
			context.Request.Header.Set("Content-Type", "application/json")

			UpdateTaskPluginMarketplaceSources(context)

			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Contains(t, recorder.Body.String(), testCase.wantErr)
			assert.Empty(t, common.OptionMap[setting.TaskPluginMarketplaceSourcesKey])
			var count int64
			require.NoError(t, model.DB.Model(&model.Option{}).Where("key = ?", setting.TaskPluginMarketplaceSourcesKey).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestTaskPluginDisplayOrderAndMetadata(t *testing.T) {
	setupTaskPluginControllerTest(t)
	const website = "https://example.com/plugins"
	keys := []string{"display-low", "display-zero", "display-beta", "display-alpha"}
	priorities := []int{-10, 0, 50, 50}
	for i, key := range keys {
		source := strings.Replace(taskPluginControllerTestSource(key, "1.0.0"), "apiVersion: 1,", fmt.Sprintf("apiVersion: 1, sortPriority: %d, website: %q,", priorities[i], website), 1)
		_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
		require.NoError(t, err)
		cleanupTaskPluginControllerRuntime(t, key)
		require.NoError(t, model.SaveTaskPlugin(&model.TaskPlugin{Key: key, Version: "1.0.0", APIVersion: 1, Source: model.LongText(source), Enabled: true}))
	}
	for _, endpoint := range []string{"list", "options"} {
		t.Run(endpoint, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			var metas []jsplugin.Meta
			if endpoint == "list" {
				ListTaskPlugins(context)
				var response struct {
					Success bool
					Data    []taskPluginListItem
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				require.True(t, response.Success)
				for _, item := range response.Data {
					metas = append(metas, item.Meta)
				}
			} else {
				GetTaskPluginOptions(context)
				var response struct {
					Success bool
					Data    []jsplugin.Meta
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				require.True(t, response.Success)
				metas = response.Data
			}
			var ordered []string
			for _, meta := range metas {
				if strings.HasPrefix(meta.Key, "display-") {
					ordered = append(ordered, meta.Key)
					assert.Equal(t, website, meta.Website)
				}
			}
			assert.Equal(t, []string{"display-alpha", "display-beta", "display-zero", "display-low"}, ordered)
		})
	}
}

func TestUploadTaskPluginStoresSidecarIconAndServesIt(t *testing.T) {
	setupTaskPluginControllerTest(t)
	const key = "icon-sidecar"
	source := taskPluginControllerTestSource(key, "1.0.0")
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })
	svgIcon := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><circle cx="4" cy="4" r="4"/></svg>`))
	upload := func(icon string) *httptest.ResponseRecorder {
		body, err := common.Marshal(map[string]any{"source": source, "icon": icon})
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task", bytes.NewReader(body))
		context.Request.Header.Set("Content-Type", "application/json")
		UploadTaskPlugin(context)
		return recorder
	}

	rejected := upload("data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`)))
	assert.Contains(t, rejected.Body.String(), `"success":false`)
	assert.Contains(t, rejected.Body.String(), "script")
	_, err := model.GetTaskPluginVersion(key, "1.0.0")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "a rejected icon must not store the plugin")

	accepted := upload(svgIcon)
	require.Contains(t, accepted.Body.String(), `"success":true`)
	assert.Contains(t, accepted.Body.String(), `"has_icon":true`)
	assert.NotContains(t, accepted.Body.String(), "base64,", "icon bytes never travel inside detail JSON")
	stored, err := model.GetTaskPluginVersion(key, "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, svgIcon, string(stored.Icon))

	item := listTaskPluginItem(t, key)
	assert.True(t, item.HasIcon)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "key", Value: key}}
	context.Request = httptest.NewRequest(http.MethodGet, "/api/plugin/task/"+key+"/icon", nil)
	GetTaskPluginIcon(context)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "image/svg+xml", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, recorder.Body.String(), "<circle")

	missing := httptest.NewRecorder()
	context, _ = gin.CreateTestContext(missing)
	context.Params = gin.Params{{Key: "key", Value: "no-such-plugin"}}
	context.Request = httptest.NewRequest(http.MethodGet, "/api/plugin/task/no-such-plugin/icon", nil)
	GetTaskPluginIcon(context)
	assert.Equal(t, http.StatusNotFound, missing.Code)
}
