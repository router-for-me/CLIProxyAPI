package jsplugin

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistrySetDisabledFactoryKeysHidesFactoryPlugin(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.RegisterFactory(routingTestPluginSource(
		"factory-off",
		50,
		`["factory-off-model"]`,
		`routes: [
			{method: "POST", path: "/vendor/factory-off", type: "submit", action: "generate", decode: "decodeVideo", render: "videoCreated"}
		],
		protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],`,
		`export const native = {
			decodeVideo: function(ctx) { return {kind: "submit", model: "factory-off-model", requestBody: ctx.body.value}; },
			videoCreated: function(ctx, task) { return task; }
		};
		`+routingProtocolExport("openai_responses"),
	), Options{})
	require.NoError(t, err)

	_, ok := registry.Get("factory-off")
	require.True(t, ok)
	generation := registry.Generation()
	before := generation.Number
	_, ok = generation.LookupDeclaredRoute("POST", "/vendor/factory-off")
	require.True(t, ok)
	_, ok = generation.LookupEndpoint("POST", "/v1/responses", "factory-off-model")
	require.True(t, ok)

	registry.SetDisabledFactoryKeys([]string{"factory-off"})
	_, ok = registry.Get("factory-off")
	assert.False(t, ok)
	generation = registry.Generation()
	assert.Greater(t, generation.Number, before)
	_, ok = generation.LookupDeclaredRoute("POST", "/vendor/factory-off")
	assert.False(t, ok)
	_, ok = generation.LookupEndpoint("POST", "/v1/responses", "factory-off-model")
	assert.False(t, ok)
	for _, plugin := range generation.Plugins() {
		assert.NotEqual(t, "factory-off", plugin.Meta.Key)
	}
	for _, route := range generation.Routes() {
		assert.NotEqual(t, "factory-off", route.Plugin.Meta.Key)
	}

	snapshot := registry.Snapshot()
	require.Len(t, snapshot.Factory, 1)
	assert.Equal(t, "factory-off", snapshot.Factory[0].Key)
	assert.Equal(t, []string{"factory-off"}, snapshot.DisabledFactory)

	registry.SetDisabledFactoryKeys(nil)
	plugin, ok := registry.Get("factory-off")
	require.True(t, ok)
	assert.Equal(t, "factory-off", plugin.Meta.Key)
	assert.Empty(t, registry.Snapshot().DisabledFactory)
	_, ok = registry.Generation().LookupDeclaredRoute("POST", "/vendor/factory-off")
	assert.True(t, ok)
}

func TestRegistrySetDisabledFactoryKeysLeavesEnabledOverrideServing(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registerTestPlugin(registry, "1.0.0-factory", true))
	require.NoError(t, registerTestPlugin(registry, "1.0.0-override", false))

	registry.SetDisabledFactoryKeys([]string{"test"})
	plugin, ok := registry.Get("test")
	require.True(t, ok)
	assert.Equal(t, "1.0.0-override", plugin.Meta.Version)

	snapshot := registry.Snapshot()
	require.Len(t, snapshot.Factory, 1)
	assert.Equal(t, "1.0.0-factory", snapshot.Factory[0].Version)
	assert.Equal(t, []string{"test"}, snapshot.DisabledFactory)
}

func TestRegistrySetDisabledFactoryKeysEmptySetDoesNotBumpGeneration(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registerTestPlugin(registry, "1.0.0-factory", true))
	before := registry.Generation().Number

	registry.SetDisabledFactoryKeys(nil)
	assert.Equal(t, before, registry.Generation().Number)
	registry.SetDisabledFactoryKeys([]string{})
	assert.Equal(t, before, registry.Generation().Number)
	registry.SetDisabledFactoryKeys([]string{"", "  "})
	assert.Equal(t, before, registry.Generation().Number)
}

func TestRegistrySnapshotDisabledFactoryIsSorted(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registerTestPlugin(registry, "1.0.0", true))
	_, err := registry.RegisterFactory(routingTestPluginSource("other", 0, `["other-model"]`, "", ""), Options{})
	require.NoError(t, err)

	registry.SetDisabledFactoryKeys([]string{"other", "test"})
	assert.Equal(t, []string{"other", "test"}, registry.Snapshot().DisabledFactory)
	require.Len(t, registry.Snapshot().Factory, 2)
}
