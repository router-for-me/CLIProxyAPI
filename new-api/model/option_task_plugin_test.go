package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskPluginEnabledOptionUpdatesRegistry(t *testing.T) {
	originalEnabled := constant.TaskPluginEnabled
	originalMap := common.OptionMap
	common.OptionMap = map[string]string{}
	const key = "option-master-off"
	source := `
export const meta = {apiVersion: 1, key: "option-master-off", name: "Option Master", version: "1.0.0", author: {name: "Test"}, models: ["option-master-model"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.RegisterFactory(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() {
		constant.TaskPluginEnabled = originalEnabled
		jsplugin.DefaultRegistry.SetEnabled(originalEnabled)
		common.OptionMap = originalMap
	})

	_, ok := jsplugin.DefaultRegistry.Get(key)
	require.True(t, ok)

	require.NoError(t, updateOptionMap("TaskPluginEnabled", "false"))

	assert.False(t, constant.TaskPluginEnabled)
	assert.Equal(t, "false", common.OptionMap["TaskPluginEnabled"])
	_, ok = jsplugin.DefaultRegistry.Get(key)
	assert.False(t, ok)

	require.NoError(t, updateOptionMap("TaskPluginEnabled", "true"))
	_, ok = jsplugin.DefaultRegistry.Get(key)
	assert.True(t, ok)
}

func TestTaskPluginDisabledFactoryKeysOptionUpdatesRegistry(t *testing.T) {
	originalMap := common.OptionMap
	common.OptionMap = map[string]string{}
	const key = "option-factory-off"
	source := `
export const meta = {apiVersion: 1, key: "option-factory-off", name: "Option Factory", version: "1.0.0", author: {name: "Test"}, models: ["option-factory-model"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.RegisterFactory(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() {
		jsplugin.DefaultRegistry.SetDisabledFactoryKeys(nil)
		common.OptionMap = originalMap
	})

	_, ok := jsplugin.DefaultRegistry.Get(key)
	require.True(t, ok)

	require.NoError(t, updateOptionMap(setting.TaskPluginDisabledFactoryKeysKey, `["option-factory-off"]`))

	assert.Equal(t, `["option-factory-off"]`, common.OptionMap[setting.TaskPluginDisabledFactoryKeysKey])
	_, ok = jsplugin.DefaultRegistry.Get(key)
	assert.False(t, ok)
	assert.Equal(t, []string{key}, jsplugin.DefaultRegistry.Snapshot().DisabledFactory)
}
