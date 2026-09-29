package setting

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTaskPluginDisabledFactoryKeysTest(t *testing.T) {
	t.Helper()
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

func TestTaskPluginDisabledFactoryKeysRoundTripAndDedupe(t *testing.T) {
	setupTaskPluginDisabledFactoryKeysTest(t)

	assert.Empty(t, GetTaskPluginDisabledFactoryKeys())
	assert.False(t, IsTaskPluginFactoryDisabled("kling"))

	require.NoError(t, SetTaskPluginDisabledFactoryKeysOption([]string{"kling", "sora", "kling", " hailuo "}))
	assert.Equal(t, []string{"hailuo", "kling", "sora"}, GetTaskPluginDisabledFactoryKeys())
	assert.Equal(t, `["hailuo","kling","sora"]`, common.OptionMap[TaskPluginDisabledFactoryKeysKey])
	assert.True(t, IsTaskPluginFactoryDisabled("kling"))
	assert.True(t, IsTaskPluginFactoryDisabled("hailuo"))
	assert.False(t, IsTaskPluginFactoryDisabled("google"))
}

func TestTaskPluginDisabledFactoryKeysBadJSONReturnsEmpty(t *testing.T) {
	setupTaskPluginDisabledFactoryKeysTest(t)

	for _, testCase := range []struct {
		name string
		raw  string
	}{
		{name: "absent", raw: ""},
		{name: "null", raw: "null"},
		{name: "object", raw: "{}"},
		{name: "number", raw: "1"},
		{name: "truncated", raw: `["kling"`},
		{name: "not json", raw: "kling"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			common.OptionMapRWMutex.Lock()
			if testCase.raw == "" {
				delete(common.OptionMap, TaskPluginDisabledFactoryKeysKey)
			} else {
				common.OptionMap[TaskPluginDisabledFactoryKeysKey] = testCase.raw
			}
			common.OptionMapRWMutex.Unlock()

			assert.Empty(t, GetTaskPluginDisabledFactoryKeys())
			assert.False(t, IsTaskPluginFactoryDisabled("kling"))
		})
	}
}
