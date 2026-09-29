package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskPluginChannelSelectionFiltersBothCachePaths(t *testing.T) {
	truncateTables(t)
	priority := int64(0)
	weight := uint(1)
	baseURL := "https://example.com"
	alphaSetting := `{"task_plugin_key":"alpha"}`
	betaSetting := `{"task_plugin_key":"beta"}`
	channels := []Channel{
		{Id: 900001, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Name: "alpha", Models: "shared", Group: "default", Priority: &priority, Weight: &weight, BaseURL: &baseURL, Setting: &alphaSetting},
		{Id: 900002, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Name: "beta", Models: "shared", Group: "default", Priority: &priority, Weight: &weight, BaseURL: &baseURL, Setting: &betaSetting},
		{Id: 900003, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "ordinary", Models: "ordinary", Group: "default", Priority: &priority, Weight: &weight},
		{Id: 900004, Type: constant.ChannelTypeKling, Status: common.ChannelStatusEnabled, Name: "legacy-alpha", Models: "legacy", Group: "default", Priority: &priority, Weight: &weight},
		{Id: 900005, Type: constant.ChannelTypeJimeng, Status: common.ChannelStatusEnabled, Name: "legacy-beta", Models: "legacy", Group: "default", Priority: &priority, Weight: &weight},
	}
	for i := range channels {
		require.NoError(t, channels[i].Insert())
	}

	selected, err := GetChannel("default", "shared", 0, identityFilters("alpha", nil))
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "alpha", selected.Name)
	selected, err = GetChannel("default", "shared", 0, identityFilters("", nil))
	require.NoError(t, err)
	assert.Nil(t, selected)
	selected, err = GetChannel("default", "ordinary", 0, identityFilters("", nil))
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "ordinary", selected.Name)
	selected, err = GetChannel("default", "legacy", 0, identityFilters("legacy-alpha", []int{constant.ChannelTypeKling}))
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "legacy-alpha", selected.Name)
	selected, err = GetChannel("default", "legacy", 0, identityFilters("legacy-alpha", []int{constant.ChannelTypeKling, constant.ChannelTypeJimeng}))
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Contains(t, []string{"legacy-alpha", "legacy-beta"}, selected.Name)
}

func identityFilters(key string, channelTypes []int) []dto.ChannelFilter {
	return []dto.ChannelFilter{{
		Kind:                   dto.FilterTaskPluginIdentity,
		TaskPluginKey:          key,
		TaskPluginChannelTypes: channelTypes,
	}}
}

func TestSharedPluginKeysFilterBothChannelSources(t *testing.T) {
	truncateTables(t)
	originalMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = originalMemoryCache; InitChannelCache() })
	filters := []dto.ChannelFilter{{Kind: dto.FilterTaskPluginIdentity, TaskPluginKey: "alpha", TaskPluginKeys: []string{"alpha", "beta"}}}
	var abilities []Ability
	for index, key := range []string{"alpha", "beta", "unrelated"} {
		setting := `{"task_plugin_key":"` + key + `"}`
		channel := Channel{Id: 910001 + index, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Name: key, Models: "shared", Group: "default", Setting: &setting}
		require.NoError(t, channel.Insert())
		abilities = append(abilities, Ability{ChannelId: channel.Id, Model: "shared", Group: "default", Enabled: true})
		matches, _ := ChannelSatisfiesFilters(&channel, "shared", filters)
		assert.Equal(t, index < 2, matches)
	}
	assert.Equal(t, abilities[:2], filterAbilitiesByConstraints(abilities, "shared", filters))
	InitChannelCache()
	channelSyncLock.RLock()
	kept, emptied := filterCandidateIDs([]int{910001, 910002, 910003}, "shared", filters)
	channelSyncLock.RUnlock()
	assert.Equal(t, []int{910001, 910002}, kept)
	assert.Empty(t, emptied)
}

func TestNewAPIChannelServesExtendedTaskPlugins(t *testing.T) {
	truncateTables(t)
	priority := int64(0)
	weight := uint(1)
	baseURL := "https://gateway.example"
	extended := `{"task_extend_plugin_keys":["alpha","gamma"]}`
	single := `{"task_plugin_key":"delta"}`
	channels := []Channel{
		{Id: 920001, Type: constant.ChannelTypeNewAPI, Status: common.ChannelStatusEnabled, Name: "gateway", Models: "shared,chat", Group: "default", Priority: &priority, Weight: &weight, BaseURL: &baseURL, Setting: &extended},
		{Id: 920002, Type: constant.ChannelTypeNewAPI, Status: common.ChannelStatusEnabled, Name: "gateway-single", Models: "shared", Group: "default", Priority: &priority, Weight: &weight, BaseURL: &baseURL, Setting: &single},
	}
	for i := range channels {
		require.NoError(t, channels[i].Insert())
	}

	selected, err := GetChannel("default", "shared", 0, identityFilters("alpha", nil))
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "gateway", selected.Name)
	selected, err = GetChannel("default", "shared", 0, identityFilters("delta", nil))
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "gateway-single", selected.Name, "a single task_plugin_key still binds a New API channel")
	selected, err = GetChannel("default", "shared", 0, identityFilters("beta", nil))
	require.NoError(t, err)
	assert.Nil(t, selected, "unbound plugins never reach the gateway channel")
	shared := []dto.ChannelFilter{{Kind: dto.FilterTaskPluginIdentity, TaskPluginKey: "beta", TaskPluginKeys: []string{"beta", "gamma"}}}
	selected, err = GetChannel("default", "shared", 0, shared)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "gateway", selected.Name, "a bound shared-model candidate admits the channel")
	selected, err = GetChannel("default", "chat", 0, identityFilters("", nil))
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "gateway", selected.Name, "requests without a pinned plugin keep using the gateway")
}
